package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/agentsdk-go/pkg/sandbox"
	"github.com/riverfjs/agentsdk-go/pkg/security"
)

// Executor wires tool registry lookup with sandbox enforcement.
// A nil sandbox manager disables enforcement.
type Executor struct {
	registry  *Registry
	sandbox   *sandbox.Manager
	persister *OutputPersister
	permCheck PermissionResolver
	logger    logger.Logger
}

// NewExecutor constructs an executor backed by the provided registry. When
// registry is nil a fresh Registry is created so callers never receive a nil
// executor by accident.
func NewExecutor(registry *Registry, sb *sandbox.Manager) *Executor {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Executor{
		registry: registry,
		sandbox:  sb,
		logger:   logger.NewDefault(), // 使用默认 logger
	}
}

// Registry exposes the underlying registry primarily for tests.
func (e *Executor) Registry() *Registry { return e.registry }

// Execute runs a single tool call. Parameters are shallow-cloned before being
// handed over to the tool to avoid concurrent callers mutating shared maps.
func (e *Executor) Execute(ctx context.Context, call Call) (*CallResult, error) {
	if e == nil || e.registry == nil {
		return nil, errors.New("executor is not initialised")
	}
	if strings.TrimSpace(call.Name) == "" {
		return nil, errors.New("tool name is empty")
	}

	if e.sandbox != nil {
		decision, err := e.sandbox.CheckToolPermission(call.Name, call.Params)
		if err != nil {
			return nil, err
		}
		decision, err = e.resolvePermission(ctx, call, decision)
		if err != nil {
			return nil, err
		}
		switch decision.Action {
		case security.PermissionDeny:
			if e.logger != nil {
				e.logger.Warnf("[tool] %s ✗ denied by rule %q for %s", call.Name, decision.Rule, decision.Target)
			}
			return nil, fmt.Errorf("tool %s denied by rule %q for %s", call.Name, decision.Rule, decision.Target)
		case security.PermissionAsk:
			if e.logger != nil {
				e.logger.Warnf("[tool] %s ? requires approval (rule %q for %s)", call.Name, decision.Rule, decision.Target)
			}
			return nil, fmt.Errorf("tool %s requires approval (rule %q for %s)", call.Name, decision.Rule, decision.Target)
		}

		if err := e.sandbox.Enforce(call.Path, call.Host, call.Usage); err != nil {
			return nil, err
		}
	}

	tool, err := e.registry.Get(call.Name)
	if err != nil {
		return nil, err
	}

	params := call.cloneParams()
	started := time.Now()
	
	// Log tool execution start with key parameters (lightweight formatting)
	if e.logger != nil {
		// For Bash/command tools, show the command; otherwise show concise params
		paramStr := ""
		if cmd, ok := params["command"].(string); ok {
			paramStr = cmd
		} else {
			paramStr = fmt.Sprintf("%v", params)
		}
		if len(paramStr) > 200 {
			paramStr = paramStr[:200] + "..."
		}
		e.logger.Infof("[tool] %s(%s)", call.Name, paramStr)
	}
	
	var (
		res     *ToolResult
		execErr error
	)
	if streamingTool, ok := tool.(StreamingTool); ok && call.StreamSink != nil {
		res, execErr = streamingTool.StreamExecute(ctx, params, call.StreamSink)
	} else {
		res, execErr = tool.Execute(ctx, params)
	}
	
	// Log tool execution result
	if e.logger != nil {
		if execErr != nil {
			e.logger.Warnf("[tool] %s ✗ %v", call.Name, execErr)
		} else if res != nil && res.Output != "" {
			output := strings.ReplaceAll(res.Output, "\n", " ")
			if len(output) > 1000 {
				output = output[:1000] + "..."
			}
			e.logger.Infof("[tool] %s ✓ %s", call.Name, output)
		}
	}
	
	if e.persister != nil && res != nil {
		// MaybePersist errors are logged internally; ignore return value
		e.persister.MaybePersist(call, res) //nolint:errcheck
	}
	cr := &CallResult{
		Call:        call,
		Result:      res,
		Err:         execErr,
		StartedAt:   started,
		CompletedAt: time.Now(),
	}
	return cr, execErr
}

// truncateParams truncates parameter map for logging
func truncateParams(params map[string]interface{}, maxLen int) string {
	if len(params) == 0 {
		return "{}"
	}
	str := fmt.Sprintf("%v", params)
	return truncateString(str, maxLen)
}

// truncateString truncates a string for logging
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ExecuteAll runs the provided calls concurrently and preserves ordering in the
// returned slice. Each call is isolated with its own parameter copy. Execution
// stops early when the context is cancelled; tools observe ctx directly.
func (e *Executor) ExecuteAll(ctx context.Context, calls []Call) []CallResult {
	results := make([]CallResult, len(calls))
	var wg sync.WaitGroup
	wg.Add(len(calls))

	for i := range calls {
		call := calls[i]
		go func(idx int) {
			defer wg.Done()
			if ctx != nil && ctx.Err() != nil {
				results[idx] = CallResult{Call: call, Err: ctx.Err()}
				return
			}
			cr, err := e.Execute(ctx, call)
			if cr != nil {
				results[idx] = *cr
				return
			}
			// When executor is nil, propagate error without result payload.
			results[idx] = CallResult{Call: call, Err: err}
		}(i)
	}

	wg.Wait()
	return results
}

// WithSandbox returns a shallow copy using the provided sandbox manager.
func (e *Executor) WithSandbox(sb *sandbox.Manager) *Executor {
	if e == nil {
		return NewExecutor(nil, sb)
	}
	clone := *e
	clone.sandbox = sb
	return &clone
}

// PermissionResolver allows callers to approve or deny sandbox PermissionAsk
// outcomes (for example via a host UI). Returning PermissionAsk keeps the
// request pending.
type PermissionResolver func(context.Context, Call, security.PermissionDecision) (security.PermissionDecision, error)

// WithPermissionResolver returns a shallow copy using the provided resolver.
func (e *Executor) WithPermissionResolver(resolver PermissionResolver) *Executor {
	if e == nil {
		exec := NewExecutor(nil, nil)
		exec.permCheck = resolver
		return exec
	}
	clone := *e
	clone.permCheck = resolver
	return &clone
}

// WithOutputPersister returns a shallow copy using the provided persister.
func (e *Executor) WithOutputPersister(persister *OutputPersister) *Executor {
	if e == nil {
		exec := NewExecutor(nil, nil)
		exec.persister = persister
		return exec
	}
	clone := *e
	clone.persister = persister
	return &clone
}

// WithLogger returns a shallow copy using the provided logger.
func (e *Executor) WithLogger(log logger.Logger) *Executor {
	if e == nil {
		exec := NewExecutor(nil, nil)
		exec.logger = log
		return exec
	}
	clone := *e
	clone.logger = log
	return &clone
}

func (e *Executor) resolvePermission(ctx context.Context, call Call, decision security.PermissionDecision) (security.PermissionDecision, error) {
	if decision.Action != security.PermissionAsk || e == nil || e.permCheck == nil {
		return decision, nil
	}
	resolved, err := e.permCheck(ctx, call, decision)
	if err != nil {
		return decision, err
	}
	if resolved.Rule == "" {
		resolved.Rule = decision.Rule
	}
	if resolved.Target == "" {
		resolved.Target = decision.Target
	}
	if resolved.Action == security.PermissionUnknown {
		resolved.Action = security.PermissionAsk
	}
	return resolved, nil
}
