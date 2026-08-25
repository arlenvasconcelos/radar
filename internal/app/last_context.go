package app

import (
	"log"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

// remembersLastContext reports whether this process may record the active
// cluster on disk and start on it next time. Only the Desktop app opts in —
// see AppConfig.RestoreLastContext. The auth and cloud-tunnel checks are
// belt-and-braces for the day Desktop grows either: there the kubeconfig
// context is shared state, and one user's switch must not steer everyone
// else's next start.
func remembersLastContext(cfg AppConfig) bool {
	return cfg.RestoreLastContext && !cfg.AuthConfig.Enabled() && !cfg.CloudTunnelConfigured
}

// startupContextPreference resolves which context this run starts on: the one
// the last session ended on, or an empty ref to keep the kubeconfig's
// current-context.
func startupContextPreference(cfg AppConfig) k8s.ContextRef {
	if !remembersLastContext(cfg) {
		return k8s.ContextRef{}
	}
	// The error matters: a store we failed to read must not look like "the
	// user never picked a cluster".
	saved, err := settings.LoadDesktopState()
	if err != nil || saved.LastContext == nil {
		return k8s.ContextRef{}
	}
	return k8s.ContextRef{
		Name:       saved.LastContext.Name,
		SourceFile: saved.LastContext.SourceFile,
		InFileName: saved.LastContext.InFileName,
	}
}

// RegisterLastContextMemory records every successful context switch so the next
// start comes back on the cluster the user was working in. Recording the switch
// rather than the exit is deliberate — a force-quit or crash would otherwise
// lose the pick.
func RegisterLastContextMemory(cfg AppConfig) {
	if !remembersLastContext(cfg) {
		return
	}
	k8s.OnContextSwitch(func(name string) {
		persistLastContext(cfg, name)
	})
}

// ForgetLastContext drops the remembered cluster, so turning the memory off and
// back on later doesn't reopen a cluster the user stopped using long ago.
func ForgetLastContext() {
	saved, err := settings.LoadDesktopState()
	if err != nil || saved.LastContext == nil {
		return
	}
	if _, err := settings.UpdateDesktopState(func(st *settings.DesktopState) {
		st.LastContext = nil
	}); err != nil {
		log.Printf("[context] failed to clear the remembered context: %v", err)
	}
}

func persistLastContext(cfg AppConfig, name string) {
	if name == "" || !remembersLastContext(cfg) {
		return
	}
	// A CAPI workload cluster lives in a temp kubeconfig that's gone next run,
	// so its context could never be restored.
	if k8s.IsEphemeralContext(name) {
		return
	}
	// Record the file too: across multiple kubeconfigs the display name alone
	// can be reassigned to another file's context between runs.
	ref := k8s.ContextSourceFor(name)
	if _, err := settings.UpdateDesktopState(func(st *settings.DesktopState) {
		st.LastContext = &settings.LastContext{
			Name:       ref.Name,
			SourceFile: ref.SourceFile,
			InFileName: ref.InFileName,
		}
	}); err != nil {
		log.Printf("[context] failed to remember last used context %q: %v", name, err)
	}
}
