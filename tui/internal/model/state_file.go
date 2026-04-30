// Package model — state_file.go: tiny persistence layer for TUI session state.
//
// Currently stores the set of collapsed group names plus a single
// rail_collapsed boolean. The file lives at ~/.claude/hub/tui-state.json.
// A missing or malformed file is non-fatal.
package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// PersistedState is the on-disk shape of the tui-state.json file.
//
// RailCollapsed is a pointer in the JSON shape only conceptually — when
// the field is missing from the file we want a clean false, which a
// plain bool already gives us. Migration is therefore just "decode and
// the zero value wins" — older files without the key parse cleanly.
type PersistedState struct {
	GroupsCollapsed []string `json:"groups_collapsed"`
	RailCollapsed   bool     `json:"rail_collapsed"`
}

// TUIState is the in-memory snapshot the model passes to SaveTUIState.
// Mirrors PersistedState but uses a map-of-bool-style group representation
// at the call site for ergonomics — callers pass the names directly.
type TUIState struct {
	GroupsCollapsed []string
	RailCollapsed   bool
}

// stateFilePath resolves to ~/.claude/hub/tui-state.json. If HOME cannot
// be resolved an empty string is returned (callers must check).
func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "hub", "tui-state.json")
}

// loadPersistedState reads and decodes the on-disk state, returning a
// zero-value PersistedState on any error. Centralizes the JSON shape so
// every loader sees a consistent view.
func loadPersistedState() PersistedState {
	var st PersistedState
	p := stateFilePath()
	if p == "" {
		return st
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

// LoadCollapsedGroups reads the persisted set of collapsed group names.
// On any error the result is an empty (non-nil) map.
func LoadCollapsedGroups() map[string]bool {
	out := map[string]bool{}
	st := loadPersistedState()
	for _, g := range st.GroupsCollapsed {
		out[g] = true
	}
	return out
}

// LoadRailCollapsed returns the persisted rail_collapsed flag, or false
// if the file is missing/malformed/has no such field (graceful migration
// from older state files that predate the key).
func LoadRailCollapsed() bool {
	return loadPersistedState().RailCollapsed
}

// SaveCollapsedGroups serializes only the collapsed-groups slice. It
// preserves the existing rail_collapsed value on disk so callers that
// only know about groups don't accidentally clobber the rail state.
// Errors are ignored — persistence is best-effort for the TUI.
func SaveCollapsedGroups(collapsed map[string]bool) error {
	cur := loadPersistedState()
	return SaveTUIState(TUIState{
		GroupsCollapsed: collapsedGroupNamesFromMap(collapsed),
		RailCollapsed:   cur.RailCollapsed,
	})
}

// SaveTUIState writes the full state snapshot. Both fields are
// serialized; older readers without the rail_collapsed field will
// silently ignore it.
func SaveTUIState(s TUIState) error {
	p := stateFilePath()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	keys := append([]string{}, s.GroupsCollapsed...)
	sort.Strings(keys)
	data, err := json.Marshal(PersistedState{
		GroupsCollapsed: keys,
		RailCollapsed:   s.RailCollapsed,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// collapsedGroupNamesFromMap returns the sorted list of keys whose
// value is true. Used internally to convert the model's group map into
// the slice shape SaveTUIState wants.
func collapsedGroupNamesFromMap(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			keys = append(keys, k)
		}
	}
	return keys
}

// collapsedGroupNames is the public helper used by the model package's
// reducers. Same behavior as collapsedGroupNamesFromMap; kept distinct
// so the function name reads naturally at the call site.
func collapsedGroupNames(m map[string]bool) []string {
	return collapsedGroupNamesFromMap(m)
}
