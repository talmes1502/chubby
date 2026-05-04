package model

import (
	"strings"
	"testing"
)

// TestDeadSession_RendersRespawnHint: when the focused session has
// status=dead, the conversation pane must render an explicit "session
// ended / Ctrl+P to respawn" placeholder instead of the captured-PTY
// ghost (the screen claude was on when it died) or a blank frame.
// Pre-fix the dead pane appeared empty and unresponsive — the user
// had no signal that the session was gone, much less how to respawn.
func TestDeadSession_RendersRespawnHint(t *testing.T) {
	s := &Session{ID: "s1", Name: "temp", Status: StatusDead, Color: "12"}
	r := renderViewportFull(s, 60, 12, true, nil)
	if !strings.Contains(r.view, "session ended") {
		t.Fatalf("dead-session pane should say 'session ended'; got:\n%s", r.view)
	}
	if !strings.Contains(r.view, "Ctrl+P") {
		t.Fatalf("dead-session pane should mention Ctrl+P respawn; got:\n%s", r.view)
	}
	if !strings.Contains(r.view, "temp") {
		t.Fatalf("dead-session pane should name the session; got:\n%s", r.view)
	}
}

// TestLiveSessionWithNilPane_StillShowsConnecting: the "(connecting…)"
// fallback for the brief window between session creation and pane
// allocation must NOT trigger for dead sessions — they get the
// dedicated placeholder above. This test guards against an ordering
// regression where the pane==nil branch would shadow StatusDead.
func TestLiveSessionWithNilPane_StillShowsConnecting(t *testing.T) {
	s := &Session{ID: "s1", Name: "fresh", Status: StatusIdle, Color: "12"}
	r := renderViewportFull(s, 60, 12, true, nil)
	if !strings.Contains(r.view, "connecting") {
		t.Fatalf("live-session-with-nil-pane should show '(connecting…)'; got:\n%s", r.view)
	}
}
