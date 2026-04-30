package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestDirs creates a tmp dir with a fixed set of subdirectories
// (and one regular file) for the path-completion tests. Returned dir is
// auto-cleaned by t.Cleanup. Subdirs created: alpha, alpaca, beta. Plus
// "alphabet.txt" as a file — to verify files are NOT considered.
func makeTestDirs(t *testing.T) string {
	t.Helper()
	// /tmp prefix to avoid the macOS AF_UNIX path-length issue (not
	// strictly relevant here since we don't bind sockets, but consistent
	// with the rest of the model tests).
	dir, err := os.MkdirTemp("/tmp", "chubby-path-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range []string{"alpha", "alpaca", "beta"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "alphabet.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return dir
}

// TestTryPathComplete_NoMatchReturnsFalse — partial that matches nothing
// (no subdir starts with "zzz") yields ok=false; the existing field-cycle
// behavior in handleKeySpawn falls through.
func TestTryPathComplete_NoMatchReturnsFalse(t *testing.T) {
	dir := makeTestDirs(t)
	in := filepath.Join(dir, "zzz")
	if _, ok, _ := tryPathComplete(in, 0); ok {
		t.Fatalf("expected no match for %q", in)
	}
}

// TestTryPathComplete_SingleMatchAppendsTrailingSlash — partial "be"
// matches exactly "beta"; result is "<dir>/beta/" with the trailing
// slash so the user can keep typing.
func TestTryPathComplete_SingleMatchAppendsTrailingSlash(t *testing.T) {
	dir := makeTestDirs(t)
	got, ok, total := tryPathComplete(filepath.Join(dir, "be"), 0)
	if !ok {
		t.Fatalf("expected match for 'be'")
	}
	if total != 1 {
		t.Fatalf("totalMatches = %d, want 1", total)
	}
	want := filepath.Join(dir, "beta") + string(filepath.Separator)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestTryPathComplete_MultiMatchCyclesOnRepeatedCalls — partial "alp"
// matches "alpaca" and "alpha". Sorted alphabetically: alpaca, alpha.
// First call (cycleIdx=0) → alpaca; second (cycleIdx=1) → alpha;
// third (cycleIdx=2) wraps back to alpaca.
func TestTryPathComplete_MultiMatchCyclesOnRepeatedCalls(t *testing.T) {
	dir := makeTestDirs(t)
	in := filepath.Join(dir, "alp")
	results := []string{}
	for i := 0; i < 3; i++ {
		got, ok, total := tryPathComplete(in, i)
		if !ok {
			t.Fatalf("idx %d: expected match", i)
		}
		if total != 2 {
			t.Fatalf("idx %d: totalMatches = %d, want 2", i, total)
		}
		results = append(results, got)
	}
	// Files (alphabet.txt) must NOT appear: only directories.
	for _, r := range results {
		if strings.Contains(r, "alphabet.txt") {
			t.Fatalf("file should not match: %q", r)
		}
	}
	want0 := filepath.Join(dir, "alpaca") + string(filepath.Separator)
	want1 := filepath.Join(dir, "alpha") + string(filepath.Separator)
	if results[0] != want0 {
		t.Fatalf("cycle[0] = %q, want %q", results[0], want0)
	}
	if results[1] != want1 {
		t.Fatalf("cycle[1] = %q, want %q", results[1], want1)
	}
	if results[2] != want0 {
		t.Fatalf("cycle wrap-around[2] = %q, want %q", results[2], want0)
	}
}

// TestTryPathComplete_TildeExpands — "~" must be expanded before
// resolving the dir; otherwise os.ReadDir would treat the literal "~"
// as a relative path. We validate by setting HOME to a tmp dir we
// fully control and asking for "~/al".
func TestTryPathComplete_TildeExpands(t *testing.T) {
	dir := makeTestDirs(t)
	// Override HOME so views.ExpandHome resolves to our tmp dir; restore
	// via t.Setenv (Go test infra cleans up).
	t.Setenv("HOME", dir)

	got, ok, total := tryPathComplete("~/al", 0)
	if !ok {
		t.Fatalf("expected match for ~/al with HOME=%s", dir)
	}
	// Should match alpaca first (alphabetical order).
	if total != 2 {
		t.Fatalf("totalMatches = %d, want 2", total)
	}
	want := filepath.Join(dir, "alpaca") + string(filepath.Separator)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestTryPathComplete_TrailingSlashListsContents — value ending in "/"
// is treated as "list this dir's subdirs" (partial = ""), so the user
// can Tab-walk into a directory whose contents they don't remember.
func TestTryPathComplete_TrailingSlashListsContents(t *testing.T) {
	dir := makeTestDirs(t)
	got, ok, total := tryPathComplete(dir+"/", 0)
	if !ok {
		t.Fatalf("expected match listing contents of %q", dir)
	}
	// Three subdirs: alpaca, alpha, beta. Files don't count.
	if total != 3 {
		t.Fatalf("totalMatches = %d, want 3", total)
	}
	// First (alphabetical) should be alpaca.
	want := filepath.Join(dir, "alpaca") + string(filepath.Separator)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestTryPathComplete_EmptyValueIsBail — empty input shouldn't silently
// list CWD; the user might be confused by the sudden jump. We bail.
func TestTryPathComplete_EmptyValueIsBail(t *testing.T) {
	if _, ok, _ := tryPathComplete("", 0); ok {
		t.Fatalf("expected ok=false for empty input")
	}
}
