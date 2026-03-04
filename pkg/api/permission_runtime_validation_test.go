package api

import (
	"strings"
	"testing"

	"github.com/riverfjs/agentsdk-go/pkg/config"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

func TestValidatePermissionDSLRuleSyntax(t *testing.T) {
	tests := []struct {
		name    string
		rule    string
		wantErr string
	}{
		{name: "valid", rule: "allow Bash ls"},
		{name: "empty", rule: "   ", wantErr: "rule is empty"},
		{name: "short", rule: "allow Bash", wantErr: "expected format"},
		{name: "bad effect", rule: "maybe Bash ls", wantErr: "unknown effect"},
		{name: "bad tool token", rule: "allow Bash! ls", wantErr: "invalid tool name"},
		{name: "empty expr", rule: "allow Bash   ", wantErr: "expected format"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolName, err := validatePermissionDSLRuleSyntax(i, tt.rule)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if strings.TrimSpace(toolName) == "" {
					t.Fatal("expected non-empty tool name")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidatePermissionDSLToolsAgainstRegistry_StrictAndExists(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(&namedTool{name: "Bash"}); err != nil {
		t.Fatalf("register Bash: %v", err)
	}
	if err := reg.Register(&namedTool{name: "Read"}); err != nil {
		t.Fatalf("register Read: %v", err)
	}

	settings := &config.Settings{
		Permissions: &config.PermissionsConfig{
			DSL: []string{
				"allow Bash ls",
				"deny Read **/*.secret",
			},
		},
	}
	if err := validatePermissionDSLToolsAgainstRegistry(settings, reg); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// lowercase `bash` should fail strict registered-name check.
	settings.Permissions.DSL = []string{"allow bash ls"}
	err := validatePermissionDSLToolsAgainstRegistry(settings, reg)
	if err == nil {
		t.Fatal("expected strict tool name check to fail")
	}
	if !strings.Contains(err.Error(), `tool "bash" is not registered`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePermissionDSLToolsAgainstRegistry_ErrorIndex(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(&namedTool{name: "Bash"}); err != nil {
		t.Fatalf("register Bash: %v", err)
	}
	settings := &config.Settings{
		Permissions: &config.PermissionsConfig{
			DSL: []string{
				"allow Bash ls",
				"maybe Bash ls",
			},
		},
	}
	err := validatePermissionDSLToolsAgainstRegistry(settings, reg)
	if err == nil {
		t.Fatal("expected validation error on second rule")
	}
	if !strings.Contains(err.Error(), "permissions.dsl[1]") {
		t.Fatalf("expected index [1] in error, got %v", err)
	}
}

func TestValidatePermissionDSLToolsAgainstRegistry_NilAndEmptyInputs(t *testing.T) {
	reg := tool.NewRegistry()

	if err := validatePermissionDSLToolsAgainstRegistry(nil, reg); err != nil {
		t.Fatalf("nil settings should be ignored: %v", err)
	}
	if err := validatePermissionDSLToolsAgainstRegistry(&config.Settings{}, reg); err != nil {
		t.Fatalf("missing permissions should be ignored: %v", err)
	}
	if err := validatePermissionDSLToolsAgainstRegistry(&config.Settings{
		Permissions: &config.PermissionsConfig{DSL: nil},
	}, reg); err != nil {
		t.Fatalf("empty dsl should be ignored: %v", err)
	}
	if err := validatePermissionDSLToolsAgainstRegistry(&config.Settings{
		Permissions: &config.PermissionsConfig{DSL: []string{"allow Bash ls"}},
	}, nil); err != nil {
		t.Fatalf("nil registry should be ignored: %v", err)
	}
}

