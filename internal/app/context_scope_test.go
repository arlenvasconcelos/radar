package app

import (
	"os"
	"path/filepath"
	"testing"
)

// The zero value is what a terminal entrypoint passes: `kubectl radar` and
// `radar diagnose --standalone` start where the shell says they do, so a
// switch made in Desktop can't steer a command typed after
// `kubectl config use-context`.
func TestTerminalEntrypointDoesNotRememberTheContext(t *testing.T) {
	useTempHome(t)

	persistLastContext(AppConfig{}, "prod-eu")

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want empty for an entrypoint that doesn't opt in", saved)
	}
}

func TestTerminalEntrypointStartsOnCurrentContext(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	got := startupContextPreference(AppConfig{})
	if got.Name != "" {
		t.Errorf("startupContextPreference() = %q, want empty so current-context stands", got.Name)
	}
}

func TestPersistLastContextSkippedWhenRestoreDisabled(t *testing.T) {
	useTempHome(t)

	cfg := remembering()
	cfg.RestoreLastContext = false
	persistLastContext(cfg, "prod-eu")

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want empty when restore is turned off", saved)
	}
}

func TestStartupContextPreferenceSkippedWhenRestoreDisabled(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	cfg := remembering()
	cfg.RestoreLastContext = false
	if got := startupContextPreference(cfg); got.Name != "" {
		t.Errorf("startupContextPreference() = %q, want empty when restore is turned off", got.Name)
	}
}

func TestForgetLastContextClearsTheMemory(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	ForgetLastContext()

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want it cleared", saved)
	}
}

func TestForgetLastContextLeavesOtherDesktopStateAlone(t *testing.T) {
	dir := useTempHome(t)

	ForgetLastContext()

	if _, err := os.Stat(filepath.Join(dir, ".radar", "desktop-state.json")); !os.IsNotExist(err) {
		t.Errorf("desktop-state.json exists after clearing nothing (err=%v)", err)
	}
}
