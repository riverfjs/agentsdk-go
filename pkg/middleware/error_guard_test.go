package middleware

import (
	"context"
	"testing"
)

func TestErrorGuardMiddleware_Name(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	if eg.Name() != "error-guard" {
		t.Errorf("expected name 'error-guard', got %s", eg.Name())
	}
}

func TestErrorGuardMiddleware_ResetOnBeforeAgent(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	eg.consecutiveErrors = 5

	err := eg.BeforeAgent(context.Background(), &State{})
	if err != nil {
		t.Fatalf("BeforeAgent failed: %v", err)
	}

	if eg.consecutiveErrors != 0 {
		t.Errorf("expected consecutive errors to be reset to 0, got %d", eg.consecutiveErrors)
	}
}

func TestErrorGuardMiddleware_DetectMetadataError(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	st := &State{
		ToolResult: struct {
			Name     string
			Output   string
			Metadata map[string]any
		}{
			Name:   "test-tool",
			Output: "some output",
			Metadata: map[string]any{
				"is_error": true,
				"error":    "tool failed",
			},
		},
		Values: map[string]any{},
	}

	err := eg.AfterTool(context.Background(), st)
	if err != nil {
		t.Fatalf("AfterTool failed: %v", err)
	}

	if eg.consecutiveErrors != 1 {
		t.Errorf("expected 1 error, got %d", eg.consecutiveErrors)
	}

	if count, ok := st.Values["error_guard.consecutive_errors"].(int); !ok || count != 1 {
		t.Errorf("expected consecutive_errors=1 in state values, got %v", st.Values["error_guard.consecutive_errors"])
	}
}

func TestErrorGuardMiddleware_DetectOutputMarker(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	st := &State{
		ToolResult: struct {
			Name     string
			Output   string
			Metadata map[string]any
		}{
			Name:   "test-tool",
			Output: "⚠️ ERROR: command failed with exit code 1",
		},
		Values: map[string]any{},
	}

	err := eg.AfterTool(context.Background(), st)
	if err != nil {
		t.Fatalf("AfterTool failed: %v", err)
	}

	if eg.consecutiveErrors != 1 {
		t.Errorf("expected 1 error, got %d", eg.consecutiveErrors)
	}
}

func TestErrorGuardMiddleware_ResetOnSuccess(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	eg.consecutiveErrors = 3

	st := &State{
		ToolResult: struct {
			Name     string
			Output   string
			Metadata map[string]any
		}{
			Name:   "test-tool",
			Output: "success",
		},
		Values: map[string]any{
			"error_guard.consecutive_errors": 3,
		},
	}

	err := eg.AfterTool(context.Background(), st)
	if err != nil {
		t.Fatalf("AfterTool failed: %v", err)
	}

	if eg.consecutiveErrors != 0 {
		t.Errorf("expected errors to be reset to 0, got %d", eg.consecutiveErrors)
	}

	if _, exists := st.Values["error_guard.consecutive_errors"]; exists {
		t.Error("expected consecutive_errors to be removed from state values")
	}
}

func TestErrorGuardMiddleware_InterventionAtThreshold(t *testing.T) {
	eg := NewErrorGuardMiddleware(WithErrorThreshold(2))
	st := &State{
		Values: map[string]any{},
	}

	// First error
	st.ToolResult = struct {
		Name     string
		Output   string
		Metadata map[string]any
	}{
		Name:     "tool1",
		Output:   "⚠️ ERROR: failed",
		Metadata: map[string]any{},
	}
	_ = eg.AfterTool(context.Background(), st)

	if shouldIntervene, ok := st.Values["error_guard.should_intervene"].(bool); ok && shouldIntervene {
		t.Error("should not intervene after 1 error (threshold is 2)")
	}

	// Second error
	st.ToolResult = struct {
		Name     string
		Output   string
		Metadata map[string]any
	}{
		Name:   "tool2",
		Output: "⚠️ ERROR: failed again",
	}
	_ = eg.AfterTool(context.Background(), st)

	if shouldIntervene, ok := st.Values["error_guard.should_intervene"].(bool); !ok || !shouldIntervene {
		t.Error("should intervene after 2 errors (threshold is 2)")
	}

	if msg, ok := st.Values["error_guard.intervention_message"].(string); !ok || msg == "" {
		t.Error("intervention message should be set")
	}
}

func TestErrorGuardMiddleware_CustomThreshold(t *testing.T) {
	eg := NewErrorGuardMiddleware(WithErrorThreshold(3))
	if eg.threshold != 3 {
		t.Errorf("expected threshold 3, got %d", eg.threshold)
	}
}

func TestErrorGuardMiddleware_CustomMarkers(t *testing.T) {
	eg := NewErrorGuardMiddleware(WithErrorMarkers([]string{"CUSTOM_ERROR"}))

	st := &State{
		ToolResult: struct {
			Name     string
			Output   string
			Metadata map[string]any
		}{
			Name:   "test-tool",
			Output: "CUSTOM_ERROR: something went wrong",
		},
		Values: map[string]any{},
	}

	err := eg.AfterTool(context.Background(), st)
	if err != nil {
		t.Fatalf("AfterTool failed: %v", err)
	}

	if eg.consecutiveErrors != 1 {
		t.Errorf("expected 1 error with custom marker, got %d", eg.consecutiveErrors)
	}
}

func TestErrorGuardMiddleware_IgnoreNilState(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	err := eg.AfterTool(context.Background(), nil)
	if err != nil {
		t.Errorf("expected no error for nil state, got %v", err)
	}
}

func TestErrorGuardMiddleware_IgnoreNilToolResult(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	st := &State{ToolResult: nil}
	err := eg.AfterTool(context.Background(), st)
	if err != nil {
		t.Errorf("expected no error for nil tool result, got %v", err)
	}
}

func TestErrorGuardMiddleware_ShouldIntervene(t *testing.T) {
	eg := NewErrorGuardMiddleware(WithErrorThreshold(2))

	if eg.ShouldIntervene() {
		t.Error("should not intervene initially")
	}

	eg.consecutiveErrors = 1
	if eg.ShouldIntervene() {
		t.Error("should not intervene after 1 error")
	}

	eg.consecutiveErrors = 2
	if !eg.ShouldIntervene() {
		t.Error("should intervene after 2 errors")
	}
}

func TestErrorGuardMiddleware_InterventionMessage(t *testing.T) {
	eg := NewErrorGuardMiddleware()
	eg.consecutiveErrors = 3

	msg := eg.InterventionMessage()
	if msg == "" {
		t.Error("intervention message should not be empty")
	}
	if !contains(msg, "3 consecutive errors") {
		t.Errorf("message should contain error count, got: %s", msg)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || 
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

