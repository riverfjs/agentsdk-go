package model

import (
	"context"
	"errors"
	"strings"
)

// FallbackSwitchEvent records a model switch during fallback retry.
type FallbackSwitchEvent struct {
	FromModel string
	ToModel   string
	LastError error
	Stream    bool
}

// FallbackOptions controls optional fallback wrapper behavior.
type FallbackOptions struct {
	// PrimaryModel is used as display/source model when Request.Model is empty.
	PrimaryModel string
	// OnSwitch is called before each fallback switch attempt.
	OnSwitch func(FallbackSwitchEvent)
}

// WrapWithFallback returns a model wrapper that retries with fallback model IDs.
// The primary call uses the incoming request model unchanged; fallbacks override
// Request.Model in order. Empty/duplicate fallback entries are ignored.
func WrapWithFallback(base Model, fallbacks []string) Model {
	return WrapWithFallbackWithOptions(base, fallbacks, FallbackOptions{})
}

// WrapWithFallbackWithOptions behaves like WrapWithFallback and accepts observer
// hooks for switch notifications.
func WrapWithFallbackWithOptions(base Model, fallbacks []string, opts FallbackOptions) Model {
	if base == nil || len(fallbacks) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(fallbacks))
	normalized := make([]string, 0, len(fallbacks))
	for _, candidate := range fallbacks {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return base
	}
	return &fallbackModel{
		base:         base,
		fallbacks:    normalized,
		primaryModel: strings.TrimSpace(opts.PrimaryModel),
		onSwitch:     opts.OnSwitch,
	}
}

type fallbackModel struct {
	base         Model
	fallbacks    []string
	primaryModel string
	onSwitch     func(FallbackSwitchEvent)
	lastUsed     string
}

func (m *fallbackModel) Complete(ctx context.Context, req Request) (*Response, error) {
	resp, err := m.base.Complete(ctx, req)
	if err == nil {
		m.lastUsed = m.currentModel(req.Model)
		return resp, nil
	}
	if !isFallbackRetryableError(err) {
		return nil, err
	}
	lastErr := err
	current := m.currentModel(req.Model)
	for _, fb := range m.fallbackCandidates(req.Model) {
		m.emitSwitch(current, fb, lastErr, false)
		nextReq := req
		nextReq.Model = fb
		resp, err = m.base.Complete(ctx, nextReq)
		if err == nil {
			m.lastUsed = fb
			return resp, nil
		}
		lastErr = err
		if !isFallbackRetryableError(err) {
			return nil, err
		}
		current = fb
	}
	return nil, lastErr
}

func (m *fallbackModel) CompleteStream(ctx context.Context, req Request, cb StreamHandler) error {
	hadVisible := false
	err := m.base.CompleteStream(ctx, req, func(sr StreamResult) error {
		if sr.Delta != "" || sr.ToolCall != nil {
			hadVisible = true
		}
		if cb == nil {
			return nil
		}
		return cb(sr)
	})
	if err == nil {
		m.lastUsed = m.currentModel(req.Model)
		return nil
	}
	if hadVisible || !isFallbackRetryableError(err) {
		return err
	}

	lastErr := err
	current := m.currentModel(req.Model)
	for _, fb := range m.fallbackCandidates(req.Model) {
		m.emitSwitch(current, fb, lastErr, true)
		nextReq := req
		nextReq.Model = fb
		hadVisible = false
		err = m.base.CompleteStream(ctx, nextReq, func(sr StreamResult) error {
			if sr.Delta != "" || sr.ToolCall != nil {
				hadVisible = true
			}
			if cb == nil {
				return nil
			}
			return cb(sr)
		})
		if err == nil {
			m.lastUsed = fb
			return nil
		}
		lastErr = err
		if hadVisible || !isFallbackRetryableError(err) {
			return err
		}
		current = fb
	}
	return lastErr
}

// LastUsedModel returns the model ID used by the latest successful call.
func (m *fallbackModel) LastUsedModel() string {
	return strings.TrimSpace(m.lastUsed)
}

func (m *fallbackModel) fallbackCandidates(current string) []string {
	cur := strings.TrimSpace(current)
	out := make([]string, 0, len(m.fallbacks))
	for _, fb := range m.fallbacks {
		if fb == cur {
			continue
		}
		out = append(out, fb)
	}
	return out
}

func (m *fallbackModel) currentModel(requestModel string) string {
	cur := strings.TrimSpace(requestModel)
	if cur != "" {
		return cur
	}
	return m.primaryModel
}

func (m *fallbackModel) emitSwitch(from, to string, lastErr error, stream bool) {
	if m.onSwitch == nil {
		return
	}
	m.onSwitch(FallbackSwitchEvent{
		FromModel: strings.TrimSpace(from),
		ToModel:   strings.TrimSpace(to),
		LastError: lastErr,
		Stream:    stream,
	})
}

func isFallbackRetryableError(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}
