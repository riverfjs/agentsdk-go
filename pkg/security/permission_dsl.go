package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type dslMatcherKind string

const (
	dslMatcherBash    dslMatcherKind = "bash_ast"
	dslMatcherPath    dslMatcherKind = "path_query"
	dslMatcherWeb     dslMatcherKind = "web_query"
	dslMatcherGeneric dslMatcherKind = "generic_query"
)

type dslRuleAST struct {
	Raw    string
	Effect PermissionAction
	Tool   string
	Expr   dslExprAST
}

type dslExprAST struct {
	Kind dslMatcherKind

	RawPattern string
	Domain     string

	BashCommand     string
	BashSubcommands []string
	BashFlags       []string
}

func parseDSLRule(line string) (*dslRuleAST, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil, errors.New("empty DSL rule")
	}
	parts := strings.Fields(trimmed)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid DSL rule %q: expected '<effect> <Tool> <expression>'", line)
	}

	effect := PermissionAction(strings.ToLower(parts[0]))
	if effect != PermissionAllow && effect != PermissionAsk && effect != PermissionDeny {
		return nil, fmt.Errorf("invalid DSL effect %q", parts[0])
	}

	toolName := strings.TrimSpace(parts[1])
	if toolName == "" {
		return nil, fmt.Errorf("invalid DSL tool %q", parts[1])
	}

	expr := strings.TrimSpace(strings.Join(parts[2:], " "))
	if expr == "" {
		return nil, fmt.Errorf("DSL rule %q has empty expression", line)
	}

	parsedExpr, err := parseDSLExpression(toolName, expr)
	if err != nil {
		return nil, err
	}

	return &dslRuleAST{
		Raw:    trimmed,
		Effect: effect,
		Tool:   toolName,
		Expr:   parsedExpr,
	}, nil
}

func parseDSLExpression(tool, expr string) (dslExprAST, error) {
	switch tool {
	case "Bash":
		return parseBashDSLExpression(expr)
	case "Read", "Write", "Edit", "Glob", "Grep":
		return dslExprAST{
			Kind:       dslMatcherPath,
			RawPattern: strings.TrimSpace(expr),
		}, nil
	case "WebFetch":
		raw := strings.TrimSpace(expr)
		if strings.HasPrefix(strings.ToLower(raw), "domain:") {
			domain := strings.TrimSpace(raw[len("domain:"):])
			if domain == "" {
				return dslExprAST{}, errors.New("empty web domain")
			}
			return dslExprAST{
				Kind:   dslMatcherWeb,
				Domain: strings.ToLower(domain),
			}, nil
		}
		return dslExprAST{
			Kind:       dslMatcherGeneric,
			RawPattern: raw,
		}, nil
	default:
		return dslExprAST{
			Kind:       dslMatcherGeneric,
			RawPattern: strings.TrimSpace(expr),
		}, nil
	}
}

func parseBashDSLExpression(expr string) (dslExprAST, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) == 0 {
		return dslExprAST{}, errors.New("empty bash expression")
	}
	out := dslExprAST{
		Kind:        dslMatcherBash,
		BashCommand: strings.ToLower(strings.TrimSpace(fields[0])),
	}
	if len(fields) >= 2 {
		for _, s := range strings.Split(fields[1], "|") {
			if v := strings.ToLower(strings.TrimSpace(s)); v != "" {
				out.BashSubcommands = append(out.BashSubcommands, v)
			}
		}
	}
	if len(fields) >= 3 {
		for _, s := range strings.Split(fields[2], "|") {
			if v := strings.TrimSpace(s); v != "" {
				out.BashFlags = append(out.BashFlags, v)
			}
		}
	}
	return out, nil
}

func compileDSLASTRule(rule *dslRuleAST) (*permissionRule, error) {
	if rule == nil {
		return nil, errors.New("nil DSL rule")
	}
	switch rule.Expr.Kind {
	case dslMatcherBash:
		match, err := compileBashASTMatcher(rule.Expr)
		if err != nil {
			return nil, err
		}
		return &permissionRule{
			raw:       rule.Raw,
			tool:      rule.Tool,
			toolMatch: func(name string) bool { return strings.EqualFold(rule.Tool, name) },
			match:     match,
		}, nil
	case dslMatcherPath:
		pattern, err := compilePattern(rule.Expr.RawPattern)
		if err != nil {
			return nil, err
		}
		return &permissionRule{
			raw:       rule.Raw,
			tool:      rule.Tool,
			toolMatch: func(name string) bool { return strings.EqualFold(rule.Tool, name) },
			match: func(target string) bool {
				return pattern(normalizePermissionPath(target))
			},
		}, nil
	case dslMatcherWeb:
		pattern, err := compilePattern(rule.Expr.Domain)
		if err != nil {
			return nil, err
		}
		return &permissionRule{
			raw:       rule.Raw,
			tool:      rule.Tool,
			toolMatch: func(name string) bool { return strings.EqualFold(rule.Tool, name) },
			match: func(target string) bool {
				return pattern(strings.ToLower(strings.TrimSpace(target)))
			},
		}, nil
	default:
		pattern, err := compilePattern(rule.Expr.RawPattern)
		if err != nil {
			return nil, err
		}
		return &permissionRule{
			raw:       rule.Raw,
			tool:      rule.Tool,
			toolMatch: func(name string) bool { return strings.EqualFold(rule.Tool, name) },
			match:     pattern,
		}, nil
	}
}

func compileBashASTMatcher(expr dslExprAST) (func(string) bool, error) {
	if expr.BashCommand == "" {
		return nil, errors.New("empty bash command")
	}
	return func(target string) bool {
		name, args, found := strings.Cut(target, ":")
		if !found {
			name, args = splitCommandNameArgs(target)
		}
		if strings.ToLower(strings.TrimSpace(name)) != expr.BashCommand {
			return false
		}
		argFields := strings.Fields(strings.TrimSpace(args))
		if len(expr.BashSubcommands) > 0 {
			if len(argFields) == 0 {
				return false
			}
			first := strings.ToLower(argFields[0])
			matched := false
			for _, s := range expr.BashSubcommands {
				if first == s {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
		if len(expr.BashFlags) > 0 {
			for _, f := range expr.BashFlags {
				for _, arg := range argFields {
					if arg == f {
						return true
					}
				}
			}
			return false
		}
		return true
	}, nil
}

func normalizePermissionPath(path string) string {
	expanded := expandTilde(path)
	if strings.TrimSpace(expanded) == "" {
		return ""
	}
	abs := expanded
	if !filepath.IsAbs(abs) {
		if v, err := filepath.Abs(abs); err == nil {
			abs = v
		}
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		abs = resolved
	}
	return filepath.Clean(abs)
}

func expandTilde(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
