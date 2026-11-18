package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/ui"
)

var taskAction = map[string]string{annotationTaskAction: "true"}

func newCommentsCmd(app *App) *cobra.Command {
	return newGroup(app, "comments", "Add, read and tidy task comments", nil,
		newAddCommentCmd(app), newGetCommentsCmd(app), newFixTablesCmd(app))
}

func newAddCommentCmd(app *App) *cobra.Command {
	var taskID, comment string
	var codeblock, bulletlist bool
	cmd := &cobra.Command{
		Use:         "add",
		Short:       "Add a comment to a task",
		Long:        `Plain comments keep inline Markdown (links, code, bold). A literal "\n" in --comment is a line break.`,
		Annotations: taskAction,
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(taskID, TaskScope{Everyone: true})
			if err != nil {
				return err
			}
			if comment == "" {
				if comment, err = ui.Ask("Comment", ""); err != nil {
					return err
				}
			}
			if strings.TrimSpace(comment) == "" {
				return errors.New("the comment is empty")
			}
			var ops []markdown.Op
			for _, line := range strings.Split(strings.ReplaceAll(comment, `\n`, "\n"), "\n") {
				attributes := map[string]any{}
				switch {
				case codeblock:
					attributes["code-block"] = map[string]any{"code-block": "plain"}
					if line != "" {
						ops = append(ops, markdown.Op{Text: line, Attributes: map[string]any{}})
					}
				case bulletlist:
					attributes["list"] = map[string]any{"list": "bullet"}
					fallthrough
				default:
					if !codeblock {
						ops = append(ops, markdown.Inline(line, nil)...)
					}
				}
				ops = append(ops, markdown.Op{Text: "\n", Attributes: attributes})
			}
			client, err := app.Backend()
			if err != nil {
				return err
			}
			if _, err := client.PostComment(task.Ref(), ops); err != nil {
				return err
			}
			fmt.Println("Comment added to", task.Ref()+".")
			return nil
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().StringVar(&comment, "comment", "", "comment text (prompt when omitted)")
	cmd.Flags().BoolVar(&codeblock, "codeblock", false, "format as a code block")
	cmd.Flags().BoolVar(&bulletlist, "bulletlist", false, "one bullet per line")
	cmd.MarkFlagsMutuallyExclusive("codeblock", "bulletlist")
	return cmd
}

func newGetCommentsCmd(app *App) *cobra.Command {
	var taskID string
	var export, asJSON bool
	cmd := &cobra.Command{
		Use:         "list",
		Aliases:     []string{"get"},
		Short:       "Show a task's comments",
		Annotations: taskAction,
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(taskID, TaskScope{Everyone: true})
			if err != nil {
				return err
			}
			client, err := app.Backend()
			if err != nil {
				return err
			}
			comments, err := client.Comments(task.Ref())
			if err != nil {
				return err
			}
			if export || asJSON {
				data, _ := json.MarshalIndent(map[string]any{"comments": comments}, "", "  ")
				if !export {
					fmt.Println(string(data))
					return nil
				}
				name := task.Ref() + "-comments.json"
				if err := os.WriteFile(name, data, 0o644); err != nil {
					return err
				}
				fmt.Println("Exported", len(comments), "comments to", name)
				return nil
			}
			if len(comments) == 0 {
				fmt.Println("No comments.")
			}
			// Oldest first reads like a conversation.
			for index := len(comments) - 1; index >= 0; index-- {
				comment := comments[index]
				when := ""
				if millis, err := strconv.ParseInt(string(comment.Date), 10, 64); err == nil {
					when = time.UnixMilli(millis).Format("2006-01-02 15:04")
				}
				fmt.Printf("── %s  %s\n%s\n\n", comment.User.DisplayName(), when, strings.TrimRight(comment.CommentText, "\n"))
			}
			return nil
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().BoolVar(&export, "export", false, "write <ID>-comments.json")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the raw JSON")
	return cmd
}

func newAttachFileCmd(app *App) *cobra.Command {
	var taskID string
	var files []string
	cmd := &cobra.Command{
		Use:         "attach",
		Short:       "Attach files to a task",
		Long:        "Attaches the --file paths, or lets you pick files under the current directory with fzf.",
		Annotations: taskAction,
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(taskID, TaskScope{Everyone: true})
			if err != nil {
				return err
			}
			if len(files) == 0 {
				if files, err = pickFiles(); err != nil {
					return err
				}
			}
			client, err := app.Backend()
			if err != nil {
				return err
			}
			for _, file := range files {
				if info, err := os.Stat(file); err != nil || info.IsDir() {
					return fmt.Errorf("not a file: %s", file)
				}
			}
			for _, file := range files {
				if _, err := client.UploadAttachment(task.Ref(), file); err != nil {
					return fmt.Errorf("attaching %s: %w", file, err)
				}
				fmt.Printf("Attached %s to %s.\n", file, task.Ref())
			}
			return nil
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().StringArrayVar(&files, "file", nil, "file to attach; repeat for several")
	return cmd
}

// pickFiles uses fzf's file walker, or asks for paths one per line without it.
func pickFiles() ([]string, error) {
	if ui.HasFzf() {
		args := append([]string{"--height=80%", "--layout=reverse", "--border", "--prompt=Files > "}, ui.FzfMultiArgs...)
		command := exec.Command("fzf", args...)
		command.Stdin, command.Stderr = os.Stdin, os.Stderr
		output, err := command.Output()
		if err != nil {
			return nil, ui.ErrCancelled
		}
		return strings.Fields(string(output)), nil
	}
	fmt.Println("Enter files to attach, one per line; a blank line finishes.")
	var files []string
	for {
		file, err := ui.Ask("File", "")
		if err != nil || file == "" {
			break
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, ui.ErrCancelled
	}
	return files, nil
}

func newExportCmd(app *App) *cobra.Command {
	var assignees []string
	var output string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write your open tasks to a dated Markdown file, by priority",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.Cache()
			if err != nil {
				return err
			}
			var ids []string
			if len(assignees) > 0 {
				if ids, err = resolveAssignees(app, assignees); err != nil {
					return err
				}
			} else {
				me, err := app.UserID()
				if err != nil {
					return err
				}
				ids = []string{me}
			}
			selected := tasks.AssignedTo(tasks.Open(c.Tasks), ids...)
			tasks.Sort(selected)
			date := time.Now().Format("2006-01-02")
			if output == "" {
				output = date + ".md"
			}
			var builder strings.Builder
			fmt.Fprintf(&builder, "# %s\n\n## General\n\n## Queue\n", date)
			current := "?"
			for _, task := range selected {
				priority := task.PriorityName()
				if priority == "" {
					priority = "no priority"
				}
				if priority != current {
					fmt.Fprintf(&builder, "\n### %s\n\n", strings.ToUpper(priority[:1])+priority[1:])
					current = priority
				}
				fmt.Fprintf(&builder, "- %s - %s\n", task.Ref(), task.Name)
			}
			if err := os.WriteFile(output, []byte(builder.String()), 0o644); err != nil {
				return err
			}
			abs, _ := filepath.Abs(output)
			fmt.Printf("Exported %d tasks to %s\n", len(selected), abs)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&assignees, "assignee", nil, "assignee name or email (default: you)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file (default YYYY-MM-DD.md)")
	return cmd
}

func newFixTablesCmd(app *App) *cobra.Command {
	var taskID string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "fix-tables",
		Short: "Turn Markdown tables in your existing comments into real tables",
		Long: `Scans your comments on one task (--task-id) or on all your open tasks, and rewrites
any Markdown table typed as plain text (| a | b | rows with a |---| separator) as a
table (a ClickUp table; Jira and the local store render it as a table too).
Formatting inside cells is kept. Only your own comments are changed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := app.Backend()
			if err != nil {
				return err
			}
			me, err := app.UserID()
			if err != nil {
				return err
			}
			var targets []string
			if taskID != "" {
				targets = []string{taskID}
			} else {
				c, err := app.Cache()
				if err != nil {
					return err
				}
				for _, task := range tasks.AssignedTo(tasks.Open(c.Tasks), me) {
					targets = append(targets, task.Ref())
				}
			}
			comments, tables := 0, 0
			for _, target := range targets {
				list, err := client.Comments(target)
				if err != nil {
					return fmt.Errorf("%s: %w", target, err)
				}
				for _, comment := range list {
					if string(comment.User.ID) != me {
						continue
					}
					fixed, count := markdown.FixTables(comment.Comment)
					if count == 0 {
						continue
					}
					first, _, _ := strings.Cut(strings.TrimSpace(comment.CommentText), "\n")
					if len(first) > 50 {
						first = first[:50]
					}
					if dryRun {
						fmt.Printf("%s: would fix %d table(s) in comment %s (%s)\n", target, count, comment.ID, first)
					} else {
						if err := client.UpdateComment(string(comment.ID), fixed); err != nil {
							return fmt.Errorf("%s comment %s: %w", target, comment.ID, err)
						}
						fmt.Printf("%s: fixed %d table(s) in comment %s (%s)\n", target, count, comment.ID, first)
					}
					comments++
					tables += count
				}
			}
			fmt.Printf("%d table(s) in %d comment(s) across %d task(s) scanned.\n", tables, comments, len(targets))
			return nil
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list the comments that would change")
	return cmd
}
