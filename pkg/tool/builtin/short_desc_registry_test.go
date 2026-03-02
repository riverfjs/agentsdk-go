package toolbuiltin

import "testing"

func TestLookupShortToolDescRegistered(t *testing.T) {
	t.Parallel()
	desc, ok := LookupShortToolDesc("bash")
	if !ok {
		t.Fatal("expected bash short description to be registered")
	}
	if desc == "" {
		t.Fatal("expected non-empty short description")
	}
}

func TestLookupShortToolDescCaseInsensitive(t *testing.T) {
	t.Parallel()
	a, okA := LookupShortToolDesc("BASH")
	b, okB := LookupShortToolDesc("bash")
	if !okA || !okB {
		t.Fatal("expected both lookups to succeed")
	}
	if a != b {
		t.Fatalf("expected same description, got %q vs %q", a, b)
	}
}

func TestLookupShortToolDescMissing(t *testing.T) {
	t.Parallel()
	if _, ok := LookupShortToolDesc("not_exists_tool_xyz"); ok {
		t.Fatal("expected missing tool lookup to fail")
	}
}

func TestLookupShortToolDescSeparatorInsensitive(t *testing.T) {
	t.Parallel()
	registerShortToolDesc("ask_user_question", "Collect structured choices from the user.")
	a, okA := LookupShortToolDesc("AskUserQuestion")
	b, okB := LookupShortToolDesc("ask-user-question")
	if !okA || !okB {
		t.Fatal("expected separator-insensitive lookups to succeed")
	}
	if a != b {
		t.Fatalf("expected same description, got %q vs %q", a, b)
	}
}
