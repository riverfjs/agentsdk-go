package middleware

import (
	"context"
	"fmt"
	"strings"
)

// ErrorGuardMiddleware tracks consecutive tool errors and injects system
// messages to force the agent to report failures to the user rather than
// retrying indefinitely.
type ErrorGuardMiddleware struct {
	threshold         int
	consecutiveErrors int
	errorMarkers      []string
}

// ErrorGuardOption configures the error guard middleware.
type ErrorGuardOption func(*ErrorGuardMiddleware)

// WithErrorThreshold sets the number of consecutive errors before intervention.
// Default is 2 (same as nanobot).
func WithErrorThreshold(threshold int) ErrorGuardOption {
	return func(eg *ErrorGuardMiddleware) {
		if threshold > 0 {
			eg.threshold = threshold
		}
	}
}

// WithErrorMarkers adds custom error detection patterns beyond the defaults.
func WithErrorMarkers(markers []string) ErrorGuardOption {
	return func(eg *ErrorGuardMiddleware) {
		eg.errorMarkers = append(eg.errorMarkers, markers...)
	}
}

// NewErrorGuardMiddleware creates a middleware that detects consecutive tool
// failures and forces agent feedback to the user.
func NewErrorGuardMiddleware(opts ...ErrorGuardOption) *ErrorGuardMiddleware {
	eg := &ErrorGuardMiddleware{
		threshold: 2,
		errorMarkers: []string{
			"⚠️ ERROR:",
			"⚠️ STDERR:",
			"Exit code",
			"command failed:",
			"execution failed:",
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(eg)
		}
	}
	return eg
}

func (eg *ErrorGuardMiddleware) Name() string {
	return "error-guard"
}

func (eg *ErrorGuardMiddleware) BeforeAgent(ctx context.Context, st *State) error {
	// Reset counter at the start of a new agent run (new user message)
	eg.consecutiveErrors = 0
	return nil
}

func (eg *ErrorGuardMiddleware) BeforeModel(ctx context.Context, st *State) error {
	return nil
}

func (eg *ErrorGuardMiddleware) AfterModel(ctx context.Context, st *State) error {
	return nil
}

func (eg *ErrorGuardMiddleware) BeforeTool(ctx context.Context, st *State) error {
	return nil
}

func (eg *ErrorGuardMiddleware) AfterTool(ctx context.Context, st *State) error {
	if st == nil || st.ToolResult == nil {
		return nil
	}

	// Use existing structToMap helper to extract fields (same as trace middleware)
	payload := structToMap(st.ToolResult, map[string]string{
		"Name":     "name",
		"Output":   "content",
		"Metadata": "metadata",
	})

	// Extract output and metadata
	var output string
	var metadata map[string]any

	if payload != nil {
		if content, ok := payload["content"].(string); ok {
			output = content
		}
		if meta, ok := payload["metadata"].(map[string]any); ok {
			metadata = meta
		}
	}

	// Check for errors in metadata (set by agent.go when tool execution fails)
	hasError := false
	if metadata != nil {
		if isErr, ok := metadata["is_error"].(bool); ok && isErr {
			hasError = true
		}
	}

	// Also check output content for error markers
	if !hasError && output != "" {
		for _, marker := range eg.errorMarkers {
			if strings.Contains(output, marker) {
				hasError = true
				break
			}
		}
	}

	if hasError {
		eg.consecutiveErrors++
		// Store error count in state for model adapter to check
		if st.Values == nil {
			st.Values = map[string]any{}
		}
		st.Values["error_guard.consecutive_errors"] = eg.consecutiveErrors
		if eg.consecutiveErrors >= eg.threshold {
			st.Values["error_guard.should_intervene"] = true
			st.Values["error_guard.intervention_message"] = eg.InterventionMessage()
		}
	} else {
		eg.consecutiveErrors = 0
		if st.Values != nil {
			delete(st.Values, "error_guard.consecutive_errors")
			delete(st.Values, "error_guard.should_intervene")
			delete(st.Values, "error_guard.intervention_message")
		}
	}

	return nil
}

func (eg *ErrorGuardMiddleware) AfterAgent(ctx context.Context, st *State) error {
	return nil
}

// ShouldIntervene returns true if consecutive errors have reached the threshold.
// This can be checked by the model adapter to inject system messages.
func (eg *ErrorGuardMiddleware) ShouldIntervene() bool {
	return eg.consecutiveErrors >= eg.threshold
}

// GetErrorCount returns the current consecutive error count.
func (eg *ErrorGuardMiddleware) GetErrorCount() int {
	return eg.consecutiveErrors
}

// InterventionMessage returns a system message to inject into the conversation.
func (eg *ErrorGuardMiddleware) InterventionMessage() string {
	return fmt.Sprintf(
		"⚠️ SYSTEM: Multiple tool failures detected (%d consecutive errors). "+
			"You MUST report these errors to the user immediately and ask for guidance. "+
			"Do not continue making tool calls without user input.",
		eg.consecutiveErrors,
	)
}

