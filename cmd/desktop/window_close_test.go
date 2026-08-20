package main

import (
	"testing"

	"github.com/skyhook-io/radar/internal/config"
)

func TestHideWindowOnClose(t *testing.T) {
	quit, stay := true, false

	cases := []struct {
		name string
		goos string
		cfg  config.Config
		want bool
	}{
		{
			"macOS hides the app so the dock icon can bring it back",
			"darwin", config.Config{}, true,
		},
		{
			"macOS honours an explicit opt-out back to quit-on-close",
			"darwin", config.Config{QuitOnWindowClose: &quit}, false,
		},
		{
			"macOS treats an explicit false as the default",
			"darwin", config.Config{QuitOnWindowClose: &stay}, true,
		},
		{
			"Linux quits: a hidden GTK window has no tray to restore it from",
			"linux", config.Config{}, false,
		},
		{
			"Linux ignores an opt-in that would strand the process with no window",
			"linux", config.Config{QuitOnWindowClose: &stay}, false,
		},
		{
			"Windows quits: hiding drops the taskbar entry with nothing to restore it",
			"windows", config.Config{}, false,
		},
		{
			"Windows ignores an opt-in that would strand the process with no window",
			"windows", config.Config{QuitOnWindowClose: &stay}, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hideWindowOnClose(tc.goos, tc.cfg); got != tc.want {
				t.Fatalf("hideWindowOnClose(%q, %+v) = %v, want %v", tc.goos, tc.cfg, got, tc.want)
			}
		})
	}
}
