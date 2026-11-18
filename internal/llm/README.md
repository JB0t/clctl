# llm

<!-- docgen:begin id=internal/llm:overview scope=internal/llm hash=837d5cddfcdc -->
Package llm connects to a local large language model server to generate text and compute embeddings. It supports both Ollama's native API and OpenAI-compatible endpoints. The Client type holds configuration like the server URL, model name, and context size. Methods like Generate and Embed handle text completion and vector creation. The ResolveModel method selects a model from the server if none is configured. The OpenAI field controls which API style to use based on the URL suffix. The package handles HTTP requests with a custom client that times out after twenty minutes. It can list available models and check if a model is present on the server. The package also deals with errors from the server and parses responses to extract tokens used and generation stats.
<!-- docgen:end id=internal/llm:overview -->

<!-- docgen:begin id=internal/llm:reference scope=internal/llm hash=837d5cddfcdc -->
## Reference

### `llm.go`

- **`DefaultURL`** (constant): DefaultURL is where Ollama listens by default.
- **`Client`** (struct)
- **`New`** (function)
- **`Client.Models`** (method): Models lists the models the server has.
- **`Client.ResolveModel`** (method): ResolveModel picks the configured model, or the server's only/first model.
- **`Stats`** (struct): Stats reports how a generation went.
- **`Client.Generate`** (method): Generate runs one non-streaming completion.
- **`Client.Embed`** (method): Embed returns one embedding per input, using an embedding model such as nomic-embed-text.
<!-- docgen:end id=internal/llm:reference -->
