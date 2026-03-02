package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/riverfjs/agentsdk-go/pkg/model"
)

// TokenStats captures token usage for a single model call.
type TokenStats struct {
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	TotalTokens   int64     `json:"total_tokens"`
	CacheCreation int64     `json:"cache_creation_input_tokens,omitempty"`
	CacheRead     int64     `json:"cache_read_input_tokens,omitempty"`
	Model         string    `json:"model"`
	SessionID     string    `json:"session_id"`
	RequestID     string    `json:"request_id"`
	Timestamp     time.Time `json:"timestamp"`
}

// SessionTokenStats aggregates token usage across all requests in a session.
type SessionTokenStats struct {
	SessionID    string                 `json:"session_id"`
	TotalInput   int64                  `json:"total_input"`
	TotalOutput  int64                  `json:"total_output"`
	TotalTokens  int64                  `json:"total_tokens"`
	CacheCreated int64                  `json:"cache_created,omitempty"`
	CacheRead    int64                  `json:"cache_read,omitempty"`
	ByModel      map[string]*ModelStats `json:"by_model,omitempty"`
	RequestCount int                    `json:"request_count"`
	FirstRequest time.Time              `json:"first_request"`
	LastRequest  time.Time              `json:"last_request"`
}

// ModelStats aggregates token usage for a specific model.
type ModelStats struct {
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	TotalTokens   int64 `json:"total_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheRead     int64 `json:"cache_read_input_tokens,omitempty"`
	RequestCount  int   `json:"request_count"`
}

// TokenCallback is called synchronously after token usage is recorded.
// The callback should be lightweight and non-blocking to avoid delaying
// the agent execution. If you need async processing, spawn a goroutine
// inside the callback.
type TokenCallback func(stats TokenStats)

// tokenTracker maintains thread-safe token statistics across sessions.
type tokenTracker struct {
	mu       sync.RWMutex
	sessions map[string]*SessionTokenStats
	total    *SessionTokenStats
	callback TokenCallback
	enabled  bool
	storePath string
}

type tokenTrackerSnapshot struct {
	Sessions map[string]*SessionTokenStats `json:"sessions"`
	Total    *SessionTokenStats            `json:"total"`
}

func newTokenTracker(enabled bool, callback TokenCallback, storePath ...string) *tokenTracker {
	path := ""
	if len(storePath) > 0 {
		path = strings.TrimSpace(storePath[0])
	}
	tr := &tokenTracker{
		sessions: make(map[string]*SessionTokenStats),
		total: &SessionTokenStats{
			SessionID: "_total",
			ByModel:   make(map[string]*ModelStats),
		},
		callback:  callback,
		enabled:   enabled,
		storePath: path,
	}
	if tr.enabled && tr.storePath != "" {
		tr.loadFromDisk()
	}
	return tr
}

// Record adds a token usage record. Thread-safe.
func (t *tokenTracker) Record(stats TokenStats) {
	if t == nil || !t.enabled {
		return
	}

	var cb TokenCallback
	t.mu.Lock()

	// Update session stats
	session, ok := t.sessions[stats.SessionID]
	if !ok {
		session = &SessionTokenStats{
			SessionID:    stats.SessionID,
			ByModel:      make(map[string]*ModelStats),
			FirstRequest: stats.Timestamp,
		}
		t.sessions[stats.SessionID] = session
	}

	session.TotalInput += stats.InputTokens
	session.TotalOutput += stats.OutputTokens
	session.TotalTokens += stats.TotalTokens
	session.CacheCreated += stats.CacheCreation
	session.CacheRead += stats.CacheRead
	session.RequestCount++
	session.LastRequest = stats.Timestamp

	// Update per-model stats for session
	if stats.Model != "" {
		modelStats, ok := session.ByModel[stats.Model]
		if !ok {
			modelStats = &ModelStats{}
			session.ByModel[stats.Model] = modelStats
		}
		modelStats.InputTokens += stats.InputTokens
		modelStats.OutputTokens += stats.OutputTokens
		modelStats.TotalTokens += stats.TotalTokens
		modelStats.CacheCreation += stats.CacheCreation
		modelStats.CacheRead += stats.CacheRead
		modelStats.RequestCount++
	}

	// Update global total
	t.total.TotalInput += stats.InputTokens
	t.total.TotalOutput += stats.OutputTokens
	t.total.TotalTokens += stats.TotalTokens
	t.total.CacheCreated += stats.CacheCreation
	t.total.CacheRead += stats.CacheRead
	t.total.RequestCount++
	if t.total.FirstRequest.IsZero() {
		t.total.FirstRequest = stats.Timestamp
	}
	t.total.LastRequest = stats.Timestamp

	// Update per-model stats for total
	if stats.Model != "" {
		modelStats, ok := t.total.ByModel[stats.Model]
		if !ok {
			modelStats = &ModelStats{}
			t.total.ByModel[stats.Model] = modelStats
		}
		modelStats.InputTokens += stats.InputTokens
		modelStats.OutputTokens += stats.OutputTokens
		modelStats.TotalTokens += stats.TotalTokens
		modelStats.CacheCreation += stats.CacheCreation
		modelStats.CacheRead += stats.CacheRead
		modelStats.RequestCount++
	}

	cb = t.callback
	t.persistLocked()
	t.mu.Unlock()

	if cb != nil {
		cb(stats)
	}
}

// GetSessionStats returns stats for a specific session. Thread-safe.
func (t *tokenTracker) GetSessionStats(sessionID string) *SessionTokenStats {
	if t == nil {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	session, ok := t.sessions[sessionID]
	if !ok {
		return nil
	}

	// Return a copy
	return copySessionStats(session)
}

// GetTotalStats returns aggregated stats across all sessions. Thread-safe.
func (t *tokenTracker) GetTotalStats() *SessionTokenStats {
	if t == nil {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	return copySessionStats(t.total)
}

// IsEnabled returns whether tracking is active.
func (t *tokenTracker) IsEnabled() bool {
	if t == nil {
		return false
	}
	return t.enabled
}

// ResetSession removes aggregated token stats for one session and adjusts total.
func (t *tokenTracker) ResetSession(sessionID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	session, ok := t.sessions[sessionID]
	if !ok || session == nil {
		return
	}
	delete(t.sessions, sessionID)

	t.total.TotalInput -= session.TotalInput
	t.total.TotalOutput -= session.TotalOutput
	t.total.TotalTokens -= session.TotalTokens
	t.total.CacheCreated -= session.CacheCreated
	t.total.CacheRead -= session.CacheRead
	t.total.RequestCount -= session.RequestCount
	if t.total.TotalInput < 0 {
		t.total.TotalInput = 0
	}
	if t.total.TotalOutput < 0 {
		t.total.TotalOutput = 0
	}
	if t.total.TotalTokens < 0 {
		t.total.TotalTokens = 0
	}
	if t.total.CacheCreated < 0 {
		t.total.CacheCreated = 0
	}
	if t.total.CacheRead < 0 {
		t.total.CacheRead = 0
	}
	if t.total.RequestCount < 0 {
		t.total.RequestCount = 0
	}

	if len(session.ByModel) > 0 {
		for name, sessModel := range session.ByModel {
			totalModel, ok := t.total.ByModel[name]
			if !ok || totalModel == nil {
				continue
			}
			totalModel.InputTokens -= sessModel.InputTokens
			totalModel.OutputTokens -= sessModel.OutputTokens
			totalModel.TotalTokens -= sessModel.TotalTokens
			totalModel.CacheCreation -= sessModel.CacheCreation
			totalModel.CacheRead -= sessModel.CacheRead
			totalModel.RequestCount -= sessModel.RequestCount
			if totalModel.InputTokens < 0 {
				totalModel.InputTokens = 0
			}
			if totalModel.OutputTokens < 0 {
				totalModel.OutputTokens = 0
			}
			if totalModel.TotalTokens < 0 {
				totalModel.TotalTokens = 0
			}
			if totalModel.CacheCreation < 0 {
				totalModel.CacheCreation = 0
			}
			if totalModel.CacheRead < 0 {
				totalModel.CacheRead = 0
			}
			if totalModel.RequestCount < 0 {
				totalModel.RequestCount = 0
			}
			if totalModel.InputTokens == 0 && totalModel.OutputTokens == 0 && totalModel.TotalTokens == 0 && totalModel.RequestCount == 0 {
				delete(t.total.ByModel, name)
			}
		}
	}

	if t.total.RequestCount == 0 {
		t.total.FirstRequest = time.Time{}
		t.total.LastRequest = time.Time{}
	}
	t.persistLocked()
}

func (t *tokenTracker) loadFromDisk() {
	data, err := os.ReadFile(t.storePath)
	if err != nil || len(data) == 0 {
		return
	}
	var snap tokenTrackerSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(snap.Sessions) > 0 {
		t.sessions = snap.Sessions
		for _, s := range t.sessions {
			if s != nil && s.ByModel == nil {
				s.ByModel = make(map[string]*ModelStats)
			}
		}
	}
	if snap.Total != nil {
		t.total = snap.Total
		if t.total.ByModel == nil {
			t.total.ByModel = make(map[string]*ModelStats)
		}
	}
}

func (t *tokenTracker) persistLocked() {
	if t == nil || !t.enabled || t.storePath == "" {
		return
	}
	dir := filepath.Dir(t.storePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	snap := tokenTrackerSnapshot{
		Sessions: make(map[string]*SessionTokenStats, len(t.sessions)),
		Total:    copySessionStats(t.total),
	}
	for k, v := range t.sessions {
		snap.Sessions[k] = copySessionStats(v)
	}
	if snap.Total == nil {
		snap.Total = &SessionTokenStats{SessionID: "_total", ByModel: make(map[string]*ModelStats)}
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := t.storePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, t.storePath)
}

func copySessionStats(s *SessionTokenStats) *SessionTokenStats {
	if s == nil {
		return nil
	}
	cp := &SessionTokenStats{
		SessionID:    s.SessionID,
		TotalInput:   s.TotalInput,
		TotalOutput:  s.TotalOutput,
		TotalTokens:  s.TotalTokens,
		CacheCreated: s.CacheCreated,
		CacheRead:    s.CacheRead,
		RequestCount: s.RequestCount,
		FirstRequest: s.FirstRequest,
		LastRequest:  s.LastRequest,
	}
	if len(s.ByModel) > 0 {
		cp.ByModel = make(map[string]*ModelStats, len(s.ByModel))
		for k, v := range s.ByModel {
			cp.ByModel[k] = &ModelStats{
				InputTokens:   v.InputTokens,
				OutputTokens:  v.OutputTokens,
				TotalTokens:   v.TotalTokens,
				CacheCreation: v.CacheCreation,
				CacheRead:     v.CacheRead,
				RequestCount:  v.RequestCount,
			}
		}
	}
	return cp
}

// tokenStatsFromUsage converts model.Usage to TokenStats.
func tokenStatsFromUsage(usage model.Usage, modelName, sessionID, requestID string) TokenStats {
	return TokenStats{
		InputTokens:   int64(usage.InputTokens),
		OutputTokens:  int64(usage.OutputTokens),
		TotalTokens:   int64(usage.TotalTokens),
		CacheCreation: int64(usage.CacheCreationTokens),
		CacheRead:     int64(usage.CacheReadTokens),
		Model:         modelName,
		SessionID:     sessionID,
		RequestID:     requestID,
		Timestamp:     time.Now().UTC(),
	}
}
