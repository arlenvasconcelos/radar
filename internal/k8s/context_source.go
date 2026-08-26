package k8s

import (
	"log"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/skyhook-io/radar/internal/errorlog"
)

// ContextRef identifies a kubeconfig context precisely enough to survive a
// restart: the name Radar displays, plus the file it came from and the name it
// carries inside that file.
//
// The name alone is ambiguous once more than one kubeconfig is loaded. Two
// files can define the same context name, and which one keeps the unqualified
// form depends on the order discoverKubeconfigs walks the directory — so
// adding a file can silently reassign the name to a different cluster. A
// caller persisting a context across restarts must carry the file too.
//
// SourceFile + InFileName are the identity; Name is the label Radar shows. A
// ref carrying only a Name resolves to nothing, because a name is exactly the
// thing another file can take over.
type ContextRef struct {
	Name       string
	SourceFile string
	InFileName string
}

// Empty reports whether the ref names nothing to resolve.
func (r ContextRef) Empty() bool {
	return r.SourceFile == "" || r.InFileName == ""
}

// canonicalKubeconfigPath resolves a path so the same file compares equal
// across restarts: --kubeconfig-dir records relative entries, and a relative
// path can also match a *different* file reached from another directory. Abs
// rather than EvalSymlinks — the latter errors on a since-deleted file.
func canonicalKubeconfigPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// ContextSourceFor returns the full reference for a context Radar currently
// knows, so callers persisting it can record where it came from. Outside the
// registry there is only one kubeconfig loaded, and it is the only place the
// active context can have come from.
func ContextSourceFor(name string) ContextRef {
	clientMu.RLock()
	defer clientMu.RUnlock()

	if entry, ok := contextRegistry[name]; ok {
		return ContextRef{Name: name, SourceFile: canonicalKubeconfigPath(entry.SourceFile), InFileName: entry.InFileName}
	}
	if name != "" && name == contextName {
		if path := singleLoadedKubeconfig(); path != "" {
			return ContextRef{Name: name, SourceFile: canonicalKubeconfigPath(path), InFileName: name}
		}
	}
	return ContextRef{Name: name}
}

// ContextReferenceKnownMissing reports whether Radar successfully loaded the
// recorded kubeconfig file and that file no longer defines the recorded
// context. A false result is intentionally inconclusive: the file may only be
// temporarily unavailable or outside this run's configured kubeconfig set.
func ContextReferenceKnownMissing(ref ContextRef) bool {
	if ref.Empty() {
		return false
	}

	wantFile := canonicalKubeconfigPath(ref.SourceFile)
	clientMu.RLock()
	defer clientMu.RUnlock()

	if contextRegistry != nil {
		for path, cfg := range perFileConfigs {
			if canonicalKubeconfigPath(path) != wantFile {
				continue
			}
			_, exists := cfg.Contexts[ref.InFileName]
			return !exists
		}
		return false
	}

	path := singleLoadedKubeconfig()
	if path == "" || canonicalKubeconfigPath(path) != wantFile {
		return false
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return false
	}
	_, exists := cfg.Contexts[ref.InFileName]
	return !exists
}

// singleLoadedKubeconfig returns the one kubeconfig backing this process, or ""
// when several are loaded (the registry answers there) or none is (in-cluster).
// --kubeconfig-dir records its find in kubeconfigPaths even when it finds
// exactly one file, so both globals have to be consulted. Callers must hold
// clientMu.
func singleLoadedKubeconfig() string {
	if kubeconfigPath != "" {
		return kubeconfigPath
	}
	if len(kubeconfigPaths) == 1 {
		return kubeconfigPaths[0]
	}
	return ""
}

// IsEphemeralContext reports whether a context lives in a temp kubeconfig Radar
// wrote itself for a CAPI workload cluster. That file is gone on the next run,
// so callers that persist a context across restarts must skip those.
func IsEphemeralContext(name string) bool {
	clientMu.RLock()
	defer clientMu.RUnlock()

	entry, ok := contextRegistry[name]
	if !ok {
		return false
	}
	for _, tmpPath := range capiKubeconfigs {
		if tmpPath == entry.SourceFile {
			return true
		}
	}
	return false
}

// applyContextPreference points overrides at the preferred context when the
// kubeconfig at path is the file it was recorded from and still defines it.
// Validating first matters twice: a context that has since been renamed or
// deleted would otherwise fail the whole startup, and the override has to be in
// place before the deferred loader builds its inner config — it captures
// CurrentContext on the first RawConfig()/ClientConfig() call and caches it.
func applyContextPreference(path string, preferred ContextRef, overrides *clientcmd.ConfigOverrides) {
	if preferred.Empty() || canonicalKubeconfigPath(preferred.SourceFile) != canonicalKubeconfigPath(path) {
		reportContextPreferenceMiss(preferred)
		return
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		reportContextPreferenceMiss(preferred)
		return
	}
	if _, ok := cfg.Contexts[preferred.InFileName]; !ok {
		reportContextPreferenceMiss(preferred)
		return
	}
	overrides.CurrentContext = preferred.InFileName
}

// reportContextPreferenceMiss explains why Radar did not come up where the last
// session left it. Falling back to current-context is the safe answer — the
// name alone is what another kubeconfig can take over — but a silent redirect
// leaves the user staring at a cluster they didn't pick, so it goes to the
// diagnostics surface and not only to the log.
func reportContextPreferenceMiss(preferred ContextRef) {
	name := preferred.Name
	if name == "" {
		name = preferred.InFileName
	}
	if name == "" {
		return
	}
	log.Printf("[k8s-init] last used context %q not found where it was recorded; using current-context", name)
	errorlog.Record("k8s-init", "warning",
		"could not reopen on %q: that context is no longer in the kubeconfig it was recorded from. Starting on the kubeconfig's current-context instead.",
		name)
}
