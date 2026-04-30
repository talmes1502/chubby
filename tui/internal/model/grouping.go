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
	RailRowHeader RailRowKind = iota
	RailRowSession
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

// BuildRailRows flattens groups + collapsed state into the visible rail
// rows. Group headers are emitted always, even if collapsed. Sessions
// inside a collapsed group are not emitted.
//
// sessions is the (already filtered, if applicable) full session list.
// origSessions is the unfiltered sessions slice — needed so SessionIdx
// points back into the canonical Model.sessions for focus tracking.
func BuildRailRows(sessions []Session, origSessions []Session, collapsed map[string]bool) []RailRow {
	idxByID := make(map[string]int, len(origSessions))
	for i, s := range origSessions {
		idxByID[s.ID] = i
	}
	groups := GroupSessions(sessions)
	rows := make([]RailRow, 0, len(sessions)+len(groups))
	for _, g := range groups {
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
