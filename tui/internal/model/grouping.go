// Package model — grouping.go: rail row layout for the left rail.
//
// Layout (as of D10a):
//   - Folder rows (alphabetical) with their assigned sessions underneath
//   - Then unfiled sessions as a flat list (alphabetical by Name,
//     case-insensitive). No header row above unfiled sessions when there
//     are NO folders; a dim "(unfiled)" separator row is inserted only
//     when both folders AND unfiled sessions are present, so the user
//     can tell where the folder section ends.
//
// Auto-grouping by first-tag / cwd-basename was removed in D10a — every
// session that isn't in an explicit user folder shows up as a flat row
// at the bottom. The legacy `GroupKey` helper is kept (now narrowed to
// "first tag wins, falling back to cwd basename") because it's still
// used by Ctrl+N pre-fill heuristics and the one-time D10c migration
// that reads old auto-group identity off existing sessions.
package model

import (
	"path"
	"sort"
	"strings"
)

// UntitledGroup is the legacy fallback group key when a session has no
// tags and a cwd of "" or "/". Retained for the D10c migration and
// Ctrl+N "skip untitled when prefilling" heuristic; the rail no longer
// renders an UntitledGroup header.
const UntitledGroup = "(untitled)"

// GroupKey returns the legacy auto-derived group key for a session: the
// first tag, falling back to the cwd basename, falling back to
// UntitledGroup. Used by D10c migration (assigns existing sessions to
// folders matching their old auto-group key) and by openSpawnModal's
// pre-fill heuristic. Not used at render time anymore — the rail
// flattens unfiled sessions directly.
func GroupKey(s Session) string {
	if len(s.Tags) > 0 && s.Tags[0] != "" {
		return s.Tags[0]
	}
	if s.Cwd == "" || s.Cwd == "/" {
		return UntitledGroup
	}
	base := path.Base(s.Cwd)
	if base == "" || base == "/" || base == "." {
		return UntitledGroup
	}
	return base
}

// RailRowKind tells the rail renderer/navigator what kind of row this is.
type RailRowKind int

const (
	// RailRowFolder is an explicit user-created folder header. Glyph:
	// 📁. Folders sort alphabetically among themselves.
	RailRowFolder RailRowKind = iota
	// RailRowSession is a session row, either inside a folder (when
	// GroupName is the folder name) or at the top level for unfiled
	// sessions (when GroupName is "").
	RailRowSession
	// RailRowUnfiledSeparator is a non-interactive dim separator row
	// rendered between the folder block and the flat unfiled-sessions
	// block, but ONLY when both blocks are non-empty. Skipped by all
	// cursor navigation.
	RailRowUnfiledSeparator
)

// RailRow is a flattened row in the left rail. Either a folder header,
// a session, or the unfiled-separator. The renderer and the up/down
// navigator both consume this list, so they stay in lock-step.
type RailRow struct {
	Kind      RailRowKind
	GroupName string
	// SessionIdx is the index into the unfiltered Model.sessions slice
	// for RailRowSession. -1 for headers / separators.
	SessionIdx int
	// Session is a copy convenience for renderers; only set for
	// RailRowSession.
	Session Session
}

// BuildRailRows flattens folders + unfiled-sessions + collapsed state
// into the visible rail rows.
//
// Layout:
//  1. User-created folders (alphabetical, case-insensitive), each with
//     their assigned visible sessions underneath.
//  2. A dim "(unfiled)" separator row, but only when both folder rows
//     and unfiled sessions are present.
//  3. Unfiled sessions (alphabetical by Name, case-insensitive).
//
// Folder headers are emitted always, even when collapsed; sessions
// inside a collapsed folder are not emitted.
//
// sessions is the (already filtered, if applicable) full session list.
// origSessions is the unfiltered sessions slice — needed so SessionIdx
// points back into the canonical Model.sessions for focus tracking.
// folders is the user folder state; pass an empty FoldersState (or
// FoldersState{} zero value) to opt out and get a flat alphabetical
// list of every visible session.
// collapsed keys are folder names.
func BuildRailRows(sessions []Session, origSessions []Session, collapsed map[string]bool, folders FoldersState) []RailRow {
	idxByID := make(map[string]int, len(origSessions))
	for i, s := range origSessions {
		idxByID[s.ID] = i
	}
	// Build a fast lookup of visible sessions by id so folder rows can
	// pick out the right Session value (and skip ids assigned to a
	// folder but currently filter-hidden).
	bySessionID := make(map[string]Session, len(sessions))
	for _, s := range sessions {
		bySessionID[s.ID] = s
	}

	rows := make([]RailRow, 0, len(sessions)+len(folders.Folders)+4)

	// 1. Folders, alphabetical.
	assigned := make(map[string]bool, 16)
	folderNames := folders.AllFolderNames()
	for _, name := range folderNames {
		rows = append(rows, RailRow{
			Kind:       RailRowFolder,
			GroupName:  name,
			SessionIdx: -1,
		})
		// Mark every assigned id (visible or not) so the unfiled section
		// below skips them. Unassigned-and-hidden sessions stay hidden
		// naturally because they're not in `sessions`.
		ids := folders.SessionsInFolder(name)
		for _, id := range ids {
			assigned[id] = true
		}
		if collapsed[name] {
			continue
		}
		for _, id := range ids {
			s, ok := bySessionID[id]
			if !ok {
				// Filter-hidden or stale id — skip without erasing the
				// assignment from disk.
				continue
			}
			rows = append(rows, RailRow{
				Kind:       RailRowSession,
				GroupName:  name,
				SessionIdx: idxByID[id],
				Session:    s,
			})
		}
	}

	// 2. Collect unfiled sessions, alphabetical by Name (case-insensitive).
	unfiled := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		if assigned[s.ID] {
			continue
		}
		unfiled = append(unfiled, s)
	}
	sort.SliceStable(unfiled, func(i, j int) bool {
		return strings.ToLower(unfiled[i].Name) < strings.ToLower(unfiled[j].Name)
	})

	// 3. Separator only when BOTH folders AND unfiled sessions exist.
	hasFolders := len(folderNames) > 0
	hasUnfiled := len(unfiled) > 0
	if hasFolders && hasUnfiled {
		rows = append(rows, RailRow{
			Kind:       RailRowUnfiledSeparator,
			GroupName:  "(unfiled)",
			SessionIdx: -1,
		})
	}

	// 4. Unfiled sessions, flat.
	for _, s := range unfiled {
		rows = append(rows, RailRow{
			Kind:       RailRowSession,
			GroupName:  "",
			SessionIdx: idxByID[s.ID],
			Session:    s,
		})
	}

	return rows
}
