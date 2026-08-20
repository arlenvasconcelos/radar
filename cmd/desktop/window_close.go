package main

import "github.com/skyhook-io/radar/internal/config"

// hideWindowOnClose decides whether closing the main window hides the app
// instead of quitting it.
//
// macOS only. Wails maps this option to `[NSApp hide:]`, which leaves the dock
// icon in place, so a dock click or Cmd+Tab brings the window back. The other
// platforms have no such affordance: GTK hides the window on delete-event and
// Windows drops the taskbar entry, and Wails v2 ships no tray icon, so a hidden
// window there is only recoverable by killing the process — which is why
// QuitOnWindowClose can only ever turn this off, never on.
func hideWindowOnClose(goos string, cfg config.Config) bool {
	if goos != "darwin" {
		return false
	}
	return !cfg.QuitOnWindowCloseOr(false)
}
