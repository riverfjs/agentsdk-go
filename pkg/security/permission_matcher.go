package security

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/riverfjs/agentsdk-go/pkg/config"
)

// PermissionAction represents the enforcement outcome for a tool invocation.
type PermissionAction string

const (
	PermissionUnknown PermissionAction = "unknown"
	PermissionAllow   PermissionAction = "allow"
	PermissionAsk     PermissionAction = "ask"
	PermissionDeny    PermissionAction = "deny"
)

// PermissionDecision captures the matched rule and derived target string.
type PermissionDecision struct {
	Action PermissionAction
	Rule   string
	Tool   string
	Target string
}

// PermissionAudit records executed decisions for later inspection.
type PermissionAudit struct {
	Tool      string
	Target    string
	Rule      string
	Action    PermissionAction
	Timestamp time.Time
}

// PermissionMatcher evaluates tool calls against allow/ask/deny rules.
type PermissionMatcher struct {
	allow []*permissionRule
	ask   []*permissionRule
	deny  []*permissionRule
	def   PermissionAction
}

type permissionRule struct {
	raw       string
	tool      string
	toolMatch func(string) bool
	match     func(string) bool
}

// NewPermissionMatcher builds a matcher from the provided permissions config.
// A nil config yields a nil matcher and no error.
func NewPermissionMatcher(cfg *config.PermissionsConfig) (*PermissionMatcher, error) {
	if cfg == nil {
		return nil, nil
	}
	if len(cfg.Allow) > 0 || len(cfg.Ask) > 0 || len(cfg.Deny) > 0 {
		return nil, errors.New("permissions.allow/ask/deny are no longer supported; use permissions.dsl")
	}
	if len(cfg.DSL) == 0 {
		return nil, nil
	}

	def := PermissionDeny
	if raw := strings.ToLower(strings.TrimSpace(cfg.Default)); raw != "" {
		switch raw {
		case string(PermissionAllow):
			def = PermissionAllow
		case string(PermissionAsk):
			def = PermissionAsk
		case string(PermissionDeny):
			def = PermissionDeny
		default:
			return nil, fmt.Errorf("unsupported permissions.default %q", cfg.Default)
		}
	}

	m := &PermissionMatcher{def: def}
	for _, line := range cfg.DSL {
		ast, err := parseDSLRule(line)
		if err != nil {
			return nil, err
		}
		rule, err := compileDSLASTRule(ast)
		if err != nil {
			return nil, err
		}
		switch ast.Effect {
		case PermissionAllow:
			m.allow = append(m.allow, rule)
		case PermissionAsk:
			m.ask = append(m.ask, rule)
		case PermissionDeny:
			m.deny = append(m.deny, rule)
		}
	}
	sort.SliceStable(m.allow, func(i, j int) bool { return m.allow[i].raw < m.allow[j].raw })
	sort.SliceStable(m.ask, func(i, j int) bool { return m.ask[i].raw < m.ask[j].raw })
	sort.SliceStable(m.deny, func(i, j int) bool { return m.deny[i].raw < m.deny[j].raw })
	return m, nil
}

// Match resolves the decision for a tool invocation. Priority: deny > ask > allow.
func (m *PermissionMatcher) Match(toolName string, params map[string]any) PermissionDecision {
	if m == nil {
		return PermissionDecision{Action: PermissionAllow, Tool: toolName}
	}

	tool := strings.TrimSpace(toolName)
	target := deriveTarget(tool, params)

	if decision, ok := m.matchRules(tool, target, m.deny, PermissionDeny); ok {
		return decision
	}
	if decision, ok := m.matchRules(tool, target, m.ask, PermissionAsk); ok {
		return decision
	}
	if decision, ok := m.matchRules(tool, target, m.allow, PermissionAllow); ok {
		return decision
	}
	if m.def == "" {
		return PermissionDecision{Action: PermissionUnknown, Tool: tool, Target: target}
	}
	return PermissionDecision{Action: m.def, Tool: tool, Target: target}
}

func (m *PermissionMatcher) matchRules(tool, target string, rules []*permissionRule, action PermissionAction) (PermissionDecision, bool) {
	for _, rule := range rules {
		if rule.toolMatch != nil {
			if !rule.toolMatch(tool) {
				continue
			}
		} else if !strings.EqualFold(rule.tool, tool) {
			continue
		}
		if rule.match(target) {
			return PermissionDecision{Action: action, Rule: rule.raw, Tool: tool, Target: target}, true
		}
	}
	return PermissionDecision{}, false
}

func compilePattern(pattern string) (func(string) bool, error) {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return nil, errors.New("empty permission pattern")
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "regex:") || strings.HasPrefix(lower, "regexp:") {
		expr := strings.TrimSpace(trimmed[strings.Index(trimmed, ":")+1:])
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, err
		}
		return re.MatchString, nil
	}

	regex := globToRegex(trimmed)
	re, err := regexp.Compile("^" + regex + "$")
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}

func globToRegex(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString(".*")
			}
		case '?':
			b.WriteString(".")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteString("\\")
			b.WriteByte(glob[i])
		default:
			b.WriteByte(glob[i])
		}
	}
	return b.String()
}

func deriveTarget(tool string, params map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash":
		cmd := firstString(params, "command")
		name, args := splitCommandNameArgs(cmd)
		if name == "" {
			return strings.TrimSpace(cmd)
		}
		if args == "" {
			return name + ":"
		}
		return name + ":" + args
	case "read", "write", "edit":
		if p := firstString(params, "file_path", "path"); p != "" {
			return filepath.Clean(p)
		}
	case "taskcreate", "taskget", "taskupdate", "tasklist":
		if id := firstString(params, "task_id", "id"); id != "" {
			return id
		}
	case "webfetch":
		u := firstString(params, "url")
		if u == "" {
			return ""
		}
		parsed, err := url.Parse(u)
		if err != nil {
			return strings.ToLower(strings.TrimSpace(u))
		}
		return strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	case "websearch":
		if q := firstString(params, "search_term", "query"); q != "" {
			return strings.ToLower(strings.TrimSpace(q))
		}
	}
	if p := firstString(params, "path", "file", "target"); p != "" {
		return filepath.Clean(p)
	}
	return firstString(params)
}

func firstString(params map[string]any, keys ...string) string {
	if params == nil {
		return ""
	}
	if len(keys) == 0 {
		for _, v := range params {
			if s := coerceToString(v); s != "" {
				return s
			}
		}
		return ""
	}
	for _, key := range keys {
		if v, ok := params[key]; ok {
			if s := coerceToString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func coerceToString(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case []byte:
		return strings.TrimSpace(string(val))
	case fmt.Stringer:
		return strings.TrimSpace(val.String())
	default:
		return ""
	}
}

func splitCommandNameArgs(cmd string) (string, string) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return "", ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", ""
	}
	name := fields[0]
	if len(fields) == 1 {
		return name, ""
	}
	return name, strings.Join(fields[1:], " ")
}
