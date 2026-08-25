package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopStateRoundTrips(t *testing.T) {
	useTempHome(t)

	if _, err := UpdateDesktopState(func(st *DesktopState) {
		st.LastContext = &LastContext{Name: "prod-eu", SourceFile: "/kube/team.yaml", InFileName: "prod"}
	}); err != nil {
		t.Fatalf("UpdateDesktopState: %v", err)
	}

	got, err := LoadDesktopState()
	if err != nil {
		t.Fatalf("LoadDesktopState: %v", err)
	}
	if got.LastContext == nil || *got.LastContext != (LastContext{
		Name: "prod-eu", SourceFile: "/kube/team.yaml", InFileName: "prod",
	}) {
		t.Errorf("LastContext = %+v, want name/file/in-file-name all preserved", got.LastContext)
	}
}

func TestLoadDesktopStateTreatsAMissingFileAsNothingRecorded(t *testing.T) {
	useTempHome(t)

	got, err := LoadDesktopState()
	if err != nil {
		t.Fatalf("LoadDesktopState on a fresh home: %v", err)
	}
	if got.LastContext != nil {
		t.Errorf("LastContext = %+v, want nil before anything is recorded", got.LastContext)
	}
}

func TestUpdateDesktopStateRefusesToOverwriteAnUnreadableFile(t *testing.T) {
	dir := useTempHome(t)

	path := filepath.Join(dir, ".radar", "desktop-state.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("{bad"), 0o644)

	if _, err := UpdateDesktopState(func(st *DesktopState) {
		st.LastContext = &LastContext{Name: "prod-eu"}
	}); err == nil {
		t.Fatal("UpdateDesktopState should fail when the existing file cannot be read")
	}

	raw, _ := os.ReadFile(path)
	if string(raw) != "{bad" {
		t.Errorf("UpdateDesktopState rewrote an unreadable file: %s", raw)
	}
}

// The two stores are separate files on purpose — /api/settings serializes
// Settings verbatim, and this is Desktop's state rather than the machine's.
func TestDesktopStateStaysOutOfTheSettingsFile(t *testing.T) {
	dir := useTempHome(t)

	if err := Save(Settings{Theme: "dark"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := UpdateDesktopState(func(st *DesktopState) {
		st.LastContext = &LastContext{Name: "prod-eu"}
	}); err != nil {
		t.Fatalf("UpdateDesktopState: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".radar", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if strings.Contains(string(raw), "prod-eu") || strings.Contains(string(raw), "lastContext") {
		t.Errorf("settings.json carries desktop state: %s", raw)
	}
	if loaded := Load(); loaded.Theme != "dark" {
		t.Errorf("theme = %q, want the settings file untouched", loaded.Theme)
	}
}

func useTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}
