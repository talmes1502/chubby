// Package model — folders.go: persistence and helpers for explicit
// user-created folders. Folders are a layer above the existing tag/cwd-
// basename auto-grouping: a session may be "in" at most one user folder,
// in which case it renders under that folder header in the rail; if it's
// not in any folder it falls through to the auto-grouping logic in
// grouping.go.
//
// The on-disk file lives at ~/.claude/chubby/folders.json (CHUBBY_HOME
// override respected). Missing/malformed files are non-fatal — the
// loader returns an empty FoldersState and the rail behaves as if no
// folders are defined.
package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FoldersState is the persisted form of user-created folder organization.
type FoldersState struct {
	// Folders maps folder name → ordered list of session ids assigned to
	// it. The empty key is reserved for the implicit "(unassigned)"
	// bucket and is NOT persisted; callers must not write to it.
	Folders map[string][]string `json:"folders"`
}

// chubbyDataDir resolves the on-disk root for chubby state. Mirrors the
// HubHome() resolver in views/history.go: CHUBBY_HOME / CHUB_HOME env
// override, then ~/.claude/chubby. Returns "" when HOME is unresolvable
// AND no env override is set, so callers can treat that as "no
// persistence available" without panicking.
func chubbyDataDir() string {
	if v := os.Getenv("CHUBBY_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("CHUB_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "chubby")
}

// foldersPath resolves to ~/.claude/chubby/folders.json. Empty string
// when the data dir cannot be resolved (callers fall back to a no-op).
func foldersPath() string {
	d := chubbyDataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "folders.json")
}

// LoadFolders reads and decodes the on-disk folders state. On any error
// (missing file, bad JSON, missing data dir) the result is a zero-value
// FoldersState with an initialized (non-nil) Folders map so callers can
// freely write to it without nil-checking first.
func LoadFolders() FoldersState {
	st := FoldersState{Folders: map[string][]string{}}
	p := foldersPath()
	if p == "" {
		return st
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return st
	}
	var on FoldersState
	if err := json.Unmarshal(data, &on); err != nil {
		return st
	}
	if on.Folders == nil {
		on.Folders = map[string][]string{}
	}
	// The empty-key bucket is reserved for "(unassigned)" — strip it on
	// load so a hand-edited folders.json can't poison the in-memory
	// invariant.
	delete(on.Folders, "")
	return on
}

// SaveFolders writes the FoldersState atomically (tempfile + rename).
// Missing data dir is non-fatal. Errors propagate so callers can
// surface them; the model layer typically ignores them since folders
// are best-effort UI state.
func SaveFolders(s FoldersState) error {
	p := foldersPath()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Drop empty-key entries before serializing — same invariant as load.
	out := FoldersState{Folders: map[string][]string{}}
	for k, v := range s.Folders {
		if k == "" {
			continue
		}
		// Defensive copy so callers can keep mutating their map after
		// SaveFolders returns without corrupting the on-disk shape.
		ids := make([]string, len(v))
		copy(ids, v)
		out.Folders[k] = ids
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// FolderForSession returns the folder name a session is assigned to, or
// "" if it's unassigned. Linear over folders × sessions per folder —
// fine for the small N we expect (tens of folders, low hundreds of
// sessions).
func (s FoldersState) FolderForSession(sid string) string {
	if sid == "" {
		return ""
	}
	for name, ids := range s.Folders {
		for _, id := range ids {
			if id == sid {
				return name
			}
		}
	}
	return ""
}

// SessionsInFolder returns the assigned session ids in their stored
// order. Returns nil for unknown folders so callers can range over the
// result without a not-found check.
func (s FoldersState) SessionsInFolder(name string) []string {
	if name == "" {
		return nil
	}
	ids := s.Folders[name]
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// Assign adds sid to folder, removing it from any other folder it might
// be in (a session belongs to at most one user folder). Creates the
// folder if it doesn't already exist. No-op when folder == "" — that
// path is reserved for Unassign so the empty-key invariant holds.
func (s *FoldersState) Assign(folder, sid string) {
	if folder == "" || sid == "" {
		return
	}
	if s.Folders == nil {
		s.Folders = map[string][]string{}
	}
	// Remove from any prior folder first.
	s.Unassign(sid)
	// Append to the target folder.
	cur := s.Folders[folder]
	for _, id := range cur {
		if id == sid {
			return // already there — Unassign should have caught it but be safe
		}
	}
	s.Folders[folder] = append(cur, sid)
}

// Unassign removes sid from any folder it's in. Empty folders are kept
// (the user explicitly created them; auto-pruning would surprise).
func (s *FoldersState) Unassign(sid string) {
	if sid == "" || s.Folders == nil {
		return
	}
	for name, ids := range s.Folders {
		out := ids[:0]
		changed := false
		for _, id := range ids {
			if id == sid {
				changed = true
				continue
			}
			out = append(out, id)
		}
		if changed {
			s.Folders[name] = out
		}
	}
}

// RenameFolder moves the entry under old to new. Returns an error if
// old doesn't exist or new already does (collision). No-op rename
// (old==new) is a successful no-op.
func (s *FoldersState) RenameFolder(old, newName string) error {
	if old == "" || newName == "" {
		return fmt.Errorf("folder name cannot be empty")
	}
	if old == newName {
		return nil
	}
	if s.Folders == nil {
		return fmt.Errorf("no such folder: %q", old)
	}
	ids, ok := s.Folders[old]
	if !ok {
		return fmt.Errorf("no such folder: %q", old)
	}
	if _, exists := s.Folders[newName]; exists {
		return fmt.Errorf("folder %q already exists", newName)
	}
	s.Folders[newName] = ids
	delete(s.Folders, old)
	return nil
}

// CreateFolder creates an empty folder. Returns an error if it already
// exists. Empty names are rejected.
func (s *FoldersState) CreateFolder(name string) error {
	if name == "" {
		return fmt.Errorf("folder name cannot be empty")
	}
	if s.Folders == nil {
		s.Folders = map[string][]string{}
	}
	if _, exists := s.Folders[name]; exists {
		return fmt.Errorf("folder %q already exists", name)
	}
	s.Folders[name] = []string{}
	return nil
}

// AllFolderNames returns folder names sorted alphabetically (case-
// insensitive). Used by the rail builder to render folders in a stable
// order.
func (s FoldersState) AllFolderNames() []string {
	out := make([]string, 0, len(s.Folders))
	for k := range s.Folders {
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
