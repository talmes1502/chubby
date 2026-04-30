// Package model — migration.go: one-time mapping from the legacy
// auto-grouping rail (first-tag / cwd-basename) into explicit user
// folders (D10c).
//
// Pre-D10a sessions had no folder concept — the rail derived a "group"
// at render time from each session's first tag (or, failing that, the
// basename of its cwd). Users grew accustomed to seeing related
// sessions clustered together. After D10a the rail no longer auto-
// groups, so without a migration those clusters would scatter into a
// flat alphabetical list on first launch and surprise users.
//
// MigrateAutoGroupingToFolders walks every session not already in a
// folder, computes its old auto-group key (first tag, falling back to
// cwd basename), and assigns it to a folder of the same name —
// creating the folder if it doesn't exist. Sessions whose old key
// would have been UntitledGroup ("(untitled)") stay unfiled, since
// "(untitled)" wasn't a meaningful cluster.
//
// The migration runs at most once per chubby data directory: a
// sentinel file at <chubbyDataDir()>/folders-migrated marks completion.
// The sentinel is touched even when 0 sessions were migrated so a user
// who launches the new TUI with zero sessions doesn't get re-migrated
// after they've manually built up a folder layout.
package model

import (
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

// migrationSentinelName is the file name we touch inside chubbyDataDir
// after a successful (or no-op) migration. Hidden behind a constant so
// tests can reference it without copy-pasting the literal.
const migrationSentinelName = "folders-migrated"

// MigrationDoneMsg is emitted by the Init-time migration so the
// reducer can show a transient "migrated N sessions into folders"
// toast when N > 0. N == 0 still flows through (idempotency / first-
// launch path) but the reducer suppresses the toast.
type MigrationDoneMsg struct {
	N int
}

// MigrateAutoGroupingToFolders scans sessions and assigns each one
// (that isn't already in a folder) into a folder matching its old
// auto-group key. Returns the number of sessions newly assigned.
//
// The function is idempotent: a sentinel file inside chubbyDataDir
// records that migration has run, and subsequent calls short-circuit
// to 0 without touching state. The sentinel is touched even when no
// sessions needed migrating, so the migration doesn't spuriously re-
// run later (e.g. after the user manually carves out a folder layout
// and adds new untagged sessions whose cwd basenames happen to match
// real folder names).
//
// state is mutated in-place when at least one session is assigned;
// callers should pass a pointer to the in-memory FoldersState so the
// rail picks up the new layout immediately. The function also calls
// SaveFolders so the on-disk file matches.
//
// Errors writing the sentinel or saving the state are swallowed
// intentionally — the migration is best-effort and a busted disk
// shouldn't crash startup. The worst case is the migration runs again
// next launch, which is also a no-op for already-assigned sessions.
func MigrateAutoGroupingToFolders(sessions []Session, state *FoldersState) int {
	dir := chubbyDataDir()
	if dir == "" {
		// No persistence available; nothing meaningful to do.
		return 0
	}
	sentinel := filepath.Join(dir, migrationSentinelName)
	if _, err := os.Stat(sentinel); err == nil {
		return 0
	}

	if state.Folders == nil {
		state.Folders = map[string][]string{}
	}
	migrated := 0
	for _, s := range sessions {
		if state.FolderForSession(s.ID) != "" {
			continue
		}
		// Derive the old auto-group key — equivalent to GroupKey,
		// inlined so the migration's intent is obvious at a glance.
		var folder string
		if len(s.Tags) > 0 && s.Tags[0] != "" {
			folder = s.Tags[0]
		} else if s.Cwd != "" {
			folder = filepath.Base(s.Cwd)
		}
		if folder == "" || folder == "/" || folder == "." || folder == UntitledGroup {
			// Unfiled sessions stay unfiled — "(untitled)" was a
			// fallback bucket, not a real cluster the user would
			// expect to see preserved as a folder.
			continue
		}
		state.Assign(folder, s.ID)
		migrated++
	}

	if migrated > 0 {
		// Best-effort save; if it fails we still touch the sentinel
		// below so we don't keep retrying — the user can always re-
		// assign manually with /movetofolder.
		if err := SaveFolders(*state); err != nil {
			// Don't return early: still touch the sentinel.
			_ = err
		}
	}

	// Make sure the data dir exists before touching the sentinel —
	// SaveFolders would have created it on the migrated>0 path, but
	// the migrated==0 path may need to do it itself.
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(sentinel, []byte("done\n"), 0o644)
	}
	return migrated
}

// runMigrationCmd wraps MigrateAutoGroupingToFolders into a tea.Cmd
// returning MigrationDoneMsg. Callers (the listMsg handler) bundle
// this into a tea.Batch so the migration runs once we have the full
// session list. The reducer reloads folders from disk on the
// MigrationDoneMsg path so the rail picks up the freshly-assigned
// sessions without the goroutine racing the in-memory state.
func runMigrationCmd(sessions []Session, state FoldersState) tea.Cmd {
	// Snapshot the session slice so the goroutine can't observe a
	// post-listMsg mutation. Folders state is value-typed so passing
	// it by value already snapshots; the migration writes through
	// SaveFolders, so the reducer's LoadFolders on receipt is the
	// real source of truth.
	snap := make([]Session, len(sessions))
	copy(snap, sessions)
	return func() tea.Msg {
		st := state
		if st.Folders == nil {
			st.Folders = map[string][]string{}
		} else {
			cp := make(map[string][]string, len(st.Folders))
			for k, v := range st.Folders {
				ids := make([]string, len(v))
				copy(ids, v)
				cp[k] = ids
			}
			st.Folders = cp
		}
		n := MigrateAutoGroupingToFolders(snap, &st)
		return MigrationDoneMsg{N: n}
	}
}
