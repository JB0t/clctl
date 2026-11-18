// Package llm talks to a local model server for summaries and embeddings: Ollama's own
// API, or an OpenAI-compatible one such as llama.cpp's llama-server when the URL ends
// in /v1.
package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultURL is where Ollama listens by default.
const DefaultURL = "http://localhost:11434"

// DefaultMaxTokens bounds one OpenAI-style completion when MaxTokens is unset, so a
// model stuck repeating itself stops; summaries are a few hundred tokens.
const DefaultMaxTokens = 1536

var thinkRE = regexp.MustCompile(`(?s)<think>.*?</think>`)

type Client struct {
	URL   string
	Model string
	// OpenAI selects the OpenAI-compatible API (/v1/models, /v1/chat/completions,
	// /v1/embeddings) instead of Ollama's /api/*; New sets it when the URL ends in /v1.
	OpenAI bool
	// ContextTokens is the context window requested per call (Ollama's num_ctx; an
	// OpenAI-style server has its context set when it starts).
	ContextTokens int
	// Threads is Ollama's num_thread; 0 lets the server choose. On CPUs with
	// performance and efficiency cores, fewer threads than cores is often faster.
	// OpenAI-style servers set their threads when they start.
	Threads int
	// MaxTokens bounds one OpenAI-style completion (max_tokens); 0 means
	// DefaultMaxTokens. A thinking model's reasoning counts towards it.
	MaxTokens int
	HTTP      *http.Client
}

func New(url, model string, contextTokens int) *Client {
	if url == "" {
		url = DefaultURL
	}
	if contextTokens <= 0 {
		contextTokens = 8192
	}
	url = strings.TrimRight(url, "/")
	// Local CPU inference over a long thread can take minutes.
	return &Client{URL: url, Model: model, OpenAI: strings.HasSuffix(url, "/v1"), ContextTokens: contextTokens,
		HTTP: &http.Client{Timeout: 20 * time.Minute}}
}

// Models lists the models the server has.
func (c *Client) Models() ([]string, error) {
	if c.OpenAI {
		return c.openAIModels()
	}
	response, err := c.HTTP.Get(c.URL + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("no model server at %s (%w); start Ollama there, or set llm_container", c.URL, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// llama.cpp's llama-server and other OpenAI-style servers
		// answer /v1/models but not Ollama's /api/* endpoints.
		if probe, err := c.HTTP.Get(c.URL + "/v1/models"); err == nil {
			probe.Body.Close()
			if probe.StatusCode == http.StatusOK {
				return nil, fmt.Errorf("%s serves the OpenAI API (llama.cpp's llm-server?); set llm_url to %s/v1 to use it", c.URL, c.URL)
			}
		}
		return nil, fmt.Errorf("%s/api/tags returned HTTP %d; is llm_url an Ollama server? (end it in /v1 for an OpenAI-style one)", c.URL, response.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tags); err != nil {
		return nil, err
	}
	var names []string
	for _, model := range tags.Models {
		names = append(names, model.Name)
	}
	return names, nil
}

// ResolveModel picks the configured model, or the server's only/first model.
func (c *Client) ResolveModel() error {
	models, err := c.Models()
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("the model server at %s has no models", c.URL)
	}
	if c.Model == "" {
		// Default to the first chat model; embedding models cannot summarise.
		for _, name := range models {
			if !strings.Contains(name, "embed") {
				c.Model = name
				return nil
			}
		}
		return fmt.Errorf("the model server at %s has only embedding models: %v", c.URL, models)
	}
	for _, name := range models {
		if name == c.Model || name == c.Model+":latest" {
			c.Model = name
			return nil
		}
	}
	return fmt.Errorf("model %q is not on %s; it has: %v", c.Model, c.URL, models)
}

// Stats reports how a generation went.
type Stats struct {
	PromptTokens, OutputTokens int
	Duration                   time.Duration
}

// Generate runs one non-streaming completion.
func (c *Client) Generate(system, prompt string) (string, Stats, error) {
	if c.OpenAI {
		return c.openAIGenerate(system, prompt)
	}
	options := map[string]any{"num_ctx": c.ContextTokens, "temperature": 0}
	if c.Threads > 0 {
		options["num_thread"] = c.Threads
	}
	body, _ := json.Marshal(map[string]any{
		"model":   c.Model,
		"system":  system,
		"prompt":  prompt,
		"stream":  false,
		"options": options,
	})
	response, err := c.HTTP.Post(c.URL+"/api/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", Stats{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", Stats{}, err
	}
	var result struct {
		Response        string `json:"response"`
		Error           string `json:"error"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
		TotalDuration   int64  `json:"total_duration"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", Stats{}, fmt.Errorf("unexpected reply from %s: %s", c.URL, truncate(string(data), 200))
	}
	if result.Error != "" {
		return "", Stats{}, errors.New(result.Error)
	}
	return result.Response, Stats{result.PromptEvalCount, result.EvalCount, time.Duration(result.TotalDuration)}, nil
}

// Embed returns one embedding per input, using an embedding model such as
// nomic-embed-text.
func (c *Client) Embed(model string, inputs []string) ([][]float32, error) {
	if c.OpenAI {
		return c.openAIEmbed(model, inputs)
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": inputs})
	response, err := c.HTTP.Post(c.URL+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, errors.New(result.Error)
	}
	if len(result.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("got %d embeddings for %d inputs", len(result.Embeddings), len(inputs))
	}
	return result.Embeddings, nil
}

func truncate(text string, limit int) string {
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

// apiError is the error body of OpenAI-style servers.
type apiError struct {
	Message string `json:"message"`
}

// openAIPost sends a JSON request to an OpenAI-style endpoint and decodes the reply.
func (c *Client) openAIPost(path string, request, out any) error {
	body, _ := json.Marshal(request)
	response, err := c.HTTP.Post(c.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("no model server at %s (%w)", c.URL, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var failure struct {
		Error *apiError `json:"error"`
	}
	if json.Unmarshal(data, &failure) == nil && failure.Error != nil {
		return fmt.Errorf("model server: %s", failure.Error.Message)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s%s returned HTTP %d: %s", c.URL, path, response.StatusCode, truncate(string(data), 200))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected reply from %s: %s", c.URL, truncate(string(data), 200))
	}
	return nil
}

func (c *Client) openAIModels() ([]string, error) {
	response, err := c.HTTP.Get(c.URL + "/models")
	if err != nil {
		return nil, fmt.Errorf("no model server at %s (%w); start it (llm_start, e.g. 'llm-server start digest')", c.URL, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// llama-server answers 503 while it loads the model.
		return nil, fmt.Errorf("model server at %s is not ready (HTTP %d)", c.URL, response.StatusCode)
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&models); err != nil {
		return nil, err
	}
	var names []string
	for _, model := range models.Data {
		names = append(names, model.ID)
	}
	return names, nil
}

func (c *Client) openAIGenerate(system, prompt string) (string, Stats, error) {
	started := time.Now()
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	err := c.openAIPost("/chat/completions", map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": prompt},
		},
		"temperature":  0,
		"max_tokens":   maxTokens,
		"cache_prompt": true, // llama-server: reuse the shared prefix (a task's background)
	}, &result)
	if err != nil {
		return "", Stats{}, err
	}
	if len(result.Choices) == 0 {
		return "", Stats{}, fmt.Errorf("the model server at %s returned no answer", c.URL)
	}
	stats := Stats{result.Usage.PromptTokens, result.Usage.CompletionTokens, time.Since(started)}
	if result.Choices[0].FinishReason == "length" {
		return "", stats, fmt.Errorf("the answer ran past %d tokens (the model may be repeating itself; raise llm_max_tokens if not)", maxTokens)
	}
	// Thinking models put their reasoning first; only the answer is wanted.
	return strings.TrimSpace(thinkRE.ReplaceAllString(result.Choices[0].Message.Content, "")), stats, nil
}

func (c *Client) openAIEmbed(model string, inputs []string) ([][]float32, error) {
	var result struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := c.openAIPost("/embeddings", map[string]any{"model": model, "input": inputs}, &result); err != nil {
		return nil, err
	}
	if len(result.Data) != len(inputs) {
		return nil, fmt.Errorf("got %d embeddings for %d inputs", len(result.Data), len(inputs))
	}
	sort.Slice(result.Data, func(i, j int) bool { return result.Data[i].Index < result.Data[j].Index })
	embeddings := make([][]float32, len(result.Data))
	for index, item := range result.Data {
		embeddings[index] = item.Embedding
	}
	return embeddings, nil
}
