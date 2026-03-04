package security

import (
	"testing"

	"github.com/riverfjs/agentsdk-go/pkg/config"
)

func TestPermissionMatcherPriority(t *testing.T) {
	cfg := &config.PermissionsConfig{
		DSL: []string{
			"allow Read **/*.md",
			"ask Read **/draft.md",
			"deny Read **/secret.md",
		},
		Default: "deny",
	}
	matcher, err := NewPermissionMatcher(cfg)
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}

	alwaysAllowed := matcher.Match("Read", map[string]any{"file_path": "/work/notes/readme.md"})
	if alwaysAllowed.Action != PermissionAllow {
		t.Fatalf("expected allow, got %v", alwaysAllowed.Action)
	}

	ask := matcher.Match("Read", map[string]any{"file_path": "/work/drafts/draft.md"})
	if ask.Action != PermissionAsk || ask.Rule != "ask Read **/draft.md" {
		t.Fatalf("expected ask, got %+v", ask)
	}

	deny := matcher.Match("Read", map[string]any{"file_path": "/work/private/secret.md"})
	if deny.Action != PermissionDeny || deny.Rule != "deny Read **/secret.md" {
		t.Fatalf("expected deny, got %+v", deny)
	}
}

func TestPermissionMatcherDefaultAction(t *testing.T) {
	cfg := &config.PermissionsConfig{
		DSL:     []string{"allow Read **/*.go"},
		Default: "ask",
	}
	matcher, err := NewPermissionMatcher(cfg)
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}
	decision := matcher.Match("Write", map[string]any{"path": "/tmp/a.txt"})
	if decision.Action != PermissionAsk {
		t.Fatalf("expected ask default, got %+v", decision)
	}
}

func TestPermissionMatcherBashGitSubcommands(t *testing.T) {
	cfg := &config.PermissionsConfig{
		DSL: []string{
			"deny Bash git commit|push|rebase",
			"allow Bash git status|diff|log",
		},
		Default: "deny",
	}
	matcher, err := NewPermissionMatcher(cfg)
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}

	denyCases := []string{"git commit -m x", "git push origin main", "git rebase -i HEAD~2"}
	for _, cmd := range denyCases {
		decision := matcher.Match("Bash", map[string]any{"command": cmd})
		if decision.Action != PermissionDeny {
			t.Fatalf("expected deny for %q, got %+v", cmd, decision)
		}
	}

	allowCases := []string{"git status", "git diff", "git log --oneline"}
	for _, cmd := range allowCases {
		decision := matcher.Match("Bash", map[string]any{"command": cmd})
		if decision.Action != PermissionAllow {
			t.Fatalf("expected allow for %q, got %+v", cmd, decision)
		}
	}
}

func TestPermissionMatcherLegacyRulesRejected(t *testing.T) {
	cfg := &config.PermissionsConfig{
		Allow: []string{"Read(**/*.md)"},
	}
	_, err := NewPermissionMatcher(cfg)
	if err == nil {
		t.Fatalf("expected legacy rules to be rejected")
	}
}
