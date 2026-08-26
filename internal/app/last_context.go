package app

import (
	"log"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

// remembersLastContext reports whether this process may record the active
// cluster on disk and start on it next time. Only the Desktop app opts in —
// see AppConfig.RestoreLastContext.
//
// The auth and cloud-tunnel checks cannot fire today: cmd/desktop is the only
// entrypoint that sets RestoreLastContext, and it configures neither. They are
// kept as a standing guard on the invariant rather than as live branches — the
// remembered cluster is one user's pick, and the moment Desktop serves more
// than one viewer, recording it would let one person's switch steer everyone
// else's next start. Delete them only together with that invariant.
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
	saved := settings.Load().LastDesktopContext
	if saved == nil {
		return k8s.ContextRef{}
	}
	return k8s.ContextRef{
		Name:       saved.Name,
		SourceFile: saved.SourceFile,
		InFileName: saved.InFileName,
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
	if settings.Load().LastDesktopContext == nil {
		return
	}
	if _, err := settings.Update(func(st *settings.Settings) {
		st.LastDesktopContext = nil
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
	if _, err := settings.Update(func(st *settings.Settings) {
		st.LastDesktopContext = &settings.LastContext{
			Name:       ref.Name,
			SourceFile: ref.SourceFile,
			InFileName: ref.InFileName,
		}
	}); err != nil {
		log.Printf("[context] failed to remember last used context %q: %v", name, err)
	}
}
