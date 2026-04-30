package views

import (
	"os"
	"path/filepath"
)

// LogTailCap caps the bytes returned by ReadLogTail.
const LogTailCap = 8 * 1024

// HubHome resolves CHUB_HOME / ~/.claude/hub. Mirrors paths.hub_home() on
// the Python side: the TUI runs on the same host as the daemon, so reading
// session logs directly off disk is simpler than a daemon round-trip.
func HubHome() string {
	if v := os.Getenv("CHUB_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "hub")
}

// ReadLogTail returns the last LogTailCap bytes of
// ${CHUB_HOME}/runs/<runID>/logs/<sessionName>.log, or a placeholder
// string on error (so the View can render something useful).
func ReadLogTail(runID, sessionName string) string {
	if runID == "" || sessionName == "" {
		return ""
	}
	p := filepath.Join(HubHome(), "runs", runID, "logs", sessionName+".log")
	data, err := os.ReadFile(p)
	if err != nil {
		return "(no log: " + err.Error() + ")"
	}
	if len(data) > LogTailCap {
		data = data[len(data)-LogTailCap:]
	}
	return string(data)
}
