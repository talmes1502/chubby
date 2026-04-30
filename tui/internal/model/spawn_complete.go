// Package model — spawn_complete.go: Tab path completion + Ctrl+P
// recent-cwd cycling for the ModeSpawn cwd field. The complementary
// keybinding integration lives in handleKeySpawn (model.go).
//
// Tab completion behavior:
//
//   - "~" or "~/foo" prefixes are expanded via views.ExpandHome before
//     the lookup.
//   - If the value ends in "/" we list the directory's contents (partial
//     is empty) so the user can keep walking deeper. Otherwise we split
//     into (dir, base) and treat base as a case-insensitive prefix.
//   - Only directories match — files would just dead-end the user since
//     spawn_session requires a directory cwd.
//   - On match the result is "<dir>/<matched>/" with a trailing slash so
//     the user can immediately Tab-deeper without typing a slash.
//   - On multiple matches, the caller cycles by passing an incrementing
//     cycleIdx; tryPathComplete returns totalMatches so the caller can
//     decide whether to advance.
//
// TODO (v2): render a multi-match popup below the modal so the user
// can see all candidates at a glance before cycling. For now we only
// reveal the current match by overwriting the input value.
package model

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/USER/chubby/tui/internal/views"
)

// tryPathComplete attempts directory-name completion for the cwd field.
//
// value is the current input (possibly with leading "~"). cycleIdx is
// modulo totalMatches; pass 0 on the first Tab and increment on each
// repeat to walk through candidates.
//
// Returns:
//   - newValue: "<dir>/<matched_dir>/" — note the trailing slash so the
//     user can keep typing a deeper component without a manual "/".
//   - ok: false when there are zero matching subdirs (incl. when the
//     parent dir doesn't exist or is unreadable). Callers should fall
//     through to the default Tab behavior in that case.
//   - totalMatches: lets the caller decide whether to bump cycleIdx for
//     the next press.
func tryPathComplete(value string, cycleIdx int) (newValue string, ok bool, totalMatches int) {
	expanded := views.ExpandHome(value)

	// Split into (dir, partial). Trailing "/" means "list this dir's
	// contents", i.e. partial is empty.
	var dir, partial string
	if strings.HasSuffix(expanded, "/") {
		dir = expanded
		partial = ""
	} else if expanded == "" {
		// Empty input — nothing to anchor against. Bail rather than
		// silently listing CWD; that would surprise the user.
		return "", false, 0
	} else {
		dir = filepath.Dir(expanded)
		partial = filepath.Base(expanded)
	}

	// Resolve "." (filepath.Dir("foo") == ".") to the real CWD so the
	// listing is well-defined; this also keeps the assembled path
	// readable rather than rooted at "./".
	if dir == "." {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Permission denied / nonexistent / not a dir — silent miss.
		return "", false, 0
	}

	pl := strings.ToLower(partial)
	matches := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if pl == "" || strings.HasPrefix(strings.ToLower(name), pl) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return "", false, 0
	}
	sort.Slice(matches, func(i, j int) bool {
		return strings.ToLower(matches[i]) < strings.ToLower(matches[j])
	})

	idx := cycleIdx
	if idx < 0 {
		idx = 0
	}
	idx = idx % len(matches)

	picked := matches[idx]
	// filepath.Join collapses redundant separators ("/foo//bar"
	// -> "/foo/bar") and drops trailing slashes, but we re-add the
	// trailing slash so the user can keep walking deeper.
	joined := filepath.Join(dir, picked)
	return joined + string(filepath.Separator), true, len(matches)
}
