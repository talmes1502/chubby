// Package model — grouping.go: hybrid session grouping for the left rail.
//
// Group key:
//   - if session has tags: first tag
//   - else: path.Base(cwd)
//   - cwd is "/" or "" → "(untitled)"
//
// Groups sort alphabetically (case-insensitive); "(untitled)" sorts last.
// Sessions within a group sort alphabetically by Name.
package model

import (
	"path"
	"sort"
	"strings"
)

// UntitledGroup is the fallback group key when a session has no tags
// and a cwd of "" or "/".
const UntitledGroup = "(untitled)"

// Group is a named bucket of sessions, in display order.
type Group struct {
	Name     string
	Sessions []Session
}

// GroupKey returns the display group key for a session.
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
	// RailRowHeader is an auto-derived group header (first tag or cwd
	// basename). Glyph: ▾/▸.
	RailRowHeader RailRowKind = iota
	// RailRowSession is a session row inside any kind of header (folder
	// or auto group). The renderer disambiguates via the parent
	// GroupName when it cares.
	RailRowSession
	// RailRowFolder is an explicit user-created folder header. Glyph:
	// 📁. Folders sort alphabetically among themselves and always
	// render above the auto-group section.
	RailRowFolder
)

// RailRow is a flattened row in the left rail. Either a group header
// or a session. The renderer and the up/down navigator both consume
// this list, so they stay in lock-step.
type RailRow struct {
	Kind      RailRowKind
	GroupName string
	// SessionIdx is the index into the unfiltered Model.sessions slice
	// for RailRowSession. -1 for headers.
	SessionIdx int
	// Session is a copy convenience for renderers; only set for
	// RailRowSession.
	Session Session
}

// BuildRailRows flattens folders + auto-groups + collapsed state into
// the visible rail rows.
//
// Layout:
//  1. User-created folders (alphabetical), each with assigned visible
//     sessions underneath.
//  2. Auto-derived groups (alphabetical, "(untitled)" last) for every
//     visible session NOT assigned to a folder.
//
// Headers are emitted always, even when collapsed; sessions inside a
// collapsed header are not emitted. Sessions in a folder do NOT also
// appear in their auto group — folders take precedence.
//
// sessions is the (already filtered, if applicable) full session list.
// origSessions is the unfiltered sessions slice — needed so SessionIdx
// points back into the canonical Model.sessions for focus tracking.
// folders is the user folder state; pass an empty FoldersState (or
// FoldersState{} zero value) to opt out and get the legacy rail.
// collapsed keys are header names — folder names and auto-group names
// share the same map; folder/group name collisions are unlikely in
// practice and we accept the tiny ambiguity rather than carrying a
// second map.
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
	for _, name := range folders.AllFolderNames() {
		rows = append(rows, RailRow{
			Kind:       RailRowFolder,
			GroupName:  name,
			SessionIdx: -1,
		})
		// Mark every assigned id (visible or not) so the auto-group
		// section below skips them. Unassigned-and-hidden sessions stay
		// hidden naturally because they're not in `sessions`.
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

	// 2. Auto-groups for the rest.
	unassigned := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		if assigned[s.ID] {
			continue
		}
		unassigned = append(unassigned, s)
	}
	for _, g := range GroupSessions(unassigned) {
		rows = append(rows, RailRow{
			Kind:       RailRowHeader,
			GroupName:  g.Name,
			SessionIdx: -1,
		})
		if collapsed[g.Name] {
			continue
		}
		for _, s := range g.Sessions {
			rows = append(rows, RailRow{
				Kind:       RailRowSession,
				GroupName:  g.Name,
				SessionIdx: idxByID[s.ID],
				Session:    s,
			})
		}
	}
	return rows
}

// GroupSessions hybrid-groups sessions and returns them in display order:
// groups alphabetical (case-insensitive), "(untitled)" last; sessions
// inside each group alphabetical by Name (case-insensitive).
func GroupSessions(sessions []Session) []Group {
	buckets := map[string][]Session{}
	for _, s := range sessions {
		k := GroupKey(s)
		buckets[k] = append(buckets[k], s)
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ki, kj := keys[i], keys[j]
		// Untitled always last.
		if ki == UntitledGroup && kj != UntitledGroup {
			return false
		}
		if kj == UntitledGroup && ki != UntitledGroup {
			return true
		}
		return strings.ToLower(ki) < strings.ToLower(kj)
	})
	out := make([]Group, 0, len(keys))
	for _, k := range keys {
		ss := buckets[k]
		sort.SliceStable(ss, func(i, j int) bool {
			return strings.ToLower(ss[i].Name) < strings.ToLower(ss[j].Name)
		})
		out = append(out, Group{Name: k, Sessions: ss})
	}
	return out
}
