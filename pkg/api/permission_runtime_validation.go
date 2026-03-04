package api

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/riverfjs/agentsdk-go/pkg/config"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

var permissionDSLToolNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

func validatePermissionDSLToolsAgainstRegistry(settings *config.Settings, registry *tool.Registry) error {
	if settings == nil || settings.Permissions == nil || len(settings.Permissions.DSL) == 0 || registry == nil {
		return nil
	}
	available := registry.Names()
	availableSet := make(map[string]struct{}, len(available))
	for _, name := range available {
		availableSet[name] = struct{}{}
	}
	for i, rule := range settings.Permissions.DSL {
		toolName, err := validatePermissionDSLRuleSyntax(i, rule)
		if err != nil {
			return err
		}
		if _, ok := availableSet[toolName]; !ok {
			return fmt.Errorf("permissions.dsl[%d]: tool %q is not registered; available tools: %s", i, toolName, strings.Join(available, ", "))
		}
	}
	return nil
}

func validatePermissionDSLRuleSyntax(index int, rule string) (string, error) {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" {
		return "", fmt.Errorf("permissions.dsl[%d]: rule is empty", index)
	}
	parts := strings.Fields(trimmed)
	if len(parts) < 3 {
		return "", fmt.Errorf("permissions.dsl[%d]: expected format '<effect> <Tool> <expression>', got %q", index, rule)
	}
	effect := strings.ToLower(parts[0])
	if effect != "allow" && effect != "ask" && effect != "deny" {
		return "", fmt.Errorf("permissions.dsl[%d]: unknown effect %q", index, parts[0])
	}
	toolName := strings.TrimSpace(parts[1])
	if toolName == "" || !permissionDSLToolNamePattern.MatchString(toolName) {
		return "", fmt.Errorf("permissions.dsl[%d]: invalid tool name %q", index, parts[1])
	}
	expr := strings.TrimSpace(strings.Join(parts[2:], " "))
	if expr == "" {
		return "", fmt.Errorf("permissions.dsl[%d]: expression is empty", index)
	}
	return toolName, nil
}

