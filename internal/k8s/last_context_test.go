package k8s

import (
	"path/filepath"
	"strings"
	"testing"
)

// restoreClientGlobals snapshots the package state doInit writes and puts it
// back afterwards, so these tests can run a real init without leaking a fake
// cluster into sibling tests.
func restoreClientGlobals(t *testing.T) {
	t.Helper()
	clientMu.Lock()
	var (
		savedPath       = kubeconfigPath
		savedPaths      = kubeconfigPaths
		savedMode       = kubeconfigMode
		savedRegistry   = contextRegistry
		savedConfigs    = perFileConfigs
		savedMtimes     = perFileMtimes
		savedContext    = contextName
		savedCluster    = clusterName
		savedNamespace  = contextNamespace
		savedUsesExec   = contextUsesExec
		savedTotal      = totalContextCount
		savedExecCmds   = execPluginCommands
		savedClient     = k8sClient
		savedConfig     = k8sConfig
		savedDiscovery  = discoveryClient
		savedDynamic    = dynamicClient
		savedGeneration = activeClientGeneration
	)
	clientMu.Unlock()

	t.Cleanup(func() {
		clientMu.Lock()
		defer clientMu.Unlock()
		kubeconfigPath = savedPath
		kubeconfigPaths = savedPaths
		kubeconfigMode = savedMode
		contextRegistry = savedRegistry
		perFileConfigs = savedConfigs
		perFileMtimes = savedMtimes
		contextName = savedContext
		clusterName = savedCluster
		contextNamespace = savedNamespace
		contextUsesExec = savedUsesExec
		totalContextCount = savedTotal
		execPluginCommands = savedExecCmds
		k8sClient = savedClient
		k8sConfig = savedConfig
		discoveryClient = savedDiscovery
		dynamicClient = savedDynamic
		activeClientGeneration = savedGeneration
	})
}

func TestDoInitPrefersRequestedContext(t *testing.T) {
	restoreClientGlobals(t)
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", "alpha", []kubeEntry{
		{ctxName: "alpha", userName: "ua", clusterName: "cluster-alpha", namespace: "ns-alpha"},
		{ctxName: "beta", userName: "ub", clusterName: "cluster-beta", namespace: "ns-beta"},
	})

	saved := ContextRef{Name: "beta", SourceFile: path, InFileName: "beta"}
	if err := doInit(InitOptions{KubeconfigPath: path, PreferredContext: saved}); err != nil {
		t.Fatalf("doInit() error = %v", err)
	}

	if got := GetContextName(); got != "beta" {
		t.Errorf("GetContextName() = %q, want %q", got, "beta")
	}
	if got := GetContextNamespace(); got != "ns-beta" {
		t.Errorf("GetContextNamespace() = %q, want %q", got, "ns-beta")
	}
	// The bookkeeping and the client must agree: a context name that says
	// "beta" while the REST config still dials alpha is the failure mode
	// this preference has to avoid.
	if host := GetConfig().Host; !strings.Contains(host, "cluster-beta") {
		t.Errorf("rest config Host = %q, want it to point at cluster-beta", host)
	}
	if ContextReferenceKnownMissing(saved) {
		t.Error("ContextReferenceKnownMissing(saved) = true for a context that resolved")
	}
}

func TestDoInitFallsBackWhenPreferredContextMissing(t *testing.T) {
	restoreClientGlobals(t)
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", "alpha", []kubeEntry{
		{ctxName: "alpha", userName: "ua", clusterName: "cluster-alpha", namespace: "ns-alpha"},
	})

	saved := ContextRef{Name: "ghost", SourceFile: path, InFileName: "ghost"}
	if err := doInit(InitOptions{KubeconfigPath: path, PreferredContext: saved}); err != nil {
		t.Fatalf("doInit() error = %v", err)
	}

	if got := GetContextName(); got != "alpha" {
		t.Errorf("GetContextName() = %q, want the kubeconfig current-context %q", got, "alpha")
	}
	if host := GetConfig().Host; !strings.Contains(host, "cluster-alpha") {
		t.Errorf("rest config Host = %q, want it to point at cluster-alpha", host)
	}
	if !ContextReferenceKnownMissing(saved) {
		t.Error("ContextReferenceKnownMissing(saved) = false after the loaded file proved the context is gone")
	}
}

func TestContextReferenceKnownMissingKeepsAnUnavailableFileInconclusive(t *testing.T) {
	restoreClientGlobals(t)
	dir := t.TempDir()
	loaded := writeKubeconfig(t, dir, "config", "alpha", []kubeEntry{
		{ctxName: "alpha", userName: "ua", clusterName: "cluster-alpha"},
	})
	if err := doInit(InitOptions{KubeconfigPath: loaded}); err != nil {
		t.Fatalf("doInit() error = %v", err)
	}

	ref := ContextRef{Name: "prod", SourceFile: filepath.Join(dir, "unavailable"), InFileName: "prod"}
	if ContextReferenceKnownMissing(ref) {
		t.Error("ContextReferenceKnownMissing(ref) = true for a file this run never loaded")
	}
}

func TestIsEphemeralContextSingleKubeconfig(t *testing.T) {
	restoreClientGlobals(t)
	clientMu.Lock()
	kubeconfigPath = "/home/user/.kube/config"
	kubeconfigPaths = nil
	contextRegistry = nil
	clientMu.Unlock()

	if IsEphemeralContext("prod") {
		t.Error("IsEphemeralContext(prod) = true, want false for a context from the user's kubeconfig")
	}
}

func TestIsEphemeralContextReportsCAPIContext(t *testing.T) {
	restoreClientGlobals(t)
	dir := t.TempDir()
	durable := writeKubeconfig(t, dir, "durable.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "u", clusterName: "c"},
	})
	temp := writeKubeconfig(t, dir, "radar-capi-kubeconfig-1234.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u", clusterName: "c"},
	})

	clientMu.Lock()
	kubeconfigPath = ""
	kubeconfigPaths = []string{durable, temp}
	contextRegistry = map[string]contextEntry{
		"prod":     {SourceFile: durable, InFileName: "prod"},
		"workload": {SourceFile: temp, InFileName: "workload"},
	}
	savedCAPI := capiKubeconfigs
	capiKubeconfigs = map[string]string{"workload": temp}
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		capiKubeconfigs = savedCAPI
		clientMu.Unlock()
	})

	if IsEphemeralContext("prod") {
		t.Error("IsEphemeralContext(prod) = true for a durable kubeconfig")
	}
	if !IsEphemeralContext("workload") {
		t.Error("IsEphemeralContext(workload) = false for a CAPI temp kubeconfig")
	}
}

// --kubeconfig-dir records its find in kubeconfigPaths even when it discovers
// exactly one file, and builds no registry because there is nothing to
// disambiguate. Without consulting both globals the source file would never be
// recorded, and a restore that requires an exact match could never resolve.
func TestContextSourceForRecordsTheFileFoundInAKubeconfigDir(t *testing.T) {
	restoreClientGlobals(t)
	clientMu.Lock()
	kubeconfigPath = ""
	kubeconfigPaths = []string{"/home/user/.kube/configs/prod.yaml"}
	contextRegistry = nil
	contextName = "prod"
	clientMu.Unlock()

	got := ContextSourceFor("prod")
	if got.SourceFile != "/home/user/.kube/configs/prod.yaml" || got.InFileName != "prod" {
		t.Errorf("ContextSourceFor(prod) = %+v, want the discovered file recorded", got)
	}
	if got.Empty() {
		t.Error("ref is not resolvable, so the memory could never be restored")
	}
}

func TestContextSourceForRecordsTheSingleKubeconfig(t *testing.T) {
	restoreClientGlobals(t)
	clientMu.Lock()
	kubeconfigPath = "/home/user/.kube/config"
	kubeconfigPaths = nil
	contextRegistry = nil
	contextName = "prod"
	clientMu.Unlock()

	got := ContextSourceFor("prod")
	if got.SourceFile != "/home/user/.kube/config" || got.InFileName != "prod" {
		t.Errorf("ContextSourceFor(prod) = %+v, want the loaded kubeconfig recorded", got)
	}
}

// Several files loaded means the registry is the only thing that knows which
// one a context came from — no single-file guess applies.
func TestContextSourceForLeavesNoFileWhenSeveralAreLoaded(t *testing.T) {
	restoreClientGlobals(t)
	clientMu.Lock()
	kubeconfigPath = ""
	kubeconfigPaths = []string{"/a.yaml", "/b.yaml"}
	contextRegistry = map[string]contextEntry{}
	contextName = "prod"
	clientMu.Unlock()

	if got := ContextSourceFor("prod"); !got.Empty() {
		t.Errorf("ContextSourceFor(prod) = %+v, want an unresolvable ref rather than a guess", got)
	}
}
