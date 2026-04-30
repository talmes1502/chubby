// Package model — state_file.go: tiny persistence layer for TUI session state.
//
// Currently stores only the set of collapsed group names. The file lives at
// ~/.claude/hub/tui-state.json. A missing or malformed file is non-fatal.
package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// PersistedState is the on-disk shape of the tui-state.json file.
type PersistedState struct {
	GroupsCollapsed []string `json:"groups_collapsed"`
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

// LoadCollapsedGroups reads the persisted set of collapsed group names.
// On any error the result is an empty (non-nil) map.
func LoadCollapsedGroups() map[string]bool {
	out := map[string]bool{}
	p := stateFilePath()
	if p == "" {
		return out
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return out
	}
	var st PersistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return out
	}
	for _, g := range st.GroupsCollapsed {
		out[g] = true
	}
	return out
}

// SaveCollapsedGroups serializes the set of collapsed groups (true entries
// only) to ~/.claude/hub/tui-state.json. Errors are ignored — persistence
// is best-effort for the TUI.
func SaveCollapsedGroups(collapsed map[string]bool) error {
	p := stateFilePath()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(collapsed))
	for k, v := range collapsed {
		if v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	data, err := json.Marshal(PersistedState{GroupsCollapsed: keys})
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
