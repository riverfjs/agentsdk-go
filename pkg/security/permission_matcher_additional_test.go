package security

import (
	"testing"

	"github.com/riverfjs/agentsdk-go/pkg/config"
)

func TestCompileDSLRuleValidation(t *testing.T) {
	if _, err := parseDSLRule("   "); err == nil {
		t.Fatal("expected error for empty rule")
	}
	if _, err := parseDSLRule("allow"); err == nil {
		t.Fatal("expected malformed DSL rule")
	}
	ast, err := parseDSLRule("allow Read **/*.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ast.Effect != PermissionAllow {
		t.Fatalf("unexpected effect: %v", ast.Effect)
	}
	rule, err := compileDSLASTRule(ast)
	if err != nil {
		t.Fatalf("compile ast failed: %v", err)
	}
	if !rule.match("/tmp/a.md") {
		t.Fatalf("expected matcher to hit markdown path")
	}
}

func TestParseDSLRuleAcceptsUnregisteredToolName(t *testing.T) {
	if _, err := parseDSLRule("allow FooTool anything"); err != nil {
		t.Fatalf("expected parser to accept tool token before runtime registry check, got %v", err)
	}
	if _, err := parseDSLRule("allow bash ls"); err != nil {
		t.Fatalf("expected parser to accept lowercase token before runtime registry check, got %v", err)
	}
}

func TestCompilePatternVariants(t *testing.T) {
	matcher, err := compilePattern("regex:^foo$")
	if err != nil {
		t.Fatalf("regex compile failed: %v", err)
	}
	if !matcher("foo") || matcher("bar") {
		t.Fatalf("regex matcher behaved unexpectedly")
	}
	if _, err := compilePattern(""); err == nil {
		t.Fatal("expected empty pattern error")
	}
	if _, err := compilePattern("regex:["); err == nil {
		t.Fatal("expected invalid regex error")
	}
}

func TestDeriveTargetCoverage(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		params map[string]any
		want   string
	}{
		{name: "bash with args", tool: "Bash", params: map[string]any{"command": "ls -la"}, want: "ls:-la"},
		{name: "bash no args", tool: "bash", params: map[string]any{"command": "ls"}, want: "ls:"},
		{name: "bash empty", tool: "bash", params: map[string]any{"command": "   "}, want: ""},
		{name: "read path", tool: "Read", params: map[string]any{"file_path": "tmp/file.txt"}, want: "tmp/file.txt"},
		{name: "taskget prefers id", tool: "TaskGet", params: map[string]any{"task_id": "task-123", "path": "/tmp/ignored"}, want: "task-123"},
		{name: "webfetch host", tool: "WebFetch", params: map[string]any{"url": "https://example.com/a"}, want: "example.com"},
		{name: "generic target key", tool: "Custom", params: map[string]any{"target": "/foo/bar"}, want: "/foo/bar"},
		{name: "first string fallback", tool: "Other", params: map[string]any{"misc": []byte(" hi ")}, want: "hi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveTarget(tt.tool, tt.params); got != tt.want {
				t.Fatalf("deriveTarget = %q, want %q", got, tt.want)
			}
		})
	}
}

type stringer struct{ v string }

func (s stringer) String() string { return s.v }

func TestFirstStringAndCoercion(t *testing.T) {
	params := map[string]any{
		"path":      stringer{v: " str "},
		"byte_path": []byte(" /tmp/bytes "),
		"other":     123,
	}
	if got := firstString(params, "missing", "byte_path"); got != "/tmp/bytes" {
		t.Fatalf("expected byte coercion, got %q", got)
	}
	if got := firstString(params, "path"); got != "str" {
		t.Fatalf("expected stringer value, got %q", got)
	}
	if got := firstString(params); got == "" {
		t.Fatal("expected fallback to first string-like value")
	}
	if got := coerceToString(123); got != "" {
		t.Fatalf("expected empty string for non-coercible type, got %q", got)
	}
	if got := firstString(nil); got != "" {
		t.Fatalf("expected empty string for nil params, got %q", got)
	}
}

func TestPermissionMatcherNilAndUnknown(t *testing.T) {
	if matcher, err := NewPermissionMatcher(nil); err != nil || matcher != nil {
		t.Fatalf("nil config should return nil matcher, got %+v err %v", matcher, err)
	}

	cfg := &config.PermissionsConfig{
		DSL:     []string{"allow Read **/*.txt"},
		Default: "deny",
	}
	matcher, err := NewPermissionMatcher(cfg)
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}
	unknown := matcher.Match("Write", map[string]any{"path": "/tmp/file.txt"})
	if unknown.Action != PermissionDeny || unknown.Tool != "Write" {
		t.Fatalf("expected deny default decision, got %+v", unknown)
	}

	badCfg := &config.PermissionsConfig{DSL: []string{"ask"}}
	if _, err := NewPermissionMatcher(badCfg); err == nil {
		t.Fatal("expected error for malformed dsl rule")
	}
}

func TestPermissionMatcherPriorityRespectsCase(t *testing.T) {
	cfg := &config.PermissionsConfig{
		DSL: []string{
			"allow Bash ls",
			"deny Bash rm",
		},
		Default: "deny",
	}
	matcher, err := NewPermissionMatcher(cfg)
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}
	decision := matcher.Match("bash", map[string]any{"command": "rm /tmp"})
	if decision.Action != PermissionDeny {
		t.Fatalf("expected deny despite case differences, got %+v", decision)
	}
	allow := matcher.Match("Bash", map[string]any{"command": "ls"})
	if allow.Action != PermissionAllow {
		t.Fatalf("expected allow, got %+v", allow)
	}
}

func TestPermissionMatcherTaskTools(t *testing.T) {
	cfg := &config.PermissionsConfig{
		DSL: []string{
			"deny TaskCreate task-create",
			"deny TaskGet task-get",
			"deny TaskUpdate task-update",
			"deny TaskList task-list",
		},
		Default: "allow",
	}
	matcher, err := NewPermissionMatcher(cfg)
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}

	tests := []struct {
		tool string
		id   string
		rule string
	}{
		{tool: "TaskCreate", id: "task-create", rule: "TaskCreate(task-create)"},
		{tool: "TaskGet", id: "task-get", rule: "TaskGet(task-get)"},
		{tool: "TaskUpdate", id: "task-update", rule: "TaskUpdate(task-update)"},
		{tool: "TaskList", id: "task-list", rule: "TaskList(task-list)"},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			decision := matcher.Match(tt.tool, map[string]any{"task_id": tt.id, "path": "/tmp/ignored"})
			if decision.Action != PermissionDeny || decision.Target != tt.id {
				t.Fatalf("unexpected decision: %+v", decision)
			}
		})
	}
}
