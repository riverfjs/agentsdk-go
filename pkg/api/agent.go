package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/riverfjs/agentsdk-go/pkg/agent"
	"github.com/riverfjs/agentsdk-go/pkg/config"
	coreevents "github.com/riverfjs/agentsdk-go/pkg/core/events"
	corehooks "github.com/riverfjs/agentsdk-go/pkg/core/hooks"
	"github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/agentsdk-go/pkg/message"
	"github.com/riverfjs/agentsdk-go/pkg/middleware"
	"github.com/riverfjs/agentsdk-go/pkg/model"
	"github.com/riverfjs/agentsdk-go/pkg/runtime/commands"
	"github.com/riverfjs/agentsdk-go/pkg/runtime/skills"
	"github.com/riverfjs/agentsdk-go/pkg/runtime/subagents"
	"github.com/riverfjs/agentsdk-go/pkg/runtime/tasks"
	"github.com/riverfjs/agentsdk-go/pkg/sandbox"
	"github.com/riverfjs/agentsdk-go/pkg/security"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
	toolbuiltin "github.com/riverfjs/agentsdk-go/pkg/tool/builtin"
)

type streamContextKey string

const streamEmitCtxKey streamContextKey = "agentsdk.stream.emit"

func withStreamEmit(ctx context.Context, emit streamEmitFunc) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, streamEmitCtxKey, emit)
}

func streamEmitFromContext(ctx context.Context) streamEmitFunc {
	if ctx == nil {
		return nil
	}
	if emit, ok := ctx.Value(streamEmitCtxKey).(streamEmitFunc); ok {
		return emit
	}
	return nil
}

// Runtime exposes the unified SDK surface that powers CLI/CI/enterprise entrypoints.
type Runtime struct {
	opts Options
	mode ModeContext
	// guardBaseSystemPrompt keeps the original caller-provided system prompt
	// (for aevitas: AGENTS.md + SOUL.md) before runtime context injections.
	guardBaseSystemPrompt string
	settings              *config.Settings
	cfg                   *config.Settings
	fs                    *config.FS
	rulesLoader           *config.RulesLoader
	sandbox               *sandbox.Manager
	sbRoot                string
	registry              *tool.Registry
	executor              *tool.Executor
	// recorder is retained for backward compatibility.
	// Deprecated: hook events are now recorded per-request via preparedRun.recorder.
	recorder         HookRecorder
	hooks            *corehooks.Executor
	histories        *historyStore
	historyPersister *diskHistoryPersister
	sessionGate      *sessionGate

	cmdExec   *commands.Executor
	skReg     *skills.Registry
	subMgr    *subagents.Manager
	tokens    *tokenTracker
	compactor *compactor
	tracer    Tracer
	logger    logger.Logger

	// per-session memory flush / compaction tracking
	sessionCompactionCount      sync.Map // sessionID -> int64
	sessionMemoryFlushAtCompact sync.Map // sessionID -> int64
	sessionCurrentModel         sync.Map // sessionID -> string

	mu sync.RWMutex

	runMu     sync.Mutex
	runWG     sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
	closed    bool
}

// New instantiates a unified runtime bound to the provided options.
func New(ctx context.Context, opts Options) (*Runtime, error) {
	opts = opts.withDefaults()
	opts = opts.frozen()
	mode := opts.modeContext()
	guardBaseSystemPrompt := strings.TrimSpace(opts.SystemPrompt)

	// 初始化 logger，如果未提供则使用默认
	log := opts.Logger
	if log == nil {
		log = logger.NewDefault()
	}

	// 初始化文件系统抽象层
	fsLayer := config.NewFS(opts.ProjectRoot, opts.EmbedFS)
	opts.fsLayer = fsLayer

	if err := materializeEmbeddedClaudeHooks(opts.ProjectRoot, opts.EmbedFS); err != nil {
		log.Warnf("claude hooks materializer warning: %v", err)
	}

	if memory, err := config.LoadClaudeMD(opts.ProjectRoot, fsLayer); err != nil {
		log.Warnf("claude.md loader warning: %v", err)
	} else if strings.TrimSpace(memory) != "" {
		if strings.TrimSpace(opts.SystemPrompt) == "" {
			opts.SystemPrompt = fmt.Sprintf("## Memory\n\n%s", strings.TrimSpace(memory))
		} else {
			opts.SystemPrompt = fmt.Sprintf("%s\n\n## Memory\n\n%s", strings.TrimSpace(opts.SystemPrompt), strings.TrimSpace(memory))
		}
	}

	settings, err := loadSettings(opts)
	if err != nil {
		return nil, err
	}

	mdl, err := resolveModel(ctx, opts)
	if err != nil {
		return nil, err
	}
	opts.Model = mdl

	sbox, sbRoot := buildSandboxManager(opts, settings)
	cmdExec, cmdErrs := buildCommandsExecutor(opts)
	if len(cmdErrs) > 0 {
		for _, err := range cmdErrs {
			log.Warnf("command loader warning: %v", err)
		}
	}
	skReg, skErrs := buildSkillsRegistry(opts)
	if len(skErrs) > 0 {
		for _, err := range skErrs {
			log.Warnf("skill loader warning: %v", err)
		}
	}
	subMgr, subErrs := buildSubagentsManager(opts)
	if len(subErrs) > 0 {
		for _, err := range subErrs {
			log.Warnf("subagent loader warning: %v", err)
		}
	}
	registry := tool.NewRegistry()
	taskTool, err := registerTools(registry, opts, settings, skReg, cmdExec, log)
	if err != nil {
		return nil, err
	}
	mcpServers := collectMCPServers(settings, opts.MCPServers)
	if err := registerMCPServers(ctx, registry, sbox, mcpServers); err != nil {
		return nil, err
	}
	if err := validatePermissionDSLToolsAgainstRegistry(settings, registry); err != nil {
		return nil, err
	}
	executor := tool.NewExecutor(registry, sbox).
		WithOutputPersister(tool.NewOutputPersister()).
		WithLogger(log)

	recorder := defaultHookRecorder()
	hooks := newHookExecutor(opts, recorder, settings)
	compactor := newCompactor(opts.ProjectRoot, opts.AutoCompact, opts.Model, opts.TokenLimit, opts.PrimaryFallbackModels, hooks, log)

	// Initialize tracer (noop without 'otel' build tag)
	tracer, err := NewTracer(opts.OTEL)
	if err != nil {
		return nil, fmt.Errorf("otel tracer init: %w", err)
	}

	var rulesLoader *config.RulesLoader
	if opts.RulesEnabled == nil || (opts.RulesEnabled != nil && *opts.RulesEnabled) {
		rulesLoader = config.NewRulesLoader(opts.ProjectRoot)
		if _, err := rulesLoader.LoadRules(); err != nil {
			log.Warnf("rules loader warning: %v", err)
		}
		if err := rulesLoader.WatchChanges(nil); err != nil {
			log.Warnf("rules watcher warning: %v", err)
		}
	}

	histories := newHistoryStore(opts.MaxSessions)
	histories.historyLimit = opts.HistoryLimit
	var historyPersister *diskHistoryPersister
	retainDays := 0
	if settings != nil && settings.CleanupPeriodDays != nil {
		retainDays = *settings.CleanupPeriodDays
	}
	if retainDays > 0 {
		historyPersister = newDiskHistoryPersister(opts.ProjectRoot)
		if historyPersister != nil {
			histories.loader = historyPersister.Load
			if err := historyPersister.Cleanup(retainDays); err != nil {
				log.Warnf("history cleanup warning: %v", err)
			}
		}
	}

	rt := &Runtime{
		opts:                  opts,
		mode:                  mode,
		guardBaseSystemPrompt: guardBaseSystemPrompt,
		settings:              settings,
		cfg:                   projectConfigFromSettings(settings),
		fs:                    fsLayer,
		rulesLoader:           rulesLoader,
		sandbox:               sbox,
		sbRoot:                sbRoot,
		registry:              registry,
		executor:              executor,
		recorder:              recorder,
		hooks:                 hooks,
		histories:             histories,
		historyPersister:      historyPersister,
		cmdExec:               cmdExec,
		skReg:                 skReg,
		subMgr:                subMgr,
		tokens:                newTokenTracker(opts.TokenTracking, opts.TokenCallback, tokenTrackerStorePath(opts.ProjectRoot)),
		compactor:             compactor,
		tracer:                tracer,
		logger:                log,
	}
	rt.sessionGate = newSessionGate()
	if rt.compactor != nil {
		rt.compactor.onCompacted = func(sessionID string) {
			if strings.TrimSpace(sessionID) == "" {
				return
			}
			next := rt.sessionCompactionValue(sessionID) + 1
			rt.sessionCompactionCount.Store(sessionID, next)
		}
	}

	if taskTool != nil {
		taskTool.SetRunner(rt.taskRunner())
	}
	return rt, nil
}

func (rt *Runtime) beginRun() error {
	rt.runMu.Lock()
	defer rt.runMu.Unlock()
	if rt.closed {
		return ErrRuntimeClosed
	}
	rt.runWG.Add(1)
	return nil
}

func (rt *Runtime) endRun() {
	rt.runWG.Done()
}

// Run executes the unified pipeline synchronously.
func (rt *Runtime) Run(ctx context.Context, req Request) (*Response, error) {
	if rt == nil {
		return nil, ErrRuntimeClosed
	}
	if err := rt.beginRun(); err != nil {
		return nil, err
	}
	defer rt.endRun()

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = defaultSessionID(rt.mode.EntryPoint)
	}
	req.SessionID = sessionID

	rt.logger.Infof("[agentsdk] Run: session=%s, prompt=%s", sessionID, truncatePrompt(req.Prompt, 80))
	if rt.promptGuardEnabled() && detectPromptDisclosureRequest(req.Prompt) {
		rt.logger.Warnf("[agentsdk] input_guard_blocked: session=%s request=%s prompt=%q", sessionID, strings.TrimSpace(req.RequestID), req.Prompt)
		return nil, &promptPolicyViolationError{message: policyRefusalMessage()}
	}

	if err := rt.sessionGate.Acquire(ctx, sessionID); err != nil {
		rt.logger.Errorf("[agentsdk] Failed to acquire session gate: %v", err)
		return nil, ErrConcurrentExecution
	}
	defer rt.sessionGate.Release(sessionID)

	prep, err := rt.prepare(ctx, req)
	if err != nil {
		rt.logger.Errorf("[agentsdk] Failed to prepare: %v", err)
		return nil, err
	}
	defer rt.persistHistory(prep.normalized.SessionID, prep.history)

	result, err := rt.runAgent(prep)
	if err != nil {
		rt.logger.Errorf("[agentsdk] Agent failed: %v", err)
		return nil, err
	}
	if modelID := strings.TrimSpace(result.model); modelID != "" {
		rt.sessionCurrentModel.Store(prep.normalized.SessionID, modelID)
	}

	outputLen := 0
	if result.output != nil && result.output.Content != "" {
		outputLen = len(result.output.Content)
	}
	if outputLen > 0 {
		rt.logger.Infof("[agentsdk] Completed: %d chars", outputLen)
	}
	if result.usage.TotalTokens > 0 || result.usage.InputTokens > 0 || result.usage.OutputTokens > 0 {
		rt.logger.Infof("[agentsdk] Token usage: in=%d out=%d total=%d cache_create=%d cache_read=%d",
			result.usage.InputTokens,
			result.usage.OutputTokens,
			result.usage.TotalTokens,
			result.usage.CacheCreationTokens,
			result.usage.CacheReadTokens,
		)
	}
	resp := rt.buildResponse(prep, result)
	rt.appendTTSAudioEvent(ctx, prep, resp)
	return resp, nil
}

func truncatePrompt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func tokenTrackerStorePath(projectRoot string) string {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".claude", "token_stats.json")
}

func mergeUsage(a, b model.Usage) model.Usage {
	merged := model.Usage{
		InputTokens:         a.InputTokens + b.InputTokens,
		OutputTokens:        a.OutputTokens + b.OutputTokens,
		TotalTokens:         a.TotalTokens + b.TotalTokens,
		CacheReadTokens:     a.CacheReadTokens + b.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens + b.CacheCreationTokens,
	}
	if merged.TotalTokens == 0 {
		merged.TotalTokens = merged.InputTokens + merged.OutputTokens
	}
	return merged
}

// RunStream executes the pipeline asynchronously and returns events over a channel.
func (rt *Runtime) RunStream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	if rt == nil {
		return nil, ErrRuntimeClosed
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.Attachments) == 0 {
		return nil, errors.New("api: prompt is empty")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = defaultSessionID(rt.mode.EntryPoint)
	}
	req.SessionID = sessionID
	if rt.promptGuardEnabled() && detectPromptDisclosureRequest(req.Prompt) {
		rt.logger.Warnf("[agentsdk] input_guard_blocked: session=%s request=%s prompt=%q", sessionID, strings.TrimSpace(req.RequestID), req.Prompt)
		return nil, &promptPolicyViolationError{message: policyRefusalMessage()}
	}

	if err := rt.beginRun(); err != nil {
		return nil, err
	}

	// 缓冲区增大以吸收前端延迟（逐字符渲染等）导致的背压，避免 progress emit 阻塞工具执行
	out := make(chan StreamEvent, 512)
	progressChan := make(chan StreamEvent, 256)
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	progressMW := newProgressMiddleware(progressChan)
	ctxWithEmit := withStreamEmit(baseCtx, progressMW.streamEmit())
	go func() {
		defer rt.endRun()
		defer close(out)
		if err := rt.sessionGate.Acquire(ctxWithEmit, sessionID); err != nil {
			isErr := true
			out <- StreamEvent{Type: EventError, Output: ErrConcurrentExecution.Error(), IsError: &isErr}
			return
		}
		defer rt.sessionGate.Release(sessionID)

		prep, err := rt.prepare(ctxWithEmit, req)
		if err != nil {
			isErr := true
			out <- StreamEvent{Type: EventError, Output: err.Error(), IsError: &isErr}
			return
		}
		defer rt.persistHistory(prep.normalized.SessionID, prep.history)

		done := make(chan struct{})
		go func() {
			defer close(done)
			dropping := false
			for event := range progressChan {
				if dropping {
					continue
				}
				select {
				case out <- event:
				case <-ctxWithEmit.Done():
					dropping = true
				}
			}
		}()

		var runErr error
		var result runResult
		defer func() {
			if rt.hooks != nil {
				reason := "completed"
				if runErr != nil {
					reason = "error"
				}
				//nolint:errcheck // session end events are non-critical notifications
				rt.hooks.Publish(coreevents.Event{
					Type:      coreevents.SessionEnd,
					SessionID: req.SessionID,
					Payload:   coreevents.SessionEndPayload{SessionID: req.SessionID, Reason: reason},
				})
			}
		}()

		result, runErr = rt.runAgentWithMiddleware(prep, progressMW)
		close(progressChan)
		<-done

		if runErr != nil {
			isErr := true
			out <- StreamEvent{Type: EventError, Output: runErr.Error(), IsError: &isErr}
			return
		}
		if modelID := strings.TrimSpace(result.model); modelID != "" {
			rt.sessionCurrentModel.Store(prep.normalized.SessionID, modelID)
		}
		if result.usage.TotalTokens > 0 || result.usage.InputTokens > 0 || result.usage.OutputTokens > 0 {
			rt.logger.Infof("[agentsdk] Token usage: in=%d out=%d total=%d cache_create=%d cache_read=%d",
				result.usage.InputTokens,
				result.usage.OutputTokens,
				result.usage.TotalTokens,
				result.usage.CacheCreationTokens,
				result.usage.CacheReadTokens,
			)
		}
		resp := rt.buildResponse(prep, result)
		rt.appendTTSAudioEvent(ctxWithEmit, prep, resp)
		out <- StreamEvent{Type: EventFinalResponse, Output: resp}
	}()
	return out, nil
}

// Close releases held resources.
func (rt *Runtime) Close() error {
	if rt == nil {
		return nil
	}
	rt.closeOnce.Do(func() {
		rt.runMu.Lock()
		rt.closed = true
		rt.runMu.Unlock()

		rt.runWG.Wait()

		var err error
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		shutdownErr := toolbuiltin.DefaultAsyncTaskManager().Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			err = errors.Join(err, shutdownErr)
		}
		if shutdownErr == nil && rt.histories != nil {
			for _, sessionID := range rt.histories.SessionIDs() {
				if cleanupErr := cleanupBashOutputSessionDir(sessionID); cleanupErr != nil {
					rt.logger.Warnf("api: session %q temp cleanup failed: %v", sessionID, cleanupErr)
				}
				if cleanupErr := cleanupToolOutputSessionDir(sessionID); cleanupErr != nil {
					rt.logger.Warnf("api: session %q tool output cleanup failed: %v", sessionID, cleanupErr)
				}
			}
		}
		if rt.rulesLoader != nil {
			if e := rt.rulesLoader.Close(); e != nil {
				err = errors.Join(err, e)
			}
		}
		if rt.registry != nil {
			rt.registry.Close()
		}
		if rt.tracer != nil {
			if e := rt.tracer.Shutdown(); e != nil {
				err = errors.Join(err, e)
			}
		}
		rt.closeErr = err
	})
	return rt.closeErr
}

// Config returns the last loaded project config.
func (rt *Runtime) Config() *config.Settings {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return config.MergeSettings(nil, rt.cfg)
}

// Settings exposes the merged settings.json snapshot for callers that need it.
func (rt *Runtime) Settings() *config.Settings {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return config.MergeSettings(nil, rt.settings)
}

// Sandbox exposes the sandbox manager.
func (rt *Runtime) Sandbox() *sandbox.Manager { return rt.sandbox }

// GetSessionStats returns aggregated token stats for a session.
func (rt *Runtime) GetSessionStats(sessionID string) *SessionTokenStats {
	if rt == nil || rt.tokens == nil {
		return nil
	}
	return rt.tokens.GetSessionStats(sessionID)
}

// GetTotalStats returns aggregated token stats across all sessions.
func (rt *Runtime) GetTotalStats() *SessionTokenStats {
	if rt == nil || rt.tokens == nil {
		return nil
	}
	return rt.tokens.GetTotalStats()
}

// GetContextReport returns an estimated static context breakdown (excluding history).
func (rt *Runtime) GetContextReport() ContextReport {
	if rt == nil {
		return ContextReport{}
	}
	systemPrompt := strings.TrimSpace(rt.opts.SystemPrompt)
	if rt.rulesLoader != nil {
		if rules := strings.TrimSpace(rt.rulesLoader.GetContent()); rules != "" {
			systemPrompt = strings.TrimSpace(systemPrompt + "\n\n## Project Rules\n\n" + rules)
		}
	}
	skillsSnippet := ""
	if strings.TrimSpace(rt.opts.ProjectRoot) != "" {
		skillsSnippet = buildSkillsSnippet(rt.opts.ProjectRoot)
	}
	defs := availableTools(rt.registry, nil)
	return buildContextReport(systemPrompt, skillsSnippet, defs)
}

// ClearSession removes all conversation history for the given session.
// This includes both in-memory history and persisted history on disk.
// The session will start fresh on the next Run() call.
func (rt *Runtime) ClearSession(sessionID string) error {
	if rt == nil {
		return fmt.Errorf("runtime is nil")
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session ID cannot be empty")
	}

	// Clear in-memory history
	if rt.histories != nil {
		rt.histories.mu.Lock()
		delete(rt.histories.data, sessionID)
		delete(rt.histories.lastUsed, sessionID)
		rt.histories.mu.Unlock()
	}

	// Clear persisted history file
	if rt.historyPersister != nil {
		path := rt.historyPersister.filePath(sessionID)
		if path != "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("failed to remove history file: %w", err)
			}
		}
	}

	// Clean up tool output directories
	_ = cleanupToolOutputSessionDir(sessionID)
	_ = cleanupBashOutputSessionDir(sessionID)

	// Reset memory flush / compaction cycle state.
	rt.sessionCompactionCount.Delete(sessionID)
	rt.sessionMemoryFlushAtCompact.Delete(sessionID)
	if rt.tokens != nil {
		rt.tokens.ResetSession(sessionID)
	}

	return nil
}

// GetHistory returns all stored messages for the given session.
// It first checks the in-memory store, then falls back to persisted history.
// Returns nil, nil if no history exists for the session.
func (rt *Runtime) GetHistory(sessionID string) ([]message.Message, error) {
	if rt == nil {
		return nil, fmt.Errorf("runtime is nil")
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session ID cannot be empty")
	}

	// Check in-memory store first (no loader, just peek)
	if rt.histories != nil {
		rt.histories.mu.Lock()
		hist, ok := rt.histories.data[sessionID]
		rt.histories.mu.Unlock()
		if ok && hist != nil {
			return hist.All(), nil
		}
	}

	// Fall back to persisted history on disk
	if rt.historyPersister != nil {
		msgs, err := rt.historyPersister.Load(sessionID)
		if err != nil {
			return nil, fmt.Errorf("load history: %w", err)
		}
		return msgs, nil
	}

	return nil, nil
}

// ----------------- internal helpers -----------------

type preparedRun struct {
	ctx              context.Context
	prompt           string
	systemPrompt     string
	guardPrompt      string
	history          *message.History
	preCompactTokens int
	normalized       Request
	recorder         *hookRecorder
	commandResults   []CommandExecution
	skillResults     []SkillExecution
	subagentResult   *subagents.Result
	mode             ModeContext
	toolWhitelist    map[string]struct{}
	attachments      []model.MessageAttachment // Images for vision API
}

type runResult struct {
	output *agent.ModelOutput
	usage  model.Usage
	reason string
	model  string
}

func (rt *Runtime) prepare(ctx context.Context, req Request) (preparedRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fallbackSession := defaultSessionID(rt.mode.EntryPoint)
	normalized := req.normalized(rt.mode, fallbackSession)
	prompt := strings.TrimSpace(normalized.Prompt)
	if prompt == "" && len(normalized.Attachments) == 0 {
		return preparedRun{}, errors.New("api: prompt is empty")
	}

	if normalized.SessionID == "" {
		normalized.SessionID = fallbackSession
	}

	// Auto-generate RequestID if not provided (UUID tracking)
	if normalized.RequestID == "" {
		normalized.RequestID = uuid.New().String()
	}

	// Save the raw user message before any injections so auto-recall searches
	// what the user actually said, not the enriched prompt with skill names etc.
	rawUserPrompt := prompt

	// Process attachments (convert to base64; audio can be pre-transcribed by ASR)
	var msgAttachments []model.MessageAttachment
	var voiceTranscripts []string
	for _, att := range normalized.Attachments {
		if att.FilePath == "" {
			continue
		}
		attType := resolveAttachmentType(att.Type, att.MimeType, att.FilePath)
		data, mimeType, err := loadAttachmentAsBase64(att.FilePath, att.MimeType, attType)
		if err != nil {
			rt.logger.Warnf("failed to load attachment %s: %v", att.FilePath, err)
			continue
		}
		if attType == "audio" {
			transcript, err := rt.transcribeAudioAttachment(ctx, preparedRun{normalized: normalized}, att.FilePath, mimeType)
			if err != nil {
				rt.logger.Warnf("failed to transcribe audio %s: %v", att.FilePath, err)
			} else if transcript != "" {
				voiceTranscripts = append(voiceTranscripts, transcript)
				// ASR-first mode: consume audio as text.
				continue
			}
			// If ASR is enabled but produced no transcript, do not forward raw audio.
			if rt.asrProvider() != nil {
				continue
			}
		}
		msgAttachments = append(msgAttachments, model.MessageAttachment{
			Type:       attType,
			Data:       data,
			MimeType:   mimeType,
			SourceType: "base64",
		})
	}
	prompt = mergeVoiceTranscripts(prompt, voiceTranscripts)

	if len(msgAttachments) > 0 {
		rt.logger.Debugf("loaded %d attachment(s) for multimodal API", len(msgAttachments))
	}

	history := rt.histories.Get(normalized.SessionID)
	recorder := defaultHookRecorder()
	preCompactTokens := estimateHistoryTokens(history)

	if rt.compactor != nil {
		if _, _, err := rt.compactor.maybeCompact(ctx, history, normalized.SessionID, recorder); err != nil {
			return preparedRun{}, err
		}
	}

	// Context Window Guard: estimate token usage from history and warn/reject
	// if approaching the configured limit. Runs after compaction so the estimate
	// reflects the (possibly-compacted) state of the conversation.
	if rt.opts.ContextWindowTokens > 0 {
		estimated := estimateHistoryTokens(history)
		warnRatio := rt.opts.ContextWindowWarnRatio
		if warnRatio <= 0 {
			warnRatio = 0.8
		}
		hardMin := rt.opts.ContextWindowHardMinTokens
		if hardMin <= 0 {
			hardMin = 2000
		}
		remaining := rt.opts.ContextWindowTokens - estimated
		usageRatio := float64(estimated) / float64(rt.opts.ContextWindowTokens)

		if remaining < hardMin {
			rt.logger.Warnf("[agentsdk] context pressure: estimated %d / %d tokens used, remaining %d (<%d)",
				estimated, rt.opts.ContextWindowTokens, remaining, hardMin)
			if rt.opts.RealtimeEventCallback != nil {
				rt.opts.RealtimeEventCallback(RealtimeEvent{
					Type:      RealtimeEventContextWindowWarn,
					Message:   fmt.Sprintf("context pressure: %d/%d tokens used; automatic memory flush may run", estimated, rt.opts.ContextWindowTokens),
					Timestamp: time.Now(),
					SessionID: normalized.SessionID,
					Metadata: map[string]any{
						"estimated_tokens": estimated,
						"remaining_tokens": remaining,
						"hard_min_tokens":  hardMin,
					},
				})
			}
		}
		if usageRatio >= warnRatio {
			warnMsg := fmt.Sprintf(
				"context usage is ~%.0f%% (%d / %d tokens); automatic memory flush may run",
				usageRatio*100, estimated, rt.opts.ContextWindowTokens,
			)
			rt.logger.Warnf("[agentsdk] context window: estimated %d / %d tokens used (%.0f%%)",
				estimated, rt.opts.ContextWindowTokens, usageRatio*100)
			if rt.opts.RealtimeEventCallback != nil {
				rt.opts.RealtimeEventCallback(RealtimeEvent{
					Type:      RealtimeEventContextWindowWarn,
					Message:   warnMsg,
					Timestamp: time.Now(),
					SessionID: normalized.SessionID,
					Metadata: map[string]any{
						"estimated_tokens": estimated,
						"remaining_tokens": remaining,
						"warn_ratio":       warnRatio,
					},
				})
			}
		}
	}

	activation := normalized.activationContext(prompt)

	cmdRes, cleanPrompt, err := rt.executeCommands(ctx, prompt, &normalized)
	if err != nil {
		return preparedRun{}, err
	}
	prompt = cleanPrompt
	activation.Prompt = prompt

	skillRes, promptAfterSkills, err := rt.executeSkills(ctx, prompt, activation, &normalized)
	if err != nil {
		return preparedRun{}, err
	}
	prompt = promptAfterSkills
	activation.Prompt = prompt
	subRes, promptAfterSubagent, err := rt.executeSubagent(ctx, prompt, activation, &normalized)
	if err != nil {
		return preparedRun{}, err
	}
	prompt = promptAfterSubagent
	activation.Prompt = prompt

	// Auto-recall: search MEMORY.md before the agent turn and prepend relevant
	// memories to the prompt. This mirrors openclaw's before_agent_start hook
	// and ensures the agent doesn't need to decide to call MemorySearch itself.
	if rt.opts.AutoRecall && strings.TrimSpace(rt.opts.ProjectRoot) != "" {
		maxR := rt.opts.AutoRecallMaxResults
		if maxR <= 0 {
			maxR = 3
		}
		rt.logger.Debugf(
			"[agentsdk] auto-recall: start session=%s request=%s max_results=%d prompt_chars=%d project_root=%s",
			normalized.SessionID,
			normalized.RequestID,
			maxR,
			len(rawUserPrompt),
			rt.opts.ProjectRoot,
		)
		results, recallErr := toolbuiltin.SearchMemory(rt.opts.ProjectRoot, rawUserPrompt, maxR)
		recallSource := "raw"
		if recallErr != nil {
			rt.logger.Warnf(
				"[agentsdk] auto-recall: search failed session=%s request=%s err=%v",
				normalized.SessionID,
				normalized.RequestID,
				recallErr,
			)
		}
		if recallErr == nil && len(results) == 0 {
			if fallbackQuery := buildAutoRecallFallbackQuery(rawUserPrompt); fallbackQuery != "" {
				rt.logger.Debugf(
					"[agentsdk] auto-recall: fallback-search session=%s request=%s query_chars=%d",
					normalized.SessionID,
					normalized.RequestID,
					len(fallbackQuery),
				)
				if fbResults, fbErr := toolbuiltin.SearchMemory(rt.opts.ProjectRoot, fallbackQuery, maxR); fbErr != nil {
					rt.logger.Warnf(
						"[agentsdk] auto-recall: fallback search failed session=%s request=%s err=%v",
						normalized.SessionID,
						normalized.RequestID,
						fbErr,
					)
				} else {
					results = fbResults
					recallSource = "fallback"
				}
			}
		}
		if recallErr == nil && len(results) > 0 {
			injectedBlock := buildAutoRecallInjectedBlock(results)
			prompt = injectedBlock + prompt
			rt.logger.Debugf(
				"[agentsdk] auto-recall: injected session=%s request=%s snippets=%d source=%s",
				normalized.SessionID,
				normalized.RequestID,
				len(results),
				recallSource,
			)
		} else if recallErr == nil {
			rt.logger.Debugf(
				"[agentsdk] auto-recall: no-match session=%s request=%s",
				normalized.SessionID,
				normalized.RequestID,
			)
		}
	}

	currentModel := rt.currentModelForSession(normalized.SessionID)
	if currentModel == "" {
		currentModel = strings.TrimSpace(rt.opts.PrimaryModelName)
	}
	systemPrompt := strings.TrimSpace(rt.opts.SystemPrompt)
	if rt.opts.Voice.Enabled && rt.opts.Voice.TTS.Enabled {
		systemPrompt = appendPlainTextForTTSRule(systemPrompt)
	}
	if contextSnippet := buildSystemContextSnippet(rt.opts.ProjectRoot, time.Now(), currentModel); contextSnippet != "" {
		if systemPrompt == "" {
			systemPrompt = contextSnippet
		} else {
			systemPrompt = systemPrompt + "\n\n## Runtime Context\n\n" + contextSnippet
		}
	}

	whitelist := combineToolWhitelists(normalized.ToolWhitelist, nil)
	guardPrompt := strings.TrimSpace(rt.guardBaseSystemPrompt)
	if guardPrompt == "" {
		guardPrompt = strings.TrimSpace(rt.opts.SystemPrompt)
	}
	prep := preparedRun{
		ctx:              ctx,
		prompt:           prompt,
		systemPrompt:     systemPrompt,
		guardPrompt:      guardPrompt,
		history:          history,
		preCompactTokens: preCompactTokens,
		normalized:       normalized,
		recorder:         recorder,
		commandResults:   cmdRes,
		skillResults:     skillRes,
		subagentResult:   subRes,
		mode:             normalized.Mode,
		toolWhitelist:    whitelist,
		attachments:      msgAttachments,
	}
	// Keep memory flush in prepare so Run/RunStream share one trigger path.
	if rt.shouldMemoryFlush(prep.normalized.SessionID, prep.history, estimateHistoryTokens(prep.history)) {
		rt.runMemoryFlushTurn(ctx, prep)
	}
	return prep, nil
}

func (rt *Runtime) runAgent(prep preparedRun) (runResult, error) {
	return rt.runAgentWithMiddleware(prep)
}

func (rt *Runtime) runAgentWithMiddleware(prep preparedRun, extras ...middleware.Middleware) (runResult, error) {
	// Select model based on request tier or subagent mapping
	selectedModel, selectedTier := rt.selectModelForSubagent(prep.normalized.TargetSubagent, prep.normalized.Model)

	// Emit ModelSelected event if a non-default model was selected
	if selectedTier != "" {
		hookAdapter := &runtimeHookAdapter{
			executor:         rt.hooks,
			recorder:         prep.recorder,
			realtimeCallback: rt.opts.RealtimeEventCallback,
			progressInterval: rt.opts.ProgressInterval,
		}
		// Best-effort event emission; errors are logged but don't block execution
		if err := hookAdapter.ModelSelected(prep.ctx, coreevents.ModelSelectedPayload{
			ToolName:  prep.normalized.TargetSubagent,
			ModelTier: string(selectedTier),
			Reason:    "subagent model mapping",
		}); err != nil {
			rt.logger.Warnf("api: failed to emit ModelSelected event: %v", err)
		}
	}

	// Determine cache enablement: request-level overrides global default
	enableCache := rt.opts.DefaultEnableCache
	if prep.normalized.EnablePromptCache != nil {
		enableCache = *prep.normalized.EnablePromptCache
	}

	hookAdapter := &runtimeHookAdapter{
		executor:         rt.hooks,
		recorder:         prep.recorder,
		realtimeCallback: rt.opts.RealtimeEventCallback,
		progressInterval: rt.opts.ProgressInterval,
	}
	baseModel := model.WrapWithFallbackWithOptions(selectedModel, rt.opts.PrimaryFallbackModels, model.FallbackOptions{
		PrimaryModel: rt.opts.PrimaryModelName,
		OnSwitch: func(evt model.FallbackSwitchEvent) {
			if rt.opts.RealtimeEventCallback == nil {
				return
			}
			from := strings.TrimSpace(evt.FromModel)
			to := strings.TrimSpace(evt.ToModel)
			msg := "Model switched to fallback"
			switch {
			case from != "" && to != "":
				msg = fmt.Sprintf("Model fallback switch: %s -> %s", from, to)
			case to != "":
				msg = fmt.Sprintf("Model fallback switch: -> %s", to)
			}
			metadata := map[string]any{
				"from_model": from,
				"to_model":   to,
				"stream":     evt.Stream,
			}
			if evt.LastError != nil {
				metadata["last_error"] = evt.LastError.Error()
			}
			rt.opts.RealtimeEventCallback(RealtimeEvent{
				Type:      RealtimeEventModelSwitch,
				Message:   msg,
				SessionID: prep.normalized.SessionID,
				Timestamp: time.Now(),
				Metadata:  metadata,
			})
		},
	})
	modelAdapter := &conversationModel{
		base:               baseModel,
		history:            prep.history,
		prompt:             prep.prompt,
		trimmer:            rt.newTrimmer(),
		tools:              availableTools(rt.registry, prep.toolWhitelist),
		systemPrompt:       prep.systemPrompt,
		guardPrompt:        prep.guardPrompt,
		rulesLoader:        rt.rulesLoader,
		enableCache:        enableCache,
		hooks:              hookAdapter,
		recorder:           prep.recorder,
		compactor:          rt.compactor,
		sessionID:          prep.normalized.SessionID,
		logger:             rt.logger,
		attachments:        prep.attachments,
		outputGuardEnabled: rt.outputGuardEnabled(),
		requestID:          prep.normalized.RequestID,
	}

	toolExec := &runtimeToolExecutor{
		executor:           rt.executor,
		hooks:              hookAdapter,
		history:            prep.history,
		allow:              prep.toolWhitelist,
		root:               rt.sbRoot,
		host:               "localhost",
		sessionID:          prep.normalized.SessionID,
		logger:             rt.logger,
		permissionResolver: buildPermissionResolver(hookAdapter, rt.opts.PermissionRequestHandler, rt.opts.ApprovalQueue, rt.opts.ApprovalApprover, rt.opts.ApprovalWhitelistTTL, rt.opts.ApprovalWait),
	}

	// Build middleware chain
	chainItems := make([]middleware.Middleware, 0, len(rt.opts.Middleware)+len(extras))

	if len(rt.opts.Middleware) > 0 {
		chainItems = append(chainItems, rt.opts.Middleware...)
	}
	if len(extras) > 0 {
		chainItems = append(chainItems, extras...)
	}
	chain := middleware.NewChain(chainItems, middleware.WithTimeout(rt.opts.MiddlewareTimeout))

	ag, err := agent.New(modelAdapter, toolExec, agent.Options{
		MaxIterations: rt.opts.MaxIterations,
		Timeout:       rt.opts.Timeout,
		Middleware:    chain,
	})
	if err != nil {
		rt.logger.Errorf("[agentsdk] Failed to create agent: %v", err)
		return runResult{}, err
	}

	agentCtx := agent.NewContext()
	if sessionID := strings.TrimSpace(prep.normalized.SessionID); sessionID != "" {
		agentCtx.Values["session_id"] = sessionID
	}
	// Propagate RequestID through agent context for distributed tracing
	if requestID := strings.TrimSpace(prep.normalized.RequestID); requestID != "" {
		agentCtx.Values["request_id"] = requestID
	}
	if len(prep.normalized.ForceSkills) > 0 {
		agentCtx.Values["request.force_skills"] = append([]string(nil), prep.normalized.ForceSkills...)
	}
	if rt.skReg != nil {
		agentCtx.Values["skills.registry"] = rt.skReg
	}

	out, err := ag.Run(prep.ctx, agentCtx)
	if err != nil {
		rt.logger.Errorf("[agentsdk] agent.Run failed: %v", err)
		return runResult{}, err
	}

	if rt.tokens != nil && rt.tokens.IsEnabled() {
		stats := tokenStatsFromUsage(modelAdapter.usage, "", prep.normalized.SessionID, prep.normalized.RequestID)
		rt.tokens.Record(stats)
		payload := coreevents.TokenUsagePayload{
			InputTokens:   stats.InputTokens,
			OutputTokens:  stats.OutputTokens,
			TotalTokens:   stats.TotalTokens,
			CacheCreation: stats.CacheCreation,
			CacheRead:     stats.CacheRead,
			Model:         stats.Model,
			SessionID:     stats.SessionID,
			RequestID:     stats.RequestID,
		}
		if rt.hooks != nil {
			//nolint:errcheck // token usage events are non-critical notifications
			rt.hooks.Publish(coreevents.Event{
				Type:      coreevents.TokenUsage,
				SessionID: stats.SessionID,
				RequestID: stats.RequestID,
				Payload:   payload,
			})
		}
		if prep.recorder != nil {
			prep.recorder.Record(coreevents.Event{
				Type:      coreevents.TokenUsage,
				SessionID: stats.SessionID,
				RequestID: stats.RequestID,
				Payload:   payload,
			})
		}
	}
	return runResult{
		output: out,
		usage:  modelAdapter.usage,
		reason: modelAdapter.stopReason,
		model:  strings.TrimSpace(modelAdapter.actualModel),
	}, nil
}

func (rt *Runtime) buildResponse(prep preparedRun, result runResult) *Response {
	events := []coreevents.Event(nil)
	if prep.recorder != nil {
		events = prep.recorder.Drain()
	}

	// Scan events and emit FileAttachment events
	newEvents := rt.extractSpecialEvents(events)
	events = append(events, newEvents...)

	resp := &Response{
		Mode:            prep.mode,
		RequestID:       prep.normalized.RequestID,
		Result:          convertRunResult(result),
		CommandResults:  prep.commandResults,
		SkillResults:    prep.skillResults,
		Subagent:        prep.subagentResult,
		HookEvents:      events,
		ProjectConfig:   rt.Settings(),
		Settings:        rt.Settings(),
		SandboxSnapshot: rt.sandboxReport(),
		Tags:            maps.Clone(prep.normalized.Tags),
	}
	return resp
}

func (rt *Runtime) sandboxReport() SandboxReport {
	report := snapshotSandbox(rt.sandbox)

	var roots []string
	if root := strings.TrimSpace(rt.sbRoot); root != "" {
		roots = append(roots, root)
	}
	report.Roots = cloneStrings(roots)

	allowed := make([]string, 0, len(rt.opts.Sandbox.AllowedPaths))
	for _, path := range rt.opts.Sandbox.AllowedPaths {
		if clean := strings.TrimSpace(path); clean != "" {
			allowed = append(allowed, clean)
		}
	}
	for _, path := range additionalSandboxPaths(rt.settings) {
		if clean := strings.TrimSpace(path); clean != "" {
			allowed = append(allowed, clean)
		}
	}
	report.AllowedPaths = cloneStrings(allowed)

	domains := rt.opts.Sandbox.NetworkAllow
	if len(domains) == 0 {
		domains = defaultNetworkAllowList(rt.opts.EntryPoint)
	}
	var cleanedDomains []string
	for _, domain := range domains {
		if host := strings.TrimSpace(domain); host != "" {
			cleanedDomains = append(cleanedDomains, host)
		}
	}
	report.AllowedDomains = cloneStrings(cleanedDomains)
	return report
}

func convertRunResult(res runResult) *Result {
	if res.output == nil {
		return nil
	}
	toolCalls := make([]model.ToolCall, len(res.output.ToolCalls))
	for i, call := range res.output.ToolCalls {
		toolCalls[i] = model.ToolCall{Name: call.Name, Arguments: call.Input}
	}
	return &Result{
		Output:     res.output.Content,
		ToolCalls:  toolCalls,
		Usage:      res.usage,
		StopReason: res.reason,
	}
}

func (rt *Runtime) executeCommands(ctx context.Context, prompt string, req *Request) ([]CommandExecution, string, error) {
	if rt.cmdExec == nil {
		return nil, prompt, nil
	}
	invocations, err := commands.Parse(prompt)
	if err != nil {
		if errors.Is(err, commands.ErrNoCommand) {
			return nil, prompt, nil
		}
		return nil, "", err
	}
	cleanPrompt := removeCommandLines(prompt, invocations)
	results, err := rt.cmdExec.Execute(ctx, invocations)
	if err != nil {
		return nil, "", err
	}
	execs := make([]CommandExecution, 0, len(results))
	for _, res := range results {
		def := definitionSnapshot(rt.cmdExec, res.Command)
		execs = append(execs, CommandExecution{Definition: def, Result: res})
		cleanPrompt = applyPromptMetadata(cleanPrompt, res.Metadata)
		mergeTags(req, res.Metadata)
		applyCommandMetadata(req, res.Metadata)
	}
	return execs, cleanPrompt, nil
}

func (rt *Runtime) executeSkills(ctx context.Context, prompt string, activation skills.ActivationContext, req *Request) ([]SkillExecution, string, error) {
	if rt.skReg == nil {
		return nil, prompt, nil
	}
	matches := rt.skReg.Match(activation)
	forced := orderedForcedSkills(rt.skReg, req.ForceSkills)
	matches = append(matches, forced...)
	if len(matches) == 0 {
		return nil, prompt, nil
	}
	prefix := ""
	execs := make([]SkillExecution, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		skill := match.Skill
		if skill == nil {
			continue
		}
		name := skill.Definition().Name
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		res, err := skill.Execute(ctx, activation)
		execs = append(execs, SkillExecution{Definition: skill.Definition(), Result: res, Err: err})
		if err != nil {
			return execs, "", err
		}
		prefix = combinePrompt(prefix, res.Output)
		activation.Metadata = mergeMetadata(activation.Metadata, res.Metadata)
		mergeTags(req, res.Metadata)
		applyCommandMetadata(req, res.Metadata)
	}
	prompt = prependPrompt(prompt, prefix)
	prompt = applyPromptMetadata(prompt, activation.Metadata)
	return execs, prompt, nil
}

func (rt *Runtime) executeSubagent(ctx context.Context, prompt string, activation skills.ActivationContext, req *Request) (*subagents.Result, string, error) {
	if req == nil {
		return nil, prompt, nil
	}

	def, builtin := applySubagentTarget(req)
	if rt.subMgr == nil {
		return nil, prompt, nil
	}
	meta := map[string]any{
		"entrypoint": req.Mode.EntryPoint,
	}
	if len(req.Metadata) > 0 {
		if len(meta) == 0 {
			meta = map[string]any{}
		}
		for k, v := range req.Metadata {
			meta[k] = v
		}
	}
	if session := strings.TrimSpace(req.SessionID); session != "" {
		meta["session_id"] = session
	}
	request := subagents.Request{
		Target:        req.TargetSubagent,
		Instruction:   prompt,
		Activation:    activation,
		ToolWhitelist: cloneStrings(req.ToolWhitelist),
		Metadata:      meta,
	}
	dispatchCtx := ctx
	if dispatchCtx == nil {
		dispatchCtx = context.Background()
	}
	if subCtx, ok := buildSubagentContext(*req, def, builtin); ok {
		dispatchCtx = subagents.WithContext(dispatchCtx, subCtx)
	}
	res, err := rt.subMgr.Dispatch(dispatchCtx, request)
	if err != nil {
		if errors.Is(err, subagents.ErrDispatchUnauthorized) {
			return nil, prompt, nil
		}
		if errors.Is(err, subagents.ErrNoMatchingSubagent) && req.TargetSubagent == "" {
			return nil, prompt, nil
		}
		return nil, "", err
	}
	text := fmt.Sprint(res.Output)
	if strings.TrimSpace(text) != "" {
		prompt = strings.TrimSpace(text)
	}
	prompt = applyPromptMetadata(prompt, res.Metadata)
	mergeTags(req, res.Metadata)
	applyCommandMetadata(req, res.Metadata)
	return &res, prompt, nil
}

func (rt *Runtime) taskRunner() toolbuiltin.TaskRunner {
	return func(ctx context.Context, req toolbuiltin.TaskRequest) (*tool.ToolResult, error) {
		return rt.runTaskInvocation(ctx, req)
	}
}

func (rt *Runtime) runTaskInvocation(ctx context.Context, req toolbuiltin.TaskRequest) (*tool.ToolResult, error) {
	if rt == nil {
		return nil, errors.New("api: runtime is nil")
	}
	if rt.subMgr == nil {
		return nil, errors.New("api: subagent manager is not configured")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("api: task prompt is empty")
	}
	sessionID := strings.TrimSpace(req.Resume)
	if sessionID == "" {
		sessionID = defaultSessionID(rt.mode.EntryPoint)
	}
	reqPayload := &Request{
		Prompt:         prompt,
		Mode:           rt.mode,
		SessionID:      sessionID,
		TargetSubagent: req.SubagentType,
	}
	if desc := strings.TrimSpace(req.Description); desc != "" {
		reqPayload.Metadata = map[string]any{"task.description": desc}
	}
	if req.Model != "" {
		if reqPayload.Metadata == nil {
			reqPayload.Metadata = map[string]any{}
		}
		reqPayload.Metadata["task.model"] = req.Model
	}
	activation := skills.ActivationContext{Prompt: prompt}
	if len(reqPayload.Metadata) > 0 {
		activation.Metadata = maps.Clone(reqPayload.Metadata)
	}
	dispatchCtx := subagents.WithTaskDispatch(ctx)
	res, _, err := rt.executeSubagent(dispatchCtx, prompt, activation, reqPayload)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("api: task execution returned no result")
	}
	return convertTaskToolResult(*res), nil
}

func convertTaskToolResult(res subagents.Result) *tool.ToolResult {
	output := strings.TrimSpace(fmt.Sprint(res.Output))
	if output == "" {
		if res.Subagent != "" {
			output = fmt.Sprintf("subagent %s completed", res.Subagent)
		} else {
			output = "subagent completed"
		}
	}
	data := map[string]any{
		"subagent": res.Subagent,
	}
	if len(res.Metadata) > 0 {
		data["metadata"] = res.Metadata
	}
	if res.Error != "" {
		data["error"] = res.Error
	}
	return &tool.ToolResult{
		Success: res.Error == "",
		Output:  output,
		Data:    data,
	}
}

// selectModelForSubagent returns the appropriate model for the given subagent type.
// Priority: 1) Request.Model override, 2) SubagentModelMapping, 3) default Model.
// Returns the selected model and the tier used (empty string if default).
func (rt *Runtime) selectModelForSubagent(subagentType string, requestTier ModelTier) (model.Model, ModelTier) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// Priority 1: Request-level override (方案 C)
	if requestTier != "" {
		if m, ok := rt.opts.ModelPool[requestTier]; ok && m != nil {
			return m, requestTier
		}
	}

	// Priority 2: Subagent type mapping (方案 A)
	if rt.opts.SubagentModelMapping != nil {
		canonical := strings.ToLower(strings.TrimSpace(subagentType))
		if tier, ok := rt.opts.SubagentModelMapping[canonical]; ok {
			if rt.opts.ModelPool != nil {
				if m, ok := rt.opts.ModelPool[tier]; ok && m != nil {
					return m, tier
				}
			}
		}
	}

	// Priority 3: Default model
	return rt.opts.Model, ""
}

func (rt *Runtime) newTrimmer() *message.Trimmer {
	if rt.opts.TokenLimit <= 0 {
		return nil
	}
	return message.NewTrimmer(rt.opts.TokenLimit, nil)
}

// estimateHistoryTokens returns a rough token count for the conversation history
// using the common approximation of 1 token ≈ 4 bytes of UTF-8 text.
// Intended for lightweight Context Window Guard checks, not billing-accurate counting.
func estimateHistoryTokens(history *message.History) int {
	if history == nil {
		return 0
	}
	total := 0
	for _, msg := range history.All() {
		total += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			// name + result + small fixed overhead for ID and JSON structure
			total += len(tc.Name) + len(tc.Result) + 20
		}
	}
	return total / 4
}

// shouldMemoryFlush returns true if current estimated session tokens crossed the
// memory-flush threshold and no flush has been performed in this compaction cycle.
func (rt *Runtime) shouldMemoryFlush(sessionID string, history *message.History, estimatedTokens int) bool {
	cfg := rt.opts.MemoryFlush
	if !cfg.Enabled || rt.opts.ContextWindowTokens <= 0 {
		return false
	}
	if estimatedTokens <= 0 {
		if history == nil {
			return false
		}
		estimatedTokens = estimateHistoryTokens(history)
	}
	if estimatedTokens <= 0 {
		return false
	}
	soft := cfg.SoftThresholdTokens
	if soft <= 0 {
		soft = 4000
	}
	reserve := cfg.ReserveTokensFloor
	if reserve <= 0 {
		reserve = 20000
	}
	threshold := rt.opts.ContextWindowTokens - reserve - soft
	if threshold <= 0 {
		return false
	}
	if estimatedTokens < threshold {
		return false
	}
	currentCompaction := rt.sessionCompactionValue(sessionID)
	if lastFlushAt, ok := rt.sessionMemoryFlushAtCompaction(sessionID); ok && lastFlushAt == currentCompaction {
		return false
	}
	return true
}

// runMemoryFlushTurn runs a hidden agent turn that asks the agent to persist
// important memories to disk. The turn's messages are NOT added to the real
// session history; only tool side-effects (file writes) persist.
func (rt *Runtime) runMemoryFlushTurn(ctx context.Context, prep preparedRun) {
	cfg := rt.opts.MemoryFlush
	soft := cfg.SoftThresholdTokens
	if soft <= 0 {
		soft = 4000
	}
	reserve := cfg.ReserveTokensFloor
	if reserve <= 0 {
		reserve = 20000
	}
	threshold := rt.opts.ContextWindowTokens - reserve - soft
	estimated := prep.preCompactTokens
	if estimated <= 0 {
		estimated = estimateHistoryTokens(prep.history)
	}

	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = "Pre-compaction memory flush. Store durable memories now. " +
			"Write a short index in MEMORY.md, and put detailed notes in memory/projects.md, memory/lessons.md, or memory/YYYY-MM-DD.md. " +
			"If nothing important to store, just say so briefly."
	}

	// Clone the current history so the flush turn's messages don't pollute the real session.
	flushHistory := message.NewHistory()
	flushHistory.Replace(prep.history.All())

	flushPrep := preparedRun{
		ctx:           ctx,
		history:       flushHistory,
		prompt:        prompt,
		normalized:    prep.normalized,
		recorder:      defaultHookRecorder(), // discard flush events from the response
		mode:          prep.mode,
		toolWhitelist: prep.toolWhitelist,
	}

	rt.logger.Infof("[agentsdk] Running memory flush for session %s (context pressure)", prep.normalized.SessionID)
	if rt.opts.RealtimeEventCallback != nil {
		rt.opts.RealtimeEventCallback(RealtimeEvent{
			Type:      RealtimeEventMemoryFlushStart,
			Message:   "automatic memory flush started",
			Timestamp: time.Now(),
			SessionID: prep.normalized.SessionID,
			Metadata: map[string]any{
				"estimated_tokens": estimated,
				"threshold_tokens": threshold,
			},
		})
	}
	if _, err := rt.runAgentWithMiddleware(flushPrep); err != nil {
		rt.logger.Warnf("[agentsdk] Memory flush turn failed: %v", err)
		if rt.opts.RealtimeEventCallback != nil {
			rt.opts.RealtimeEventCallback(RealtimeEvent{
				Type:      RealtimeEventMemoryFlushFailed,
				Message:   "automatic memory flush failed",
				Timestamp: time.Now(),
				SessionID: prep.normalized.SessionID,
				Metadata: map[string]any{
					"error":            err.Error(),
					"estimated_tokens": estimated,
					"threshold_tokens": threshold,
				},
			})
		}
		return
	}
	rt.sessionMemoryFlushAtCompact.Store(prep.normalized.SessionID, rt.sessionCompactionValue(prep.normalized.SessionID))
	if rt.opts.RealtimeEventCallback != nil {
		rt.opts.RealtimeEventCallback(RealtimeEvent{
			Type:      RealtimeEventMemoryFlushDone,
			Message:   "automatic memory flush completed",
			Timestamp: time.Now(),
			SessionID: prep.normalized.SessionID,
			Metadata: map[string]any{
				"estimated_tokens": estimated,
				"threshold_tokens": threshold,
				"compaction_cycle": rt.sessionCompactionValue(prep.normalized.SessionID),
			},
		})
	}
}

func (rt *Runtime) sessionCompactionValue(sessionID string) int64 {
	if rt == nil {
		return 0
	}
	if v, ok := rt.sessionCompactionCount.Load(sessionID); ok {
		if n, ok := v.(int64); ok && n >= 0 {
			return n
		}
	}
	return 0
}

func (rt *Runtime) sessionMemoryFlushAtCompaction(sessionID string) (int64, bool) {
	if rt == nil {
		return 0, false
	}
	v, ok := rt.sessionMemoryFlushAtCompact.Load(sessionID)
	if !ok {
		return 0, false
	}
	n, ok := v.(int64)
	if !ok {
		return 0, false
	}
	return n, true
}

func (rt *Runtime) currentModelForSession(sessionID string) string {
	if rt == nil {
		return ""
	}
	key := strings.TrimSpace(sessionID)
	if key == "" {
		return ""
	}
	if v, ok := rt.sessionCurrentModel.Load(key); ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// ----------------- adapters -----------------

type conversationModel struct {
	base               model.Model
	history            *message.History
	prompt             string
	trimmer            *message.Trimmer
	tools              []model.ToolDefinition
	systemPrompt       string
	guardPrompt        string
	rulesLoader        *config.RulesLoader
	enableCache        bool // Enable prompt caching for this conversation
	usage              model.Usage
	stopReason         string
	hooks              *runtimeHookAdapter
	recorder           *hookRecorder
	compactor          *compactor
	sessionID          string
	logger             logger.Logger
	attachments        []model.MessageAttachment // Images for vision API
	outputGuardEnabled bool
	requestID          string
	actualModel        string
}

func (m *conversationModel) Generate(ctx context.Context, _ *agent.Context) (*agent.ModelOutput, error) {
	if m.base == nil {
		return nil, errors.New("model is nil")
	}

	if strings.TrimSpace(m.prompt) != "" {
		userMsg := message.Message{
			Role:        "user",
			Content:     strings.TrimSpace(m.prompt),
			Attachments: m.attachments,
		}
		m.history.Append(userMsg)
		if err := m.hooks.UserPrompt(ctx, m.prompt); err != nil {
			return nil, err
		}
		m.prompt = ""
		m.attachments = nil // Clear after use
	}

	if m.compactor != nil {
		if _, _, err := m.compactor.maybeCompact(ctx, m.history, m.sessionID, m.recorder); err != nil {
			return nil, err
		}
	}

	snapshot := m.history.All()
	if m.trimmer != nil {
		snapshot = m.trimmer.Trim(snapshot)
	}

	systemPrompt := m.systemPrompt
	if m.rulesLoader != nil {
		if rules := m.rulesLoader.GetContent(); rules != "" {
			systemPrompt = fmt.Sprintf("%s\n\n## Project Rules\n\n%s", systemPrompt, rules)
		}
	}
	req := model.Request{
		Messages:          convertMessages(snapshot),
		Tools:             m.tools,
		System:            systemPrompt,
		MaxTokens:         0,
		Model:             "",
		Temperature:       nil,
		EnablePromptCache: m.enableCache,
	}

	// Populate middleware state with model request if available
	if st, ok := ctx.Value(model.MiddlewareStateKey).(*middleware.State); ok && st != nil {
		st.ModelInput = req
		if st.Values == nil {
			st.Values = map[string]any{}
		}
		st.Values["model.request"] = req
	}

	// Use streaming internally: some API proxies return empty tool_use.input
	// in non-streaming mode but work correctly with streaming. Streaming is
	// also the production-standard path for the Anthropic API.
	var resp *model.Response
	emit := streamEmitFromContext(ctx)
	textStreamed := false
	textBlockStarted := false
	textBlockIndex := 0
	if err := m.base.CompleteStream(ctx, req, func(sr model.StreamResult) error {
		if sr.Delta != "" && emit != nil {
			if !textBlockStarted {
				idx := textBlockIndex
				emit(ctx, StreamEvent{
					Type:         EventContentBlockStart,
					Index:        &idx,
					ContentBlock: &ContentBlock{Type: "text"},
				})
				textBlockStarted = true
			}
			for _, r := range sr.Delta {
				idx := textBlockIndex
				emit(ctx, StreamEvent{
					Type:  EventContentBlockDelta,
					Index: &idx,
					Delta: &Delta{Type: "text_delta", Text: string(r)},
				})
			}
			textStreamed = true
		}
		if sr.Final && sr.Response != nil {
			resp = sr.Response
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if used := usedModelFromBase(m.base); used != "" {
		m.actualModel = used
	}
	if textBlockStarted && emit != nil {
		idx := textBlockIndex
		emit(ctx, StreamEvent{Type: EventContentBlockStop, Index: &idx})
	}
	if resp == nil {
		return nil, errors.New("model returned no final response")
	}
	m.usage = mergeUsage(m.usage, resp.Usage)
	m.stopReason = resp.StopReason

	// Populate middleware state with model response and usage
	if st, ok := ctx.Value(model.MiddlewareStateKey).(*middleware.State); ok && st != nil {
		st.ModelOutput = resp
		if st.Values == nil {
			st.Values = map[string]any{}
		}
		st.Values["model.response"] = resp
		st.Values["model.usage"] = resp.Usage
		st.Values["model.stop_reason"] = resp.StopReason
		if textStreamed {
			st.Values["model.text_streamed"] = true
		}
	}

	assistant := message.Message{Role: resp.Message.Role, Content: strings.TrimSpace(resp.Message.Content)}
	if len(resp.Message.ToolCalls) > 0 {
		assistant.ToolCalls = make([]message.ToolCall, len(resp.Message.ToolCalls))
		for i, call := range resp.Message.ToolCalls {
			assistant.ToolCalls[i] = message.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
		}
	}
	if m.outputGuardEnabled {
		originalContent := assistant.Content
		guardPrompt := strings.TrimSpace(m.guardPrompt)
		if guardPrompt == "" {
			guardPrompt = m.systemPrompt
		}
		redacted, blocked, reason, signal := redactAssistantDisclosure(assistant.Content, guardPrompt)
		if blocked {
			if m.logger != nil {
				m.logger.Warnf("[agentsdk] output_guard_redacted: session=%s request=%s reason=%s output=%q", m.sessionID, strings.TrimSpace(m.requestID), reason, originalContent)
			}
			assistant.Content = redacted
			assistant.ToolCalls = nil
		} else if signal != "" && m.logger != nil {
			m.logger.Warnf("[agentsdk] output_guard_signal: session=%s request=%s signal=%s output=%q", m.sessionID, strings.TrimSpace(m.requestID), signal, originalContent)
		}
	}
	// Fix null/empty tool_use.input BEFORE appending to history.
	// Some API proxies strip tool_use.input entirely (returning null), which causes
	// 400 errors when the message is replayed in subsequent requests. Substitute {}
	// so the stored history always contains a valid dictionary.
	for i, call := range assistant.ToolCalls {
		if len(call.Arguments) == 0 {
			m.logger.Warnf("[agentsdk] tool call %q (id=%s) has no arguments — "+
				"API proxy likely stripped tool_use.input; will use sentinel {\"_\":\"\"} on next request", call.Name, call.ID)
			// Use a non-empty sentinel so API proxies don't strip tool_use.input.
			// Tools that take no parameters safely ignore this key.
			assistant.ToolCalls[i].Arguments = map[string]any{"_": ""}
		}
	}

	m.history.Append(assistant)

	out := &agent.ModelOutput{Content: assistant.Content, Done: len(assistant.ToolCalls) == 0}
	if len(assistant.ToolCalls) > 0 {
		out.ToolCalls = make([]agent.ToolCall, len(assistant.ToolCalls))
		for i, call := range assistant.ToolCalls {
			out.ToolCalls[i] = agent.ToolCall{ID: call.ID, Name: call.Name, Input: call.Arguments}
		}
	}
	return out, nil
}

type runtimeToolExecutor struct {
	executor  *tool.Executor
	hooks     *runtimeHookAdapter
	history   *message.History
	allow     map[string]struct{}
	root      string
	host      string
	sessionID string
	logger    logger.Logger

	permissionResolver tool.PermissionResolver
}

func (t *runtimeToolExecutor) measureUsage() sandbox.ResourceUsage {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return sandbox.ResourceUsage{MemoryBytes: stats.Alloc}
}

func (t *runtimeToolExecutor) isAllowed(ctx context.Context, name string) bool {
	canon := canonicalToolName(name)
	if canon == "" {
		return false
	}
	reqAllowed := len(t.allow) == 0
	if len(t.allow) > 0 {
		_, reqAllowed = t.allow[canon]
	}
	subCtx, ok := subagents.FromContext(ctx)
	if !ok || len(subCtx.ToolWhitelist) == 0 {
		return reqAllowed
	}
	subSet := toLowerSet(subCtx.ToolWhitelist)
	if len(subSet) == 0 {
		return reqAllowed
	}
	_, subAllowed := subSet[canon]
	if len(t.allow) == 0 {
		return subAllowed
	}
	return reqAllowed && subAllowed
}

func (t *runtimeToolExecutor) Execute(ctx context.Context, call agent.ToolCall, _ *agent.Context) (agent.ToolResult, error) {
	if t.executor == nil {
		return agent.ToolResult{}, errors.New("tool executor not initialised")
	}
	if !t.isAllowed(ctx, call.Name) {
		return agent.ToolResult{}, fmt.Errorf("tool %s is not whitelisted", call.Name)
	}

	// Defensive check: if tool call has empty/nil arguments but the tool requires
	// parameters, return a diagnostic error instead of executing with missing params.
	// This commonly happens when an API proxy strips tool_use.input (returns "input": {}).
	if len(call.Input) == 0 {
		if reg := t.executor.Registry(); reg != nil {
			if impl, err := reg.Get(call.Name); err == nil {
				if schema := impl.Schema(); schema != nil && len(schema.Required) > 0 {
					errMsg := fmt.Sprintf(
						"tool %q called with empty arguments but requires %v; "+
							"the API proxy likely stripped tool_use.input — check proxy configuration",
						call.Name, schema.Required)
					t.logger.Warnf("WARNING: %s (id=%s)", errMsg, call.ID)
					if t.history != nil {
						t.history.Append(message.Message{
							Role: "tool",
							ToolCalls: []message.ToolCall{{
								ID:     call.ID,
								Name:   call.Name,
								Result: errMsg,
							}},
						})
					}
					return agent.ToolResult{
						Name:     call.Name,
						Output:   errMsg,
						Metadata: map[string]any{"error": "empty_arguments"},
					}, nil
				}
			}
		}
	}

	// Helper to append tool result to history
	appendToolResult := func(content string) {
		if t.history != nil {
			t.history.Append(message.Message{
				Role: "tool",
				ToolCalls: []message.ToolCall{{
					ID:     call.ID,
					Name:   call.Name,
					Result: content,
				}},
			})
		}
	}

	params, preErr := t.hooks.PreToolUse(ctx, coreToolUsePayload(call))
	if preErr != nil {
		if errors.Is(preErr, ErrToolUseRequiresApproval) && t.permissionResolver != nil {
			checkParams := call.Input
			if params != nil {
				checkParams = params
			}
			decision, err := t.permissionResolver(ctx, tool.Call{
				Name:      call.Name,
				Params:    checkParams,
				SessionID: t.sessionID,
			}, security.PermissionDecision{
				Action: security.PermissionAsk,
				Tool:   call.Name,
				Rule:   "hook:pre_tool_use",
			})
			if err != nil {
				preErr = err
			} else {
				switch decision.Action {
				case security.PermissionAllow:
					preErr = nil
				case security.PermissionDeny:
					preErr = fmt.Errorf("%w: %s", ErrToolUseDenied, call.Name)
				default:
					preErr = fmt.Errorf("%w: %s", ErrToolUseRequiresApproval, call.Name)
				}
			}
		}
	}
	if preErr != nil {
		// Hook denied execution - still need to add tool_result to history
		errContent := fmt.Sprintf(`{"error":%q}`, preErr.Error())
		appendToolResult(errContent)
		return agent.ToolResult{Name: call.Name, Output: errContent, Metadata: map[string]any{"error": preErr.Error()}}, preErr
	}
	if params != nil {
		call.Input = params
	}

	callSpec := tool.Call{
		Name:      call.Name,
		Params:    call.Input,
		Path:      t.root,
		Host:      t.host,
		Usage:     t.measureUsage(),
		SessionID: t.sessionID,
	}
	if emit := streamEmitFromContext(ctx); emit != nil {
		callSpec.StreamSink = func(chunk string, isStderr bool) {
			evt := StreamEvent{
				Type:      EventToolExecutionOutput,
				ToolUseID: call.ID,
				Name:      call.Name,
				Output:    chunk,
			}
			evt.IsStderr = &isStderr
			emit(ctx, evt)
		}
	}
	if t.host != "" {
		callSpec.Host = t.host
	}
	exec := t.executor
	if t.permissionResolver != nil {
		exec = exec.WithPermissionResolver(t.permissionResolver)
	}
	result, err := exec.Execute(ctx, callSpec)
	toolResult := agent.ToolResult{Name: call.Name}
	meta := map[string]any{}
	content := ""
	if result != nil && result.Result != nil {
		toolResult.Output = result.Result.Output
		meta["data"] = result.Result.Data
		if result.Result.OutputRef != nil {
			meta["output_ref"] = result.Result.OutputRef
		}
		content = result.Result.Output
	}
	if err != nil {
		meta["error"] = err.Error()
		content = fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	if len(meta) > 0 {
		toolResult.Metadata = meta
	}

	if hookErr := t.hooks.PostToolUse(ctx, coreToolResultPayload(call, result, err)); hookErr != nil && err == nil {
		// Hook failed - still need to add tool_result to history
		appendToolResult(content)
		return toolResult, hookErr
	}

	appendToolResult(content)
	return toolResult, err
}

func coreToolUsePayload(call agent.ToolCall) coreevents.ToolUsePayload {
	return coreevents.ToolUsePayload{Name: call.Name, Params: call.Input}
}

func coreToolResultPayload(call agent.ToolCall, res *tool.CallResult, err error) coreevents.ToolResultPayload {
	payload := coreevents.ToolResultPayload{
		Name:   call.Name,
		Params: call.Input, // Include original params
	}
	if res != nil && res.Result != nil {
		payload.Result = res.Result.Output
		payload.Duration = res.Duration()
	}
	payload.Err = err
	return payload
}

func buildPermissionResolver(hooks *runtimeHookAdapter, handler PermissionRequestHandler, approvals *security.ApprovalQueue, approver string, whitelistTTL time.Duration, approvalWait bool) tool.PermissionResolver {
	if hooks == nil && handler == nil && approvals == nil {
		return nil
	}
	return func(ctx context.Context, call tool.Call, decision security.PermissionDecision) (security.PermissionDecision, error) {
		if decision.Action != security.PermissionAsk {
			return decision, nil
		}

		req := PermissionRequest{
			ToolName:   call.Name,
			ToolParams: call.Params,
			SessionID:  call.SessionID,
			Rule:       decision.Rule,
			Target:     decision.Target,
			Reason:     buildPermissionReason(decision),
		}

		var record *security.ApprovalRecord
		if approvals != nil && strings.TrimSpace(call.SessionID) != "" {
			command := formatApprovalCommand(call.Name, decision.Target)
			rec, err := approvals.Request(call.SessionID, command, nil)
			if err != nil {
				return decision, err
			}
			record = rec
			req.Approval = rec
			if rec != nil && rec.State == security.ApprovalApproved && rec.AutoApproved {
				return decisionWithAction(decision, security.PermissionAllow), nil
			}
		}

		if hooks != nil {
			hookDecision, err := hooks.PermissionRequest(ctx, coreevents.PermissionRequestPayload{
				ToolName:   call.Name,
				ToolParams: call.Params,
				Reason:     req.Reason,
			})
			if err != nil {
				return decision, err
			}
			switch hookDecision {
			case coreevents.PermissionAllow:
				if record != nil {
					if _, err := approvals.Approve(record.ID, approvalActor(approver), whitelistTTL); err != nil {
						return decision, err
					}
				}
				return decisionWithAction(decision, security.PermissionAllow), nil
			case coreevents.PermissionDeny:
				if record != nil {
					if _, err := approvals.Deny(record.ID, approvalActor(approver), "denied by permission hook"); err != nil {
						return decision, err
					}
				}
				return decisionWithAction(decision, security.PermissionDeny), nil
			}
		}

		if handler != nil {
			hostDecision, err := handler(ctx, req)
			if err != nil {
				return decision, err
			}
			switch hostDecision {
			case coreevents.PermissionAllow:
				if record != nil {
					if _, err := approvals.Approve(record.ID, approvalActor(approver), whitelistTTL); err != nil {
						return decision, err
					}
				}
				return decisionWithAction(decision, security.PermissionAllow), nil
			case coreevents.PermissionDeny:
				if record != nil {
					if _, err := approvals.Deny(record.ID, approvalActor(approver), "denied by host"); err != nil {
						return decision, err
					}
				}
				return decisionWithAction(decision, security.PermissionDeny), nil
			}
		}

		if approvalWait && approvals != nil && record != nil {
			resolved, err := approvals.Wait(ctx, record.ID)
			if err != nil {
				return decision, err
			}
			switch resolved.State {
			case security.ApprovalApproved:
				return decisionWithAction(decision, security.PermissionAllow), nil
			case security.ApprovalDenied:
				return decisionWithAction(decision, security.PermissionDeny), nil
			}
		}

		return decision, nil
	}
}

func buildPermissionReason(decision security.PermissionDecision) string {
	rule := strings.TrimSpace(decision.Rule)
	target := strings.TrimSpace(decision.Target)
	switch {
	case rule == "" && target == "":
		return ""
	case rule == "":
		return fmt.Sprintf("target %q", target)
	case target == "":
		return fmt.Sprintf("rule %q", rule)
	default:
		return fmt.Sprintf("rule %q for %s", rule, target)
	}
}

func formatApprovalCommand(toolName, target string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "tool"
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return name
	}
	return fmt.Sprintf("%s(%s)", name, target)
}

func decisionWithAction(base security.PermissionDecision, action security.PermissionAction) security.PermissionDecision {
	base.Action = action
	return base
}

func approvalActor(approver string) string {
	if strings.TrimSpace(approver) == "" {
		return "host"
	}
	return strings.TrimSpace(approver)
}

// ----------------- config + registries -----------------

func registerTools(registry *tool.Registry, opts Options, settings *config.Settings, skReg *skills.Registry, cmdExec *commands.Executor, log logger.Logger) (*toolbuiltin.TaskTool, error) {
	entry := effectiveEntryPoint(opts)
	tools := opts.Tools
	var taskTool *toolbuiltin.TaskTool

	if len(tools) == 0 {
		sandboxDisabled := settings != nil && settings.Sandbox != nil && settings.Sandbox.Enabled != nil && !*settings.Sandbox.Enabled
		if skReg == nil {
			skReg = skills.NewRegistry()
		}
		if cmdExec == nil {
			cmdExec = commands.NewExecutor()
		}

		factories := builtinToolFactories(opts.ProjectRoot, sandboxDisabled, entry, settings, skReg, cmdExec, opts.Sandbox.AllowedPaths)
		names := builtinOrder(entry)
		selectedNames := filterBuiltinNames(opts.EnabledBuiltinTools, names)
		for _, name := range selectedNames {
			ctor := factories[name]
			if ctor == nil {
				continue
			}
			impl := ctor()
			if impl == nil {
				continue
			}
			if t, ok := impl.(*toolbuiltin.TaskTool); ok {
				taskTool = t
			}
			tools = append(tools, impl)
		}

		if len(opts.CustomTools) > 0 {
			tools = append(tools, opts.CustomTools...)
		}
	} else {
		taskTool = locateTaskTool(tools)
	}

	disallowed := toLowerSet(opts.DisallowedTools)
	if settings != nil && len(settings.DisallowedTools) > 0 {
		if disallowed == nil {
			disallowed = map[string]struct{}{}
		}
		for _, name := range settings.DisallowedTools {
			if key := canonicalToolName(name); key != "" {
				disallowed[key] = struct{}{}
			}
		}
		if len(disallowed) == 0 {
			disallowed = nil
		}
	}

	seen := make(map[string]struct{})
	for _, impl := range tools {
		if impl == nil {
			continue
		}
		name := strings.TrimSpace(impl.Name())
		if name == "" {
			continue
		}
		canon := canonicalToolName(name)
		if disallowed != nil {
			if _, blocked := disallowed[canon]; blocked {
				log.Infof("tool %s skipped: disallowed", name)
				continue
			}
		}
		if _, ok := seen[canon]; ok {
			log.Infof("tool %s skipped: duplicate name", name)
			continue
		}
		seen[canon] = struct{}{}
		if err := registry.Register(impl); err != nil {
			return nil, fmt.Errorf("api: register tool %s: %w", impl.Name(), err)
		}
	}

	if taskTool == nil {
		taskTool = locateTaskTool(tools)
	}
	return taskTool, nil
}

func builtinToolFactories(root string, sandboxDisabled bool, entry EntryPoint, settings *config.Settings, skReg *skills.Registry, cmdExec *commands.Executor, allowedPaths []string) map[string]func() tool.Tool {
	factories := map[string]func() tool.Tool{}

	var (
		syncThresholdBytes  int
		asyncThresholdBytes int
	)
	if settings != nil && settings.BashOutput != nil {
		if settings.BashOutput.SyncThresholdBytes != nil {
			syncThresholdBytes = *settings.BashOutput.SyncThresholdBytes
		}
		if settings.BashOutput.AsyncThresholdBytes != nil {
			asyncThresholdBytes = *settings.BashOutput.AsyncThresholdBytes
		}
	}
	if asyncThresholdBytes > 0 {
		toolbuiltin.DefaultAsyncTaskManager().SetMaxOutputLen(asyncThresholdBytes)
	}

	sharedSandbox := (*security.Sandbox)(nil)
	if !sandboxDisabled {
		sharedSandbox = newBuiltinToolSandbox(root, settings, allowedPaths)
	}

	bashCtor := func() tool.Tool {
		var bash *toolbuiltin.BashTool
		if sandboxDisabled {
			bash = toolbuiltin.NewBashToolWithSandbox(root, security.NewDisabledSandbox())
		} else {
			bash = toolbuiltin.NewBashToolWithSandbox(root, sharedSandbox)
		}
		if syncThresholdBytes > 0 {
			bash.SetOutputThresholdBytes(syncThresholdBytes)
		}
		if entry == EntryPointCLI {
			bash.AllowShellMetachars(true)
		}
		return bash
	}

	readCtor := func() tool.Tool {
		if sandboxDisabled {
			return toolbuiltin.NewReadToolWithSandbox(root, security.NewDisabledSandbox())
		}
		return toolbuiltin.NewReadToolWithSandbox(root, sharedSandbox)
	}
	writeCtor := func() tool.Tool {
		if sandboxDisabled {
			return toolbuiltin.NewWriteToolWithSandbox(root, security.NewDisabledSandbox())
		}
		return toolbuiltin.NewWriteToolWithSandbox(root, sharedSandbox)
	}
	editCtor := func() tool.Tool {
		if sandboxDisabled {
			return toolbuiltin.NewEditToolWithSandbox(root, security.NewDisabledSandbox())
		}
		return toolbuiltin.NewEditToolWithSandbox(root, sharedSandbox)
	}

	respectGitignore := true
	if settings != nil && settings.RespectGitignore != nil {
		respectGitignore = *settings.RespectGitignore
	}
	grepCtor := func() tool.Tool {
		if sandboxDisabled {
			grep := toolbuiltin.NewGrepToolWithSandbox(root, security.NewDisabledSandbox())
			grep.SetRespectGitignore(respectGitignore)
			return grep
		}
		grep := toolbuiltin.NewGrepToolWithSandbox(root, sharedSandbox)
		grep.SetRespectGitignore(respectGitignore)
		return grep
	}
	globCtor := func() tool.Tool {
		if sandboxDisabled {
			glob := toolbuiltin.NewGlobToolWithSandbox(root, security.NewDisabledSandbox())
			glob.SetRespectGitignore(respectGitignore)
			return glob
		}
		glob := toolbuiltin.NewGlobToolWithSandbox(root, sharedSandbox)
		glob.SetRespectGitignore(respectGitignore)
		return glob
	}
	taskStore := tasks.NewTaskStore()

	factories["bash"] = bashCtor
	factories["file_read"] = readCtor
	factories["file_write"] = writeCtor
	factories["file_edit"] = editCtor
	factories["grep"] = grepCtor
	factories["glob"] = globCtor
	factories["web_fetch"] = func() tool.Tool { return toolbuiltin.NewWebFetchTool(nil) }
	factories["web_search"] = func() tool.Tool { return toolbuiltin.NewWebSearchTool(nil) }
	factories["bash_output"] = func() tool.Tool { return toolbuiltin.NewBashOutputTool(nil) }
	factories["bash_status"] = func() tool.Tool { return toolbuiltin.NewBashStatusTool() }
	factories["kill_task"] = func() tool.Tool { return toolbuiltin.NewKillTaskTool() }
	factories["task_create"] = func() tool.Tool { return toolbuiltin.NewTaskCreateTool(taskStore) }
	factories["task_list"] = func() tool.Tool { return toolbuiltin.NewTaskListTool(taskStore) }
	factories["task_get"] = func() tool.Tool { return toolbuiltin.NewTaskGetTool(taskStore) }
	factories["task_update"] = func() tool.Tool { return toolbuiltin.NewTaskUpdateTool(taskStore) }
	factories["ask_user_question"] = func() tool.Tool { return toolbuiltin.NewAskUserQuestionTool() }
	factories["send_file"] = func() tool.Tool { return toolbuiltin.NewSendFileTool() }
	factories["skill"] = func() tool.Tool { return toolbuiltin.NewSkillTool(skReg, nil) }
	factories["slash_command"] = func() tool.Tool { return toolbuiltin.NewSlashCommandTool(cmdExec) }
	factories["memorysearch"] = func() tool.Tool { return toolbuiltin.NewMemorySearchTool(root) }
	factories["memoryget"] = func() tool.Tool { return toolbuiltin.NewMemoryGetTool(root) }
	factories["memorywrite"] = func() tool.Tool { return toolbuiltin.NewMemoryWriteTool(root) }
	factories["listskills"] = func() tool.Tool { return toolbuiltin.NewListSkillsTool(root) }

	if shouldRegisterTaskTool(entry) {
		factories["task"] = func() tool.Tool { return toolbuiltin.NewTaskTool() }
	}

	return factories
}

func newBuiltinToolSandbox(root string, settings *config.Settings, allowedPaths []string) *security.Sandbox {
	sb := security.NewSandbox(root)
	add := func(path string) {
		clean := strings.TrimSpace(path)
		if clean == "" {
			return
		}
		if abs, err := filepath.Abs(clean); err == nil {
			clean = abs
		}
		sb.Allow(clean)
		if resolved, err := filepath.EvalSymlinks(clean); err == nil && strings.TrimSpace(resolved) != "" {
			sb.Allow(resolved)
		}
	}
	if settings != nil && settings.Permissions != nil {
		for _, p := range settings.Permissions.AdditionalDirectories {
			add(p)
		}
	}
	for _, p := range allowedPaths {
		add(p)
	}
	return sb
}

func builtinOrder(entry EntryPoint) []string {
	order := []string{
		"bash",
		"file_read",
		"file_write",
		"file_edit",
		"web_fetch",
		"web_search",
		"bash_output",
		"bash_status",
		"kill_task",
		"task_create",
		"task_list",
		"task_get",
		"task_update",
		"ask_user_question",
		"send_file",
		"skill",
		"slash_command",
		"grep",
		"glob",
		"memorysearch",
		"memoryget",
		"memorywrite",
		"listskills",
	}
	if shouldRegisterTaskTool(entry) {
		order = append(order, "task")
	}
	return order
}

func filterBuiltinNames(enabled []string, order []string) []string {
	if enabled == nil {
		return append([]string(nil), order...)
	}
	if len(enabled) == 0 {
		return nil
	}
	canon := func(name string) string {
		name = strings.ToLower(strings.TrimSpace(name))
		name = strings.NewReplacer("-", "", "_", "", " ", "").Replace(name)
		return name
	}
	set := make(map[string]struct{}, len(enabled))
	for _, name := range enabled {
		key := canon(name)
		if key != "" {
			set[key] = struct{}{}
		}
	}
	var filtered []string
	for _, name := range order {
		if _, ok := set[canon(name)]; ok {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func shouldRegisterTaskTool(entry EntryPoint) bool {
	switch entry {
	case EntryPointCLI, EntryPointPlatform:
		return true
	default:
		return false
	}
}

func locateTaskTool(tools []tool.Tool) *toolbuiltin.TaskTool {
	for _, impl := range tools {
		if impl == nil {
			continue
		}
		if task, ok := impl.(*toolbuiltin.TaskTool); ok {
			return task
		}
	}
	return nil
}

func effectiveEntryPoint(opts Options) EntryPoint {
	entry := opts.EntryPoint
	if entry == "" {
		entry = opts.Mode.EntryPoint
	}
	if entry == "" {
		entry = defaultEntrypoint
	}
	return entry
}

func registerMCPServers(ctx context.Context, registry *tool.Registry, manager *sandbox.Manager, servers []mcpServer) error {
	for _, server := range servers {
		spec := server.Spec
		if err := enforceSandboxHost(manager, spec); err != nil {
			return err
		}
		opts := tool.MCPServerOptions{Headers: server.Headers, Env: server.Env}
		if server.TimeoutSeconds > 0 {
			opts.Timeout = time.Duration(server.TimeoutSeconds) * time.Second
		}

		var err error
		if len(opts.Headers) == 0 && len(opts.Env) == 0 && opts.Timeout <= 0 {
			err = registry.RegisterMCPServer(ctx, spec, server.Name)
		} else {
			err = registry.RegisterMCPServerWithOptions(ctx, spec, server.Name, opts)
		}
		if err != nil {
			return fmt.Errorf("api: register MCP %s: %w", spec, err)
		}
	}
	return nil
}

func enforceSandboxHost(manager *sandbox.Manager, server string) error {
	if manager == nil || strings.TrimSpace(server) == "" {
		return nil
	}
	u, err := url.Parse(server)
	if err != nil || u == nil || strings.TrimSpace(u.Scheme) == "" {
		return nil
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	base, _, _ := strings.Cut(scheme, "+")
	switch base {
	case "http", "https", "sse":
		if err := manager.CheckNetwork(u.Host); err != nil {
			return fmt.Errorf("api: MCP host denied: %w", err)
		}
	}
	return nil
}

func resolveModel(ctx context.Context, opts Options) (model.Model, error) {
	if opts.Model != nil {
		return opts.Model, nil
	}
	if opts.ModelFactory != nil {
		mdl, err := opts.ModelFactory.Model(ctx)
		if err != nil {
			return nil, fmt.Errorf("api: model factory: %w", err)
		}
		return mdl, nil
	}
	return nil, ErrMissingModel
}

func defaultSessionID(entry EntryPoint) string {
	prefix := strings.TrimSpace(string(entry))
	if prefix == "" {
		prefix = string(defaultEntrypoint)
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// loadAttachmentAsBase64 loads a multimodal file and returns base64-encoded data.
func loadAttachmentAsBase64(filePath, mimeType, attachmentType string) (string, string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", fmt.Errorf("read file: %w", err)
	}

	attachmentType = strings.ToLower(strings.TrimSpace(attachmentType))
	if attachmentType == "" {
		attachmentType = "image"
	}

	// Auto-detect MIME type if not provided
	if mimeType == "" {
		mimeType = DetectAttachmentMIME(attachmentType, filePath)
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return encoded, mimeType, nil
}

func resolveAttachmentType(rawType, mimeType, filePath string) string {
	return DetectAttachmentType(rawType, mimeType, filePath)
}

type lastUsedModelReader interface {
	LastUsedModel() string
}

func usedModelFromBase(base model.Model) string {
	if base == nil {
		return ""
	}
	if reader, ok := base.(lastUsedModelReader); ok {
		return strings.TrimSpace(reader.LastUsedModel())
	}
	return ""
}

// buildSkillsSnippet returns structured available-skills metadata used for
// context reporting. It delegates to toolbuiltin.ScanSkillsList so frontmatter
// parsing is not duplicated.
func buildSkillsSnippet(projectRoot string) string {
	list := toolbuiltin.ScanSkillsList(projectRoot)
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<available_skills>")
	for _, s := range list {
		b.WriteString("\n  <skill>")
		b.WriteString("\n    <name>")
		b.WriteString(escapePromptTagValue(s.Name))
		b.WriteString("</name>")
		if s.Description != "" {
			b.WriteString("\n    <description>")
			b.WriteString(escapePromptTagValue(s.Description))
			b.WriteString("</description>")
		}
		if s.Location != "" {
			b.WriteString("\n    <location>")
			b.WriteString(escapePromptTagValue(s.Location))
			b.WriteString("</location>")
		}
		b.WriteString("\n  </skill>")
	}
	b.WriteString("\n</available_skills>")
	return b.String()
}

func buildSystemContextSnippet(projectRoot string, now time.Time, primaryModel string) string {
	var sections []string
	sections = append(sections, "<current_time>"+escapePromptTagValue(now.Format("2006-01-02 15:04 MST"))+"</current_time>")
	if modelID := strings.TrimSpace(primaryModel); modelID != "" {
		sections = append(sections, "<current_model>"+escapePromptTagValue(modelID)+"</current_model>")
	}
	if skillsSnippet := buildSkillsSnippet(projectRoot); skillsSnippet != "" {
		sections = append(sections, skillsSnippet)
	}
	return strings.Join(sections, "\n")
}

func buildAutoRecallFallbackQuery(raw string) string {
	q := strings.TrimSpace(raw)
	if q == "" {
		return ""
	}
	lower := strings.ToLower(q)
	hotwords := []string{
		"home", "workspace", "path", "directory", "root",
		"路径", "目录", "工作目录", "根目录", "哪里", "在哪",
	}
	matched := false
	for _, w := range hotwords {
		if strings.Contains(lower, w) || strings.Contains(q, w) {
			matched = true
			break
		}
	}
	if !matched {
		return ""
	}
	anchors := []string{
		"home directory", "home path", "workspace directory", "workspace path",
		"agent home", "working directory", "~/.aevitas", "~/.aevitas/workspace",
		"home 路径", "工作目录", "workspace 目录",
	}
	return q + " " + strings.Join(anchors, " ")
}

func buildAutoRecallInjectedBlock(results []toolbuiltin.MemoryChunk) string {
	var sb strings.Builder
	sb.WriteString("<relevant-memories>\n")
	sb.WriteString("The following memories may be relevant to this conversation:\n")
	for _, r := range results {
		sb.WriteString("- ")
		sb.WriteString(r.Snippet)
		sb.WriteByte('\n')
	}
	sb.WriteString("</relevant-memories>\n\n")
	return sb.String()
}

var promptTagEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

func escapePromptTagValue(value string) string {
	return promptTagEscaper.Replace(strings.TrimSpace(value))
}

func appendPlainTextForTTSRule(systemPrompt string) string {
	const rule = "Please reply in plain readable text for TTS, do not use Markdown/code blocks/tables/emoji."
	trimmed := strings.TrimSpace(systemPrompt)
	if strings.Contains(trimmed, rule) {
		return trimmed
	}
	if trimmed == "" {
		return rule
	}
	return trimmed + "\n\n" + rule
}
