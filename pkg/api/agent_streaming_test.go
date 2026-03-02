package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/riverfjs/agentsdk-go/pkg/model"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

func TestRunStreamForwardsToolStreamSink(t *testing.T) {
	root := newClaudeProject(t)
	streamTool := &streamingStubTool{}
	mdl := &stubModel{responses: []*model.Response{
		{Message: model.Message{
			Role: "assistant",
			ToolCalls: []model.ToolCall{{
				ID:        "tool_1",
				Name:      streamTool.Name(),
				Arguments: map[string]any{"text": "hi"},
			}},
		}},
		{Message: model.Message{Role: "assistant", Content: "done"}},
	}}

	rt, err := New(context.Background(), Options{ProjectRoot: root, Model: mdl, Tools: []tool.Tool{streamTool}})
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	stream, err := rt.RunStream(context.Background(), Request{Prompt: "go"})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var chunks []string
	var stderrFlags []bool
	for evt := range stream {
		if evt.Type != EventToolExecutionOutput || evt.IsStderr == nil {
			continue
		}
		if evt.IsError != nil {
			t.Fatalf("tool output should not set IsError, got %+v", evt)
		}
		chunks = append(chunks, fmt.Sprint(evt.Output))
		stderrFlags = append(stderrFlags, *evt.IsStderr)
	}

	if streamTool.streamCalls != 1 {
		t.Fatalf("expected streaming path, got %d stream calls", streamTool.streamCalls)
	}
	if streamTool.execCalls != 0 {
		t.Fatalf("Execute should not run when stream sink present")
	}
	if len(chunks) != 2 || chunks[0] != "chunk-1" || chunks[1] != "chunk-err" {
		t.Fatalf("unexpected streamed chunks: %+v", chunks)
	}
	if len(stderrFlags) != 2 || stderrFlags[0] || !stderrFlags[1] {
		t.Fatalf("stderr flags mismatch: %+v", stderrFlags)
	}
}

func TestRunStreamEmitsFinalResponseEvent(t *testing.T) {
	root := newClaudeProject(t)
	mdl := &stubModel{responses: []*model.Response{
		{Message: model.Message{Role: "assistant", Content: "done"}},
	}}
	rt, err := New(context.Background(), Options{ProjectRoot: root, Model: mdl})
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	stream, err := rt.RunStream(context.Background(), Request{Prompt: "hello"})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var got *Response
	for evt := range stream {
		if evt.Type != EventFinalResponse {
			continue
		}
		resp, ok := evt.Output.(*Response)
		if !ok {
			t.Fatalf("expected *Response, got %T", evt.Output)
		}
		got = resp
	}
	if got == nil || got.Result == nil || got.Result.Output != "done" {
		t.Fatalf("unexpected final response: %#v", got)
	}
}

func TestRunStreamEmitsLiveTextDeltasBeforeFinal(t *testing.T) {
	root := newClaudeProject(t)
	mdl := &realtimeDeltaModel{}
	rt, err := New(context.Background(), Options{ProjectRoot: root, Model: mdl})
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	stream, err := rt.RunStream(context.Background(), Request{Prompt: "hello"})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var (
		seenDelta bool
		seenFinal bool
		sequence  []string
	)
	for evt := range stream {
		sequence = append(sequence, evt.Type)
		if evt.Type == EventContentBlockDelta && evt.Delta != nil && evt.Delta.Type == "text_delta" {
			seenDelta = true
		}
		if evt.Type == EventFinalResponse {
			seenFinal = true
			if !seenDelta {
				t.Fatalf("final arrived before live text delta, sequence=%v", sequence)
			}
		}
	}

	if !seenDelta {
		t.Fatalf("expected at least one live text delta, sequence=%v", sequence)
	}
	if !seenFinal {
		t.Fatalf("expected final response event, sequence=%v", sequence)
	}
}

type streamingStubTool struct {
	streamCalls int
	execCalls   int
}

func (s *streamingStubTool) Name() string             { return "stream" }
func (s *streamingStubTool) Description() string      { return "stream stub" }
func (s *streamingStubTool) Schema() *tool.JSONSchema { return nil }
func (s *streamingStubTool) Execute(context.Context, map[string]interface{}) (*tool.ToolResult, error) {
	s.execCalls++
	return &tool.ToolResult{Output: "exec"}, nil
}

func (s *streamingStubTool) StreamExecute(ctx context.Context, params map[string]interface{}, emit func(string, bool)) (*tool.ToolResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	s.streamCalls++
	if emit != nil {
		emit("chunk-1", false)
		emit("chunk-err", true)
	}
	return &tool.ToolResult{Success: true, Output: "chunk-1\nchunk-err", Data: params}, nil
}

type realtimeDeltaModel struct{}

func (m *realtimeDeltaModel) Complete(context.Context, model.Request) (*model.Response, error) {
	return &model.Response{Message: model.Message{Role: "assistant", Content: "hello"}}, nil
}

func (m *realtimeDeltaModel) CompleteStream(_ context.Context, _ model.Request, cb model.StreamHandler) error {
	if err := cb(model.StreamResult{Delta: "he"}); err != nil {
		return err
	}
	if err := cb(model.StreamResult{Delta: "llo"}); err != nil {
		return err
	}
	return cb(model.StreamResult{
		Final: true,
		Response: &model.Response{
			Message: model.Message{Role: "assistant", Content: "hello"},
		},
	})
}
