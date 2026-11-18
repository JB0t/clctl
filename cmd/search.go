package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/cache"
	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/index"
	"github.com/JB0t/clctl/internal/llm"
	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/notes"
	"github.com/JB0t/clctl/internal/tasks"
)

const defaultEmbedModel = "nomic-embed-text"

func indexPath() string { return index.DefaultPath(filepath.Dir(cache.DefaultPath())) }

func embedModel(app *App) string {
	cfg, _ := app.Config()
	if model := cfg.Get(config.KeyLLMEmbedModel); model != "" {
		return model
	}
	return defaultEmbedModel
}

func embedder(client *llm.Client, model string) index.Embedder {
	return func(texts []string) ([][]float32, error) { return client.Embed(model, texts) }
}

// noteSources returns every note under the notes root (skipping workspaces and digests).
func noteSources(root string) map[string][2]string {
	sources := map[string][2]string{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && (entry.Name() == notes.WorkspacesDir || strings.HasPrefix(entry.Name(), ".")) {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".md") || entry.Name() == "digest.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		sources[filepath.ToSlash(rel)] = [2]string{notes.Day(root, path), string(data)}
		return nil
	})
	return sources
}

// commentSources returns the comments on your open tasks, one source per comment.
func commentSources(app *App) (map[string][2]string, error) {
	c, err := app.Cache()
	if err != nil {
		return nil, err
	}
	me, err := app.UserID()
	if err != nil {
		return nil, err
	}
	client, err := app.Backend()
	if err != nil {
		return nil, err
	}
	mine := tasks.AssignedTo(tasks.Open(c.Tasks), me)
	sources := map[string][2]string{}
	var lock sync.Mutex
	var group sync.WaitGroup
	limiter := make(chan struct{}, 6)
	var firstErr error
	for _, task := range mine {
		group.Add(1)
		go func() {
			defer group.Done()
			limiter <- struct{}{}
			defer func() { <-limiter }()
			comments, err := client.Comments(task.Ref())
			lock.Lock()
			defer lock.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", task.Ref(), err)
				}
				return
			}
			for _, comment := range comments {
				text := markdown.PlainText(comment.Comment)
				if text == "" {
					continue
				}
				day := millisTime(string(comment.Date)).Format("2006-01-02")
				sources[task.Ref()+"#"+string(comment.ID)] = [2]string{day, "# " + task.Ref() + " " + task.Name + "\n" + text}
			}
		}()
	}
	group.Wait()
	return sources, firstErr
}

func newIndexCmd(app *App) *cobra.Command {
	var withComments, rebuild bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build or update the local search index (notes, and optionally comments)",
		Long: `Splits your notes (and with --comments, the comments on your open tasks) into chunks
by heading, embeds them with a local embedding model (llm_embed_model, default
nomic-embed-text) and stores them in $XDG_CACHE_HOME/clctl/index.json. Only new or
changed chunks are embedded. Search it with 'clctl search'.

--rebuild drops the index and embeds everything again, e.g. after switching embedding
servers (an index made with Ollama's nomic-embed-text and one made with llm-server's
have the same model name but slightly different vectors). Add --comments to keep
comments in the rebuilt index.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := app.NotesRoot()
			if err != nil && !withComments {
				return err
			}
			client, stop, err := app.EmbedServer()
			if err != nil {
				return err
			}
			defer stop()
			model := embedModel(app)
			idx := index.Load(indexPath())
			if rebuild {
				fmt.Fprintf(os.Stderr, "Rebuilding: dropping %d indexed chunks.\n", len(idx.Chunks))
				idx.Chunks = nil
			}
			sources := map[string][2]string{}
			if root != "" {
				notesFound := noteSources(root)
				for key, value := range notesFound {
					sources[key] = value
				}
				removed := idx.Prune(func(source string) bool {
					_, isNote := notesFound[source]
					return isNote || strings.Contains(source, "#")
				})
				if removed > 0 {
					fmt.Fprintf(os.Stderr, "Dropped %d chunks of deleted notes.\n", removed)
				}
			}
			if withComments {
				comments, err := commentSources(app)
				if err != nil {
					return err
				}
				for key, value := range comments {
					sources[key] = value
				}
			}
			started := time.Now()
			embedded, err := idx.Update(model, sources, embedder(client, model))
			if err != nil {
				return err
			}
			if err := idx.Save(); err != nil {
				return err
			}
			fmt.Printf("Indexed %d sources (%d chunks); embedded %d new or changed chunks in %s.\n",
				len(sources), len(idx.Chunks), embedded, time.Since(started).Round(time.Second))
			return nil
		},
	}
	cmd.Flags().BoolVar(&withComments, "comments", false, "also index the comments on your open tasks")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "drop the index and embed everything again")
	return cmd
}

func newSemanticSearchCmd(app *App) *cobra.Command {
	var limit int
	var onlyNotes, onlyComments bool
	cmd := &cobra.Command{
		Use:     "search QUERY",
		Short:   "Search notes and comments by meaning (run 'clctl index' first)",
		Args:    cobra.MinimumNArgs(1),
		Example: `  clctl search "postgres connection timeout in the dask cluster"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			idx := index.Load(indexPath())
			if len(idx.Chunks) == 0 {
				return fmt.Errorf("the index is empty; run 'clctl index' first")
			}
			client, stop, err := app.EmbedServer()
			if err != nil {
				return err
			}
			defer stop()
			vectors, err := client.Embed(idx.Model, []string{"search_query: " + strings.Join(args, " ")})
			if err != nil {
				return err
			}
			filter := func(chunk index.Chunk) bool {
				isComment := strings.Contains(chunk.Source, "#")
				return !(onlyNotes && isComment) && !(onlyComments && !isComment)
			}
			for _, result := range idx.Search(vectors[0], limit, filter) {
				where := result.Source
				if result.Title != "" {
					where += " > " + result.Title
				}
				snippet := strings.Join(strings.Fields(result.Text), " ")
				if len([]rune(snippet)) > 200 {
					snippet = string([]rune(snippet)[:200]) + "…"
				}
				fmt.Printf("%.2f  %s  %s\n      %s\n", result.Score, orUndated(result.Day), where, snippet)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "number of results")
	cmd.Flags().BoolVar(&onlyNotes, "notes", false, "only notes")
	cmd.Flags().BoolVar(&onlyComments, "comments", false, "only comments")
	cmd.MarkFlagsMutuallyExclusive("notes", "comments")
	return cmd
}
