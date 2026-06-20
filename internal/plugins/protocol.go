package plugins

import "encoding/json"

const (
	// ProtocolVersion is the JSONL protocol version spoken by this harness.
	ProtocolVersion = "0.1.0"

	// methodInitialize requests plugin metadata and tool schemas.
	methodInitialize = "initialize"

	// methodToolExecute requests one plugin tool execution.
	methodToolExecute = "tool.execute"

	// contentTypeText identifies plain text response content.
	contentTypeText = "text"
)

// request is one JSONL-RPC request sent from harness to a plugin.
type request struct {
	// ID correlates the request with one plugin response.
	ID string `json:"id"`

	// Method names the requested plugin operation.
	Method string `json:"method"`

	// Params stores the method-specific request object.
	Params any `json:"params,omitempty"`
}

// response is one JSONL-RPC response sent from a plugin to harness.
type response struct {
	// ID correlates the response with one harness request.
	ID string `json:"id"`

	// Result stores the method-specific success object.
	Result json.RawMessage `json:"result,omitempty"`

	// Error stores the method failure, when any.
	Error *responseError `json:"error,omitempty"`
}

// responseError describes one plugin protocol failure.
type responseError struct {
	// Code is an optional plugin-specific error code.
	Code string `json:"code,omitempty"`

	// Message is the human-readable error text.
	Message string `json:"message"`
}

// initializeParams is sent during plugin startup.
type initializeParams struct {
	// ProtocolVersion is the highest protocol version this harness
	// supports.
	ProtocolVersion string `json:"protocolVersion"`
}

// initializeResult describes plugin capabilities after startup.
type initializeResult struct {
	// Name is the plugin's preferred display name.
	Name string `json:"name"`

	// Tools are model-callable tool schemas exposed by the plugin.
	Tools []toolSpec `json:"tools,omitempty"`
}

// toolSpec is the wire representation of a model-callable tool schema.
type toolSpec struct {
	// Name is the model-facing tool identifier.
	Name string `json:"name"`

	// Description explains when and how the model should call the tool.
	Description string `json:"description"`

	// Parameters is a JSON Schema object describing tool arguments.
	Parameters json.RawMessage `json:"parameters"`
}

// toolExecuteParams is sent when the model calls a plugin tool.
type toolExecuteParams struct {
	// CallID is the model provider's tool-call identifier.
	CallID string `json:"callID"`

	// Name is the model-facing tool name to execute.
	Name string `json:"name"`

	// Arguments stores the raw JSON argument object from the model.
	Arguments json.RawMessage `json:"arguments"`
}

// toolExecuteResult is returned after a plugin tool call completes.
type toolExecuteResult struct {
	// Content stores model-visible output parts.
	Content []contentPart `json:"content,omitempty"`
}

// contentPart is one typed plugin output part.
type contentPart struct {
	// Type identifies how Text should be interpreted.
	Type string `json:"type"`

	// Text stores plain text for contentTypeText parts.
	Text string `json:"text"`
}
