package views

import (
	"reflect"
	"testing"
)

func TestMatchSlashCommands_PrefixCaseInsensitive(t *testing.T) {
	got := MatchSlashCommands("c")
	// "clear", "color", and "compact" all start with c, sorted alphabetically.
	names := make([]string, 0, len(got))
	for _, c := range got {
		names = append(names, c.Name)
	}
	want := []string{"clear", "color", "compact"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v want %v", names, want)
	}
}

func TestMatchSlashCommands_UpperCasePrefix(t *testing.T) {
	got := MatchSlashCommands("MO")
	if len(got) != 1 || got[0].Name != "model" {
		t.Fatalf("got %+v want one match for 'model'", got)
	}
}

func TestMatchSlashCommands_EmptyPrefixReturnsAll(t *testing.T) {
	got := MatchSlashCommands("")
	if len(got) != len(SlashCommands) {
		t.Fatalf("empty prefix returned %d, want all %d", len(got), len(SlashCommands))
	}
}

func TestMatchSlashCommands_NoMatch(t *testing.T) {
	if got := MatchSlashCommands("zzz"); len(got) != 0 {
		t.Fatalf("expected zero matches, got %v", got)
	}
}

func TestMatchSlashArg_ModelAll(t *testing.T) {
	got := MatchSlashArg("model", "")
	if len(got) == 0 {
		t.Fatal("expected some args for /model")
	}
	// We expect both short aliases and full IDs.
	hasOpus, hasFull := false, false
	for _, a := range got {
		if a == "opus" {
			hasOpus = true
		}
		if a == "claude-opus-4-7" {
			hasFull = true
		}
	}
	if !hasOpus || !hasFull {
		t.Fatalf("missing expected args: %v", got)
	}
}

func TestMatchSlashArg_PrefixSonnet(t *testing.T) {
	got := MatchSlashArg("model", "so")
	if len(got) != 1 || got[0] != "sonnet" {
		t.Fatalf("got %v want [sonnet]", got)
	}
}

func TestMatchSlashArg_PrefixClaude(t *testing.T) {
	got := MatchSlashArg("model", "claude-")
	// Three full IDs all start with 'claude-'.
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 claude-* matches", got)
	}
}

func TestMatchSlashArg_UnknownCommand(t *testing.T) {
	if got := MatchSlashArg("nonesuch", ""); got != nil {
		t.Fatalf("expected nil for unknown command, got %v", got)
	}
}

func TestMatchSlashArg_NoArgsCommand(t *testing.T) {
	if got := MatchSlashArg("clear", ""); got != nil {
		t.Fatalf("expected nil for arg-less /clear, got %v", got)
	}
}

func TestFindSlashCommand_Found(t *testing.T) {
	c := FindSlashCommand("model")
	if c == nil || c.Name != "model" {
		t.Fatalf("expected to find /model, got %+v", c)
	}
}

func TestFindSlashCommand_CaseInsensitive(t *testing.T) {
	c := FindSlashCommand("MoDeL")
	if c == nil || c.Name != "model" {
		t.Fatalf("expected case-insensitive lookup, got %+v", c)
	}
}

func TestFindSlashCommand_NotFound(t *testing.T) {
	if c := FindSlashCommand("zzz"); c != nil {
		t.Fatalf("expected nil, got %+v", c)
	}
}
