package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riverfjs/agentsdk-go/pkg/message"
	"github.com/riverfjs/agentsdk-go/pkg/model"
)

func TestRuntime_ClearSession_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Create history directory and file
	historyDir := filepath.Join(tmpDir, ".claude", "history")
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}

	sessionID := "test-session"
	historyFile := filepath.Join(historyDir, sessionID+".json")
	if err := os.WriteFile(historyFile, []byte(`{"messages":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	// Create runtime
	rt, err := New(context.Background(), Options{
		ProjectRoot: tmpDir,
		ModelFactory: &model.OpenAIProvider{
			APIKey:    "test-key",
			ModelName: "gpt-4",
		},
	})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer rt.Close()

	// Add session to in-memory store
	history := rt.histories.Get(sessionID)
	history.Append(message.Message{Role: "user", Content: "test"})
	rt.tokens.Record(TokenStats{
		InputTokens:  5,
		OutputTokens: 2,
		TotalTokens:  7,
		SessionID:    sessionID,
		Timestamp:    time.Now().UTC(),
	})

	// Clear session
	if err := rt.ClearSession(sessionID); err != nil {
		t.Fatalf("ClearSession error: %v", err)
	}

	// Verify history file was deleted
	if _, err := os.Stat(historyFile); !os.IsNotExist(err) {
		t.Error("Expected history file to be deleted")
	}

	// Verify in-memory history was cleared
	rt.histories.mu.Lock()
	_, exists := rt.histories.data[sessionID]
	rt.histories.mu.Unlock()

	if exists {
		t.Error("Expected in-memory history to be cleared")
	}
	if got := rt.GetSessionStats(sessionID); got != nil {
		t.Errorf("Expected token stats to be cleared, got %+v", got)
	}
}

func TestRuntime_ClearSession_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()

	rt, err := New(context.Background(), Options{
		ProjectRoot: tmpDir,
		ModelFactory: &model.OpenAIProvider{
			APIKey:    "test-key",
			ModelName: "gpt-4",
		},
	})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer rt.Close()

	// Clear non-existent session (should not error)
	if err := rt.ClearSession("nonexistent"); err != nil {
		t.Errorf("Expected no error for non-existent session, got: %v", err)
	}
}

func TestRuntime_ClearSession_EmptyID(t *testing.T) {
	tmpDir := t.TempDir()

	rt, err := New(context.Background(), Options{
		ProjectRoot: tmpDir,
		ModelFactory: &model.OpenAIProvider{
			APIKey:    "test-key",
			ModelName: "gpt-4",
		},
	})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer rt.Close()

	// Empty session ID should error
	if err := rt.ClearSession(""); err == nil {
		t.Error("Expected error for empty session ID")
	}
}

func TestRuntime_ClearSession_NilRuntime(t *testing.T) {
	var rt *Runtime

	// Nil runtime should error
	if err := rt.ClearSession("test"); err == nil {
		t.Error("Expected error for nil runtime")
	}
}
