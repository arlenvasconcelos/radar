package settings

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// DesktopState is state the Desktop app owns alone: what the window was doing
// when it was last closed, so reopening it comes back to the same place.
//
// It lives in its own file, deliberately NOT in the Settings struct, for two
// reasons. /api/settings serializes Settings verbatim (including through a
// Cloud tunnel), so anything in there is served to every viewer and can be
// round-tripped away by a PUT from a client that never saw the field. And this
// is Desktop's state, not the machine's: `kubectl radar` shares $HOME with the
// Desktop app, so a value both could reach is one a Desktop switch could use
// to steer a later terminal command. Keeping the store separate makes that
// separation structural rather than a flag someone can flip.
type DesktopState struct {
	// LastContext is the kubeconfig context the Desktop window was last
	// switched to, restored on the next launch so it reopens on the cluster
	// the user was working in rather than the kubeconfig's current-context.
	LastContext *LastContext `json:"lastContext,omitempty"`
}

// LastContext identifies a kubeconfig context precisely enough to survive a
// restart. The displayed name alone is not enough across multiple kubeconfig
// files: which file owns the unqualified form depends on directory read order,
// so dropping a new file into a watched directory can steal the name and point
// the restore at a different cluster. SourceFile + InFileName pin the exact
// context; the name is the fallback for when that file has moved.
type LastContext struct {
	Name       string `json:"name"`
	SourceFile string `json:"sourceFile,omitempty"`
	InFileName string `json:"inFileName,omitempty"`
}

// desktopMu serializes load-mutate-save cycles on the desktop-state file.
var desktopMu sync.Mutex

// DesktopStatePath returns the desktop-state file path
// (~/.radar/desktop-state.json).
func DesktopStatePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[settings] Cannot determine home directory: %v (desktop state will not be persisted)", err)
		return ""
	}
	return filepath.Join(homeDir, ".radar", "desktop-state.json")
}

// LoadDesktopState reads the desktop state, distinguishing "no file yet"
// (zero value, nil error) from a failed read or parse (zero value, error).
// Callers restoring a remembered cluster must use the error: treating an
// unreadable file as "the user never picked one" would silently start
// somewhere else and then overwrite the pick they actually had.
func LoadDesktopState() (DesktopState, error) {
	path := DesktopStatePath()
	if path == "" {
		return DesktopState{}, errors.New("desktop state path unavailable")
	}
	var s DesktopState
	if err := readJSONFile(path, &s); err != nil {
		return DesktopState{}, err
	}
	return s, nil
}

// UpdateDesktopState atomically loads, applies a mutation, and saves.
//
// It refuses to write over a file it could not read, for the same reason
// Update does: a context switch calls this on its own, so one unreadable file
// would otherwise erase whatever else the store had picked up.
func UpdateDesktopState(mutate func(*DesktopState)) (DesktopState, error) {
	desktopMu.Lock()
	defer desktopMu.Unlock()
	s, err := LoadDesktopState()
	if err != nil {
		return s, err
	}
	mutate(&s)
	return s, writeJSONFile(DesktopStatePath(), s)
}
