package k8s

import (
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// `--kubeconfig-dir ./configs` puts a relative path in the registry, so the
// recorded ref and the live entry spell the same file two different ways.
func TestPreferredContextMatchesAbsoluteRefAgainstRelativeRegistryPath(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)

	registry := map[string]contextEntry{
		"alpha": {SourceFile: filepath.Join("configs", "a.yaml"), InFileName: "alpha"},
	}
	recorded := ContextRef{
		Name:       "alpha",
		SourceFile: filepath.Join(home, "configs", "a.yaml"), // as ContextSourceFor stores it
		InFileName: "alpha",
	}

	if _, _, ok := matchPreferred(registry, recorded); !ok {
		t.Error("a ref recorded as an absolute path did not match the same file held relatively")
	}
}

func TestPreferredContextDoesNotResolveRelativePathFromAnotherWorkingDirectory(t *testing.T) {
	recordedFrom := t.TempDir()
	loadedFrom := t.TempDir()
	recorded := ContextRef{
		Name:       "alpha",
		SourceFile: filepath.Join(recordedFrom, "configs", "a.yaml"),
		InFileName: "alpha",
	}

	t.Chdir(loadedFrom)
	registry := map[string]contextEntry{
		"alpha": {SourceFile: filepath.Join("configs", "a.yaml"), InFileName: "alpha"},
	}

	if _, _, ok := matchPreferred(registry, recorded); ok {
		t.Error("a relative path from another working directory matched a different file")
	}
}

// Recording resolved is what stops the ambiguity: a path stored relatively
// would compare equal to a different file in another directory, and nothing
// can disambiguate it after the fact — the cwd that produced it is gone.
func TestContextSourceForRecordsAResolvedPath(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	t.Cleanup(SetTestRegistryEntry("alpha", filepath.Join("configs", "a.yaml"), "alpha"))

	got := ContextSourceFor("alpha")

	if !filepath.IsAbs(got.SourceFile) {
		t.Errorf("recorded SourceFile = %q, want an absolute path", got.SourceFile)
	}
	if want := filepath.Join(home, "configs", "a.yaml"); got.SourceFile != want {
		t.Errorf("recorded SourceFile = %q, want %q", got.SourceFile, want)
	}
}

// Same property on the single-kubeconfig path, which compares against the file
// doInit is about to load rather than against a registry.
func TestApplyContextPreferenceComparesResolvedPaths(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeKubeconfig(t, dir, "config", "alpha", []kubeEntry{
		{ctxName: "alpha", userName: "ua", clusterName: "cluster-alpha"},
		{ctxName: "beta", userName: "ub", clusterName: "cluster-beta"},
	})

	recorded := ContextRef{
		Name:       "beta",
		SourceFile: filepath.Join(dir, "config"), // absolute, as recorded
		InFileName: "beta",
	}

	overrides := &clientcmd.ConfigOverrides{}
	applyContextPreference("config", recorded, overrides) // relative, as loaded

	if overrides.CurrentContext != "beta" {
		t.Errorf("CurrentContext = %q, want %q: the same file spelled two ways did not match",
			overrides.CurrentContext, "beta")
	}
}
