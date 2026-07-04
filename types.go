package main

// ExecutionState represents the lifecycle status of a step.
type ExecutionState string

const (
	StatePending ExecutionState = "pending"
	StateRunning ExecutionState = "running"
	StateSuccess ExecutionState = "success"
	StateError   ExecutionState = "error"
)

// StepType represents the type of operation a step performs.
type StepType string

const (
	StepCreate  StepType = "create"
	StepModify  StepType = "modify"
	StepCommand StepType = "command"
)

// Step represents a single structured task from the blueprint.
type Step struct {
	Index        int            `json:"index"`
	Type         StepType       `json:"type"`
	Path         string         `json:"path,omitempty"` // For file operations
	Description  string         `json:"description"`
	TargetBlock  string         `json:"target_block,omitempty"` // For modification searches
	Instructions string         `json:"instructions"`           // Instructions for Ollama on what to write/replace
	Feedback     string         `json:"feedback,omitempty"`     // Interactive correction feedback
	Command      string         `json:"command,omitempty"`      // For shell execution
	Status       ExecutionState `json:"status"`
	ErrorMsg     string         `json:"error_msg,omitempty"`
}

// OllamaModel represents an available model in local Ollama storage.
type OllamaModel struct {
	Name string `json:"name"`
}

// OllamaTagsResponse corresponds to Ollama's /api/tags response.
type OllamaTagsResponse struct {
	Models []OllamaModel `json:"models"`
}

// OllamaOptions represents optional configuration parameters for model execution.
type OllamaOptions struct {
	NumCtx int `json:"num_ctx,omitempty"`
}

// OllamaGenerateRequest represents the request body for /api/generate.
type OllamaGenerateRequest struct {
	Model   string        `json:"model"`
	Prompt  string        `json:"prompt"`
	Stream  bool          `json:"stream"`
	Options OllamaOptions `json:"options,omitempty"`
}

// OllamaGenerateResponse represents the response body from /api/generate.
type OllamaGenerateResponse struct {
	Response string `json:"response"`
}
