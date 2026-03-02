package api

import (
	"errors"
	"strings"
	"unicode"
)

const (
	defaultPolicyRefusalMessage = "Sorry, I can't process that request."
)

// ErrPromptPolicyViolation is the sentinel error for prompt-policy blocks.
var ErrPromptPolicyViolation = errors.New("api: prompt policy violation")

type promptPolicyViolationError struct {
	message string
}

func (e *promptPolicyViolationError) Error() string {
	if e == nil || strings.TrimSpace(e.message) == "" {
		return defaultPolicyRefusalMessage
	}
	return e.message
}

func (e *promptPolicyViolationError) Unwrap() error {
	return ErrPromptPolicyViolation
}

func detectPromptDisclosureRequest(prompt string) bool {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" {
		return false
	}
	targetTerms := []string{
		"system prompt", "system message", "developer prompt", "developer message",
		"hidden instruction", "hidden prompt", "internal policy",
		"message systeme", "message système", "invite systeme", "invite système",
		"系统提示词", "系统提示", "系统消息", "开发者提示词", "开发者消息", "隐藏指令", "内部规则",
		"agents.md", "soul.md",
	}
	actionTerms := []string{
		"reveal", "show", "print", "dump", "expose", "verbatim", "exact", "full text",
		"translate", "summarize", "repeat", "send me",
		"traduire", "résumer", "envoyer",
		"透露", "展示", "输出", "复述", "翻译", "发送给我", "原文", "逐字", "完整内容",
	}
	if containsAny(text, targetTerms) && containsAny(text, actionTerms) {
		return true
	}
	if strings.Contains(text, "ignore previous instructions") && containsAny(text, targetTerms) {
		return true
	}
	return false
}

func redactAssistantDisclosure(output, systemPrompt string) (string, bool, string) {
	cleanOutput := strings.TrimSpace(output)
	if cleanOutput == "" {
		return output, false, ""
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return output, false, ""
	}
	if hasSystemPromptLineOverlap(cleanOutput, systemPrompt) {
		return defaultPolicyRefusalMessage, true, "line_overlap"
	}
	if hasPromptFingerprintLeak(cleanOutput, systemPrompt) {
		return defaultPolicyRefusalMessage, true, "fingerprint"
	}
	return output, false, ""
}

func hasSystemPromptLineOverlap(output, systemPrompt string) bool {
	for _, line := range strings.Split(systemPrompt, "\n") {
		trimmed := strings.TrimSpace(line)
		if len([]rune(trimmed)) < 24 {
			continue
		}
		if strings.Contains(output, trimmed) {
			return true
		}
	}
	return false
}

func hasPromptFingerprintLeak(output, systemPrompt string) bool {
	outNorm := normalizeForFingerprint(output)
	sysNorm := normalizeForFingerprint(systemPrompt)
	if len(outNorm) < 40 || len(sysNorm) < 80 {
		return false
	}
	const n = 10
	outGrams := charNgrams(outNorm, n)
	sysGrams := charNgrams(sysNorm, n)
	if len(outGrams) == 0 || len(sysGrams) == 0 {
		return false
	}
	overlap := 0
	for gram := range outGrams {
		if _, ok := sysGrams[gram]; ok {
			overlap++
		}
	}
	ratio := float64(overlap) / float64(min(len(outGrams), len(sysGrams)))
	return overlap >= 12 && ratio >= 0.12
}

func normalizeForFingerprint(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func charNgrams(s string, n int) map[string]struct{} {
	rs := []rune(s)
	if n <= 0 || len(rs) < n {
		return nil
	}
	out := make(map[string]struct{}, len(rs)-n+1)
	for i := 0; i+n <= len(rs); i++ {
		out[string(rs[i:i+n])] = struct{}{}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (rt *Runtime) promptGuardEnabled() bool {
	if rt == nil {
		return false
	}
	if rt.opts.PromptGuardEnabled == nil {
		return true
	}
	return *rt.opts.PromptGuardEnabled
}

func (rt *Runtime) outputGuardEnabled() bool {
	if rt == nil {
		return false
	}
	if rt.opts.OutputGuardEnabled == nil {
		return true
	}
	return *rt.opts.OutputGuardEnabled
}

func policyRefusalMessage() string {
	return defaultPolicyRefusalMessage
}

// PromptPolicyRefusalMessage returns the canonical guard refusal text.
func PromptPolicyRefusalMessage() string {
	return defaultPolicyRefusalMessage
}

// IsPromptPolicyViolation reports whether err represents a prompt-policy block.
func IsPromptPolicyViolation(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrPromptPolicyViolation)
}

func containsAny(text string, terms []string) bool {
	for _, term := range terms {
		if term != "" && strings.Contains(text, term) {
			return true
		}
	}
	return false
}
