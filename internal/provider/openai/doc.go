// Package openai implements a stdlib-only OpenAI-compatible streaming client.
//
// The client targets the Chat Completions streaming shape used by OpenAI and
// many local or hosted compatible providers. It intentionally avoids SDK
// dependencies so provider behavior stays behind the model.Client interface
// without changing the runtime dependency boundary.
package openai
