package views

import (
	"os"
	"path/filepath"
)

// LogTailCap caps the bytes returned by ReadLogTail.
const LogTailCap = 8 * 1024

// chubbyEnv reads CHUBBY_<name> with CHUB_<name> as a backward-compat
// fallback. Mirrors paths.chubby_env() on the Python side.
func chubbyEnv(name string) string {
	if v := os.Getenv("CHUBBY_" + name); v != "" {
		return v
	}
	return os.Getenv("CHUB_" + name)
}

// HubHome resolves CHUBBY_HOME (legacy: CHUB_HOME) / ~/.claude/chubby.
// Mirrors paths.hub_home() on the Python side: the TUI runs on the same
// host as the daemon, so reading session logs directly off disk is
// simpler than a daemon round-trip.
func HubHome() string {
	if v := chubbyEnv("HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "chubby")
}

// ReadLogTail returns the last LogTailCap bytes of
// ${CHUBBY_HOME}/runs/<runID>/logs/<sessionName>.log, or a placeholder
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
