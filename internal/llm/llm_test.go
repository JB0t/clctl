package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIServer(t *testing.T) {
	finish := "stop"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-instruct"}]}`))
		case "/v1/chat/completions":
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request["temperature"] != float64(0) || request["model"] != "qwen3-instruct" {
				t.Errorf("request = %v", request)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"<think>hmm</think>\n- did it"},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
		case "/v1/embeddings":
			_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[2]},{"index":0,"embedding":[1]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL+"/v1/", "", 0)
	if !client.OpenAI || client.URL != server.URL+"/v1" {
		t.Fatalf("client = %+v", client)
	}
	if err := client.ResolveModel(); err != nil || client.Model != "qwen3-instruct" {
		t.Fatalf("ResolveModel = %v, %q", err, client.Model)
	}
	text, stats, err := client.Generate("system", "prompt")
	if err != nil || text != "- did it" || stats.PromptTokens != 12 {
		t.Fatalf("Generate = %q, %+v, %v", text, stats, err)
	}
	finish = "length"
	if _, _, err := client.Generate("system", "prompt"); err == nil {
		t.Fatal("expected an error for a cut-off answer")
	}
	vectors, err := client.Embed("nomic-embed-text", []string{"a", "b"})
	if err != nil || vectors[0][0] != 1 || vectors[1][0] != 2 {
		t.Fatalf("Embed = %v, %v", vectors, err)
	}

	ollama := New(server.URL, "", 0)
	if _, err := ollama.Models(); err == nil || !strings.Contains(err.Error(), "/v1") {
		t.Fatalf("Ollama client on an OpenAI server: %v", err)
	}
}
