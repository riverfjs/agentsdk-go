package model

import (
	"context"
	"errors"
	"testing"
)

type fallbackStubModel struct {
	requests        []Request
	completeErrs    map[string]error
	streamErrs      map[string]error
	streamDelta     map[string]string
	responseByModel map[string]*Response
}

func (m *fallbackStubModel) Complete(_ context.Context, req Request) (*Response, error) {
	m.requests = append(m.requests, req)
	if err := m.completeErrs[req.Model]; err != nil {
		return nil, err
	}
	if resp := m.responseByModel[req.Model]; resp != nil {
		return resp, nil
	}
	return &Response{Message: Message{Role: "assistant", Content: "ok"}}, nil
}

func (m *fallbackStubModel) CompleteStream(_ context.Context, req Request, cb StreamHandler) error {
	m.requests = append(m.requests, req)
	if delta := m.streamDelta[req.Model]; delta != "" {
		if err := cb(StreamResult{Delta: delta}); err != nil {
			return err
		}
	}
	if err := m.streamErrs[req.Model]; err != nil {
		return err
	}
	resp := m.responseByModel[req.Model]
	if resp == nil {
		resp = &Response{Message: Message{Role: "assistant", Content: "ok"}}
	}
	return cb(StreamResult{Final: true, Response: resp})
}

func TestWrapWithFallback_Complete_PrimaryFailFallbackSuccess(t *testing.T) {
	base := &fallbackStubModel{
		completeErrs: map[string]error{
			"": errors.New("primary down"),
		},
		responseByModel: map[string]*Response{
			"fallback-a": {Message: Message{Role: "assistant", Content: "ok-fallback"}},
		},
	}
	wrapped := WrapWithFallback(base, []string{"fallback-a"})
	resp, err := wrapped.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("complete err: %v", err)
	}
	if resp == nil || resp.Message.Content != "ok-fallback" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(base.requests) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(base.requests))
	}
}

func TestWrapWithFallback_CompleteStream_PrimaryStreamsThenFails_NoFallback(t *testing.T) {
	base := &fallbackStubModel{
		streamDelta: map[string]string{
			"": "partial",
		},
		streamErrs: map[string]error{
			"": errors.New("stream failed"),
		},
		responseByModel: map[string]*Response{
			"fallback-a": {Message: Message{Role: "assistant", Content: "should-not-use"}},
		},
	}
	wrapped := WrapWithFallback(base, []string{"fallback-a"})
	if err := wrapped.CompleteStream(context.Background(), Request{}, func(StreamResult) error { return nil }); err == nil {
		t.Fatal("expected stream error")
	}
	if len(base.requests) != 1 {
		t.Fatalf("expected no fallback attempt after visible stream, got %d", len(base.requests))
	}
}

func TestWrapWithFallbackWithOptions_EmitsSwitchEvent(t *testing.T) {
	base := &fallbackStubModel{
		completeErrs: map[string]error{
			"": errors.New("primary down"),
		},
		responseByModel: map[string]*Response{
			"fallback-a": {Message: Message{Role: "assistant", Content: "ok-fallback"}},
		},
	}
	var observed []FallbackSwitchEvent
	wrapped := WrapWithFallbackWithOptions(base, []string{"fallback-a"}, FallbackOptions{
		PrimaryModel: "anthropic/claude-opus-4.6",
		OnSwitch: func(evt FallbackSwitchEvent) {
			observed = append(observed, evt)
		},
	})

	if _, err := wrapped.Complete(context.Background(), Request{}); err != nil {
		t.Fatalf("complete err: %v", err)
	}
	if len(observed) != 1 {
		t.Fatalf("expected 1 switch event, got %d", len(observed))
	}
	if observed[0].FromModel != "anthropic/claude-opus-4.6" || observed[0].ToModel != "fallback-a" {
		t.Fatalf("unexpected switch event: %+v", observed[0])
	}
	if observed[0].LastError == nil || observed[0].LastError.Error() != "primary down" {
		t.Fatalf("unexpected last error: %+v", observed[0].LastError)
	}
}

