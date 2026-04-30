package views

import (
	"reflect"
	"strings"
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

func TestRenderSlashPopup_EmptyReturnsBlank(t *testing.T) {
	if got := RenderSlashPopup(nil, 0, 80); got != "" {
		t.Fatalf("expected empty string for nil matches, got %q", got)
	}
}

func TestRenderSlashPopup_RendersAllNames(t *testing.T) {
	matches := MatchSlashCommands("c") // clear, color, compact
	out := RenderSlashPopup(matches, 0, 80)
	for _, c := range matches {
		if !strings.Contains(out, "/"+c.Name) {
			t.Fatalf("output missing /%s; got:\n%s", c.Name, out)
		}
	}
}

func TestRenderSlashPopup_LineCountMatchesMatchCount(t *testing.T) {
	matches := MatchSlashCommands("c")
	out := RenderSlashPopup(matches, 0, 80)
	lines := strings.Split(out, "\n")
	if len(lines) != len(matches) {
		t.Fatalf("expected %d lines, got %d (output:\n%s)",
			len(matches), len(lines), out)
	}
}
