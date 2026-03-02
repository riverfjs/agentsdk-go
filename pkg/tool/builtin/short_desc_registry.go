package toolbuiltin

import (
	"strings"
	"sync"
	"unicode"
)

var (
	shortToolDescMu sync.RWMutex
	shortToolDescs  = map[string]string{}
)

func normalizeToolName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func registerShortToolDesc(name, shortToolDesc string) {
	key := normalizeToolName(name)
	val := strings.TrimSpace(shortToolDesc)
	if key == "" || val == "" {
		return
	}
	shortToolDescMu.Lock()
	shortToolDescs[key] = val
	shortToolDescMu.Unlock()
}

// LookupShortToolDesc returns the model-facing short description for a tool.
func LookupShortToolDesc(name string) (string, bool) {
	key := normalizeToolName(name)
	if key == "" {
		return "", false
	}
	shortToolDescMu.RLock()
	val, ok := shortToolDescs[key]
	shortToolDescMu.RUnlock()
	return val, ok
}
