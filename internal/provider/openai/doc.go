// Package openai implements a stdlib-only OpenAI-compatible streaming client.
//
// The client supports Chat Completions for broad OpenAI-compatible providers
// and the Responses API for richer OpenAI output items such as reasoning
// summaries and response identifiers. It intentionally avoids SDK dependencies
// so provider behavior stays behind the model.Client interface without
// changing the runtime dependency boundary.
package openai
