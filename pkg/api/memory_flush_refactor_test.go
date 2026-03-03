package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/agentsdk-go/pkg/message"
	"github.com/riverfjs/agentsdk-go/pkg/model"
	"go.uber.org/zap"
)

type memoryFlushOKModel struct{}

func (m memoryFlushOKModel) Complete(context.Context, model.Request) (*model.Response, error) {
	return &model.Response{Message: model.Message{Role: "assistant", Content: "ok"}}, nil
}

func (m memoryFlushOKModel) CompleteStream(_ context.Context, req model.Request, cb model.StreamHandler) error {
	resp, err := m.Complete(context.Background(), req)
	if err != nil {
		return err
	}
	return cb(model.StreamResult{Final: true, Response: resp})
}

type memoryFlushFailModel struct{}

func (m memoryFlushFailModel) Complete(context.Context, model.Request) (*model.Response, error) {
	return nil, errors.New("model failed")
}

func (m memoryFlushFailModel) CompleteStream(context.Context, model.Request, model.StreamHandler) error {
	return errors.New("model failed")
}

func newMockRuntimeForMemoryFlush(opts Options) *Runtime {
	if opts.Logger == nil {
		opts.Logger = logger.NewZapLogger(zap.NewNop())
	}
	if opts.Model == nil {
		opts.Model = memoryFlushOKModel{}
	}
	rt := &Runtime{
		opts:      opts,
		mode:      ModeContext{EntryPoint: EntryPointCLI},
		histories: newHistoryStore(8),
		logger:    opts.Logger,
	}
	rt.sessionGate = newSessionGate()
	return rt
}

func TestShouldMemoryFlush_CompactionCycleGate(t *testing.T) {
	rt := &Runtime{
		opts: Options{
			ContextWindowTokens: 200000,
			MemoryFlush: MemoryFlushConfig{
				Enabled:             true,
				ReserveTokensFloor:  20000,
				SoftThresholdTokens: 4000,
			},
		},
	}
	sessionID := "s1"

	if !rt.shouldMemoryFlush(sessionID, nil, 176000) {
		t.Fatal("expected flush at threshold")
	}

	rt.sessionMemoryFlushAtCompact.Store(sessionID, int64(0))
	if rt.shouldMemoryFlush(sessionID, nil, 176000) {
		t.Fatal("expected skip when already flushed in current compaction cycle")
	}

	rt.sessionCompactionCount.Store(sessionID, int64(1))
	if !rt.shouldMemoryFlush(sessionID, nil, 176000) {
		t.Fatal("expected flush after compaction cycle advanced")
	}
}

func TestShouldMemoryFlush_UsesHistoryEstimateWhenEstimateMissing(t *testing.T) {
	rt := &Runtime{
		opts: Options{
			ContextWindowTokens: 100,
			MemoryFlush: MemoryFlushConfig{
				Enabled:             true,
				ReserveTokensFloor:  20,
				SoftThresholdTokens: 10,
			},
		},
	}
	h := message.NewHistory()
	h.Append(message.Message{Role: "user", Content: strings.Repeat("a", 280)}) // ~70 tokens

	if !rt.shouldMemoryFlush("s1", h, 0) {
		t.Fatal("expected flush based on history estimate")
	}
}

func TestRunMemoryFlushTurn_MarkOnSuccessOnly(t *testing.T) {
	okRT := newMockRuntimeForMemoryFlush(Options{
		Model:       memoryFlushOKModel{},
		MemoryFlush: MemoryFlushConfig{Enabled: true},
	})
	var okEvents []RealtimeEvent
	okRT.opts.RealtimeEventCallback = func(event RealtimeEvent) { okEvents = append(okEvents, event) }
	okRT.sessionCompactionCount.Store("ok", int64(2))
	okRT.runMemoryFlushTurn(context.Background(), preparedRun{
		preCompactTokens: 176000,
		history:          message.NewHistory(),
		normalized:       Request{SessionID: "ok"},
		mode:             okRT.mode,
	})
	if v, ok := okRT.sessionMemoryFlushAtCompact.Load("ok"); !ok || v.(int64) != 2 {
		t.Fatalf("expected flush mark at compaction=2, got %v (ok=%v)", v, ok)
	}
	if len(okEvents) < 2 || okEvents[0].Type != RealtimeEventMemoryFlushStart || okEvents[len(okEvents)-1].Type != RealtimeEventMemoryFlushDone {
		t.Fatalf("expected start/done events, got %+v", okEvents)
	}

	failRT := newMockRuntimeForMemoryFlush(Options{
		Model:       memoryFlushFailModel{},
		MemoryFlush: MemoryFlushConfig{Enabled: true},
	})
	var failEvents []RealtimeEvent
	failRT.opts.RealtimeEventCallback = func(event RealtimeEvent) { failEvents = append(failEvents, event) }
	failRT.sessionCompactionCount.Store("fail", int64(3))
	failRT.runMemoryFlushTurn(context.Background(), preparedRun{
		preCompactTokens: 176000,
		history:          message.NewHistory(),
		normalized:       Request{SessionID: "fail"},
		mode:             failRT.mode,
	})
	if _, ok := failRT.sessionMemoryFlushAtCompact.Load("fail"); ok {
		t.Fatal("flush mark should not be written when flush turn fails")
	}
	if len(failEvents) < 2 || failEvents[0].Type != RealtimeEventMemoryFlushStart || failEvents[len(failEvents)-1].Type != RealtimeEventMemoryFlushFailed {
		t.Fatalf("expected start/failed events, got %+v", failEvents)
	}
}

func TestPrepare_ContextWindowDoesNotTellUserReset(t *testing.T) {
	var events []RealtimeEvent
	rt := newMockRuntimeForMemoryFlush(Options{
		Model:                      memoryFlushOKModel{},
		ContextWindowTokens:        100,
		ContextWindowHardMinTokens: 20,
		RealtimeEventCallback:      func(event RealtimeEvent) { events = append(events, event) },
	})

	h := rt.histories.Get("s1")
	h.Append(message.Message{Role: "user", Content: strings.Repeat("x", 400)}) // ~100 tokens

	_, err := rt.prepare(context.Background(), Request{Prompt: "hello", SessionID: "s1"})
	if err != nil {
		t.Fatalf("prepare should not reject due to reset hinting: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected context pressure event")
	}
	for _, evt := range events {
		if strings.Contains(strings.ToLower(evt.Message), "reset") {
			t.Fatalf("context events should not mention reset: %+v", evt)
		}
	}
}

func TestRunStream_TriggersMemoryFlushWhenThresholdReached(t *testing.T) {
	rt := newMockRuntimeForMemoryFlush(Options{
		Model:               memoryFlushOKModel{},
		ContextWindowTokens: 100,
		MemoryFlush: MemoryFlushConfig{
			Enabled:             true,
			ReserveTokensFloor:  20,
			SoftThresholdTokens: 10,
		},
	})
	sessionID := "stream-flush"
	h := rt.histories.Get(sessionID)
	h.Append(message.Message{Role: "user", Content: strings.Repeat("a", 280)}) // ~70 tokens (threshold reached)

	stream, err := rt.RunStream(context.Background(), Request{Prompt: "hello", SessionID: sessionID})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range stream {
		// consume all events
	}

	if _, ok := rt.sessionMemoryFlushAtCompact.Load(sessionID); !ok {
		t.Fatal("expected memory flush mark for RunStream path")
	}
}
