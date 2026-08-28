package server

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/images"
	rcpkg "github.com/skyhook-io/radar/pkg/remotecommand"
)

// PodFilesystem represents the file listing response for a pod container
type PodFilesystem struct {
	Root       *images.FileNode `json:"root"`
	TotalFiles int              `json:"totalFiles"`
}

// handlePodFileList lists files at a given path inside a pod container.
// GET /api/pods/{ns}/{name}/files?container=X&path=/
func (s *Server) handlePodFileList(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/"
	}

	// Clean the path to prevent traversal.
	// Use path.Clean (POSIX) not filepath.Clean — the path runs inside a Linux
	// container, but filepath is OS-specific and converts to backslashes on Windows.
	dirPath = path.Clean(dirPath)

	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	// Use find to list files — provides type, size, timestamp, permissions
	// -maxdepth 1 lists only immediate children (like ls)
	// Output format: type\tsize\ttimestamp\tpermissions\tpath
	// Wrap in sh -c so the shell resolves PATH — direct exec via the container
	// runtime can fail to find binaries that are available through the shell's PATH.
	findCmd := fmt.Sprintf("find %s -maxdepth 1 -printf '%%y\\t%%s\\t%%T@\\t%%m\\t%%p\\n'", shellQuote(dirPath))
	cmd := []string{"/bin/sh", "-c", findCmd}

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		log.Printf("[copy] Failed to create executor for %s/%s: %v", namespace, podName, err)
		errorlog.Record("copy", "error", "failed to create executor for %s/%s: %v", namespace, podName, err)
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create executor: %v", err))
		return
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		// find failed — could be missing command, unsupported flags (e.g. -printf on BusyBox), etc.
		// Always fall back to ls which is more universally available.
		findErrMsg := fmt.Sprintf("%v: %s", err, stderr.String())
		log.Printf("[copy] find failed for %s/%s (falling back to ls): %s", namespace, podName, findErrMsg)
		nodes, totalFiles, lsErr := s.listFilesWithLS(r, namespace, podName, container, dirPath)
		if lsErr != nil {
			errMsg := classifyExecError(findErrMsg, lsErr.Error())
			errorlog.Record("copy", "error", "file list failed for %s/%s: %s", namespace, podName, errMsg)
			s.writeError(w, http.StatusInternalServerError, errMsg)
			return
		}
		s.writeJSON(w, PodFilesystem{Root: buildRootNode(dirPath, nodes), TotalFiles: totalFiles})
		return
	}

	nodes, totalFiles := parseFindOutput(stdout.String(), dirPath)
	s.writeJSON(w, PodFilesystem{Root: buildRootNode(dirPath, nodes), TotalFiles: totalFiles})
}

// listFilesWithLS is a fallback when find is not available
func (s *Server) listFilesWithLS(r *http.Request, namespace, podName, container, dirPath string) ([]*images.FileNode, int, error) {
	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		return nil, 0, fmt.Errorf("cluster client not available")
	}

	cmd := []string{"/bin/sh", "-c", fmt.Sprintf("ls -la %s", shellQuote(dirPath))}

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		return nil, 0, err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("%v: %s", err, stderr.String())
	}

	nodes := parseLSOutput(stdout.String(), dirPath)
	return nodes, len(nodes), nil
}

// handlePodFileDownload downloads a single file from a pod container.
// GET /api/pods/{ns}/{name}/files/download?container=X&path=/some/file
func (s *Server) handlePodFileDownload(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		s.writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	filePath = path.Clean(filePath)
	fileName := path.Base(filePath)

	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	// First try: tar cf - to stream the file (handles binary files correctly).
	// The stream is extracted and written to the response as it arrives —
	// buffering a whole file would put arbitrarily large files in memory, and
	// exec transports drop long transfers anyway (see resumingTarStream).
	dir := path.Dir(filePath)
	base := path.Base(filePath)

	runAttempt := func(ctx context.Context, offset uint64, stdout io.Writer) error {
		var cmd []string
		if offset == 0 {
			cmd = []string{"tar", "cf", "-", "-C", dir, base}
		} else {
			// tail -c+N is 1-based: resume at the first byte not yet delivered.
			// The header block stays 512 bytes however large the size field
			// says the file is, so a re-exec emits an identical byte stream
			// from 512 onward for as long as the file's existing bytes are
			// unchanged — an appended-to log still splices correctly. This is
			// the trick kubectl cp --retries relies on. A file rotated or
			// rewritten mid-transfer splices two different archives, which
			// nothing downstream can detect.
			cmd = []string{"/bin/sh", "-c", fmt.Sprintf("tar cf - -C %s %s | tail -c+%d", shellQuote(dir), shellQuote(base), offset+1)}
		}

		req := client.CoreV1().RESTClient().Post().
			Resource("pods").
			Name(podName).
			Namespace(namespace).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: container,
				Command:   cmd,
				Stdout:    true,
				Stderr:    true,
			}, scheme.ParameterCodec)

		exec, err := rcpkg.NewExecutor(config, req.URL())
		if err != nil {
			return fmt.Errorf("failed to create executor: %w", err)
		}

		var stderr bytes.Buffer
		if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: stdout,
			Stderr: &stderr,
		}); err != nil {
			return fmt.Errorf("%w %s", err, stderr.String())
		}
		return nil
	}

	stream := newResumingTarStream(r.Context(), runAttempt, tarStreamMaxRetries)
	defer stream.Close()

	// Extract the file from the tar stream
	tr := tar.NewReader(stream)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("File not found in tar stream: %s", filePath))
			return
		}
		if err != nil {
			errMsg := err.Error()
			if isCommandNotFound(errMsg) {
				// tar not available — fallback to cat
				catContent, catErr := s.downloadWithCat(r, namespace, podName, container, filePath)
				if catErr != nil {
					if isCommandNotFound(catErr.Error()) {
						s.writeError(w, http.StatusInternalServerError, "Container lacks 'tar' and 'cat' commands. Cannot download files from distroless containers.")
					} else {
						log.Printf("[copy] cat fallback failed for %s/%s path=%s: %v", namespace, podName, filePath, catErr)
						errorlog.Record("copy", "error", "file download failed for %s/%s: %v", namespace, podName, catErr)
						s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to download file: %v", catErr))
					}
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
				w.Header().Set("Content-Length", strconv.Itoa(len(catContent)))
				w.Write(catContent)
				return
			}
			if strings.Contains(errMsg, "No such file") || strings.Contains(errMsg, "not found") {
				s.writeError(w, http.StatusNotFound, fmt.Sprintf("File not found: %s", filePath))
				return
			}
			log.Printf("[copy] exec tar failed for %s/%s path=%s: %v", namespace, podName, filePath, err)
			errorlog.Record("copy", "error", "file download failed for %s/%s path=%s: %v", namespace, podName, filePath, err)
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to download file: %v", err))
			return
		}

		if header.Typeflag == tar.TypeReg {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
			w.Header().Set("Content-Length", strconv.FormatInt(header.Size, 10))
			if n, err := io.CopyN(w, tr, header.Size); err != nil {
				// Headers are already sent; ending the body short of
				// Content-Length makes the client fail the download visibly.
				log.Printf("[copy] streaming %s from %s/%s aborted after %d of %d bytes: %v", filePath, namespace, podName, n, header.Size, err)
				errorlog.Record("copy", "error", "file download for %s/%s path=%s aborted after %d of %d bytes: %v", namespace, podName, filePath, n, header.Size, err)
			}
			return
		}
	}
}

// tarStreamMaxRetries bounds how many times a dropped tar stream is resumed.
const tarStreamMaxRetries = 10

// execTarAttempt runs one tar exec attempt, writing the tar byte stream
// starting at the given offset (bytes already delivered) to stdout, and
// returns once the exec finishes.
type execTarAttempt func(ctx context.Context, offset uint64, stdout io.Writer) error

// resumingTarStream reads a pod's tar output across exec attempts,
// transparently resuming after mid-stream drops. Exec transports routinely
// kill long transfers (LBs, tunnels, apiserver proxies) — sometimes as a
// normal close, which client-go surfaces as a successful exec with truncated
// output. This is the failure kubectl cp works around with --retries, and the
// resume mechanism here is modeled on its TarPipe.
//
// A premature clean EOF is indistinguishable from real completion at this
// layer, so EOF also triggers a resume. Consumers must therefore stop reading
// once they have what they need — the download handler stops after the file
// entry and never reads the archive trailer.
type resumingTarStream struct {
	ctx        context.Context
	run        execTarAttempt
	maxRetries int
	reader     *io.PipeReader
	bytesRead  uint64
	retries    int
}

func newResumingTarStream(ctx context.Context, run execTarAttempt, maxRetries int) *resumingTarStream {
	t := &resumingTarStream{ctx: ctx, run: run, maxRetries: maxRetries}
	t.startFrom(0)
	return t
}

func (t *resumingTarStream) startFrom(offset uint64) {
	pr, pw := io.Pipe()
	t.reader = pr
	go func() {
		pw.CloseWithError(t.run(t.ctx, offset, pw))
	}()
}

func (t *resumingTarStream) Read(p []byte) (int, error) {
	for {
		n, err := t.reader.Read(p)
		if n > 0 {
			t.bytesRead += uint64(n)
			return n, nil
		}
		if err == nil {
			continue
		}
		if t.ctx.Err() != nil {
			return 0, err
		}
		// A first-attempt failure before any data propagates unchanged: the
		// caller needs the raw error to classify it (cat fallback, 404), and
		// nothing has been delivered, so a client retry is free.
		if t.bytesRead == 0 && t.retries == 0 {
			return 0, err
		}
		if t.retries >= t.maxRetries {
			return 0, err
		}
		t.retries++
		log.Printf("[copy] tar stream interrupted, resuming at %d bytes (retry %d/%d): %v", t.bytesRead, t.retries, t.maxRetries, err)
		t.startFrom(t.bytesRead)
	}
}

// Close unblocks the current attempt's writer; the exec attempt itself is
// stopped by the request context.
func (t *resumingTarStream) Close() error {
	return t.reader.CloseWithError(context.Canceled)
}

// downloadWithCat is a fallback when tar is not available
func (s *Server) downloadWithCat(r *http.Request, namespace, podName, container, filePath string) ([]byte, error) {
	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		return nil, fmt.Errorf("cluster client not available")
	}

	cmd := []string{"cat", filePath}

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		return nil, err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// parseFindOutput parses the output of find -printf '%y\t%s\t%T@\t%m\t%p\n'
func parseFindOutput(output, dirPath string) ([]*images.FileNode, int) {
	var nodes []*images.FileNode
	totalFiles := 0

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}

		fileType := parts[0]
		sizeStr := parts[1]
		perms := parts[3]
		filePath := parts[4]

		// Skip the directory itself
		if filePath == dirPath || filePath == "." {
			continue
		}

		size, _ := strconv.ParseInt(sizeStr, 10, 64)

		var nodeType string
		switch fileType {
		case "d":
			nodeType = "dir"
		case "l":
			nodeType = "symlink"
		default:
			nodeType = "file"
		}

		node := &images.FileNode{
			Name:        path.Base(filePath),
			Path:        filePath,
			Type:        nodeType,
			Size:        size,
			Permissions: formatOctalPerms(perms),
		}

		nodes = append(nodes, node)
		totalFiles++
	}

	sortFileNodes(nodes)
	return nodes, totalFiles
}

// parseLSOutput parses `ls -la` output as a fallback
func parseLSOutput(output, dirPath string) []*images.FileNode {
	var nodes []*images.FileNode

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}

		// ls -la output: permissions links owner group size month day time name [-> target]
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}

		perms := fields[0]
		sizeStr := fields[4]
		name := fields[8]

		// Skip . and ..
		if name == "." || name == ".." {
			continue
		}

		size, _ := strconv.ParseInt(sizeStr, 10, 64)

		var nodeType string
		var linkTarget string
		switch {
		case perms[0] == 'd':
			nodeType = "dir"
		case perms[0] == 'l':
			nodeType = "symlink"
			// Extract link target (after "->")
			for i, f := range fields {
				if f == "->" && i+1 < len(fields) {
					linkTarget = strings.Join(fields[i+1:], " ")
					break
				}
			}
		default:
			nodeType = "file"
		}

		nodePath := path.Join(dirPath, name)

		node := &images.FileNode{
			Name:        name,
			Path:        nodePath,
			Type:        nodeType,
			Size:        size,
			Permissions: perms,
			LinkTarget:  linkTarget,
		}

		nodes = append(nodes, node)
	}

	sortFileNodes(nodes)
	return nodes
}

// buildRootNode wraps file nodes in a root directory node
func buildRootNode(dirPath string, children []*images.FileNode) *images.FileNode {
	return &images.FileNode{
		Name:     path.Base(dirPath),
		Path:     dirPath,
		Type:     "dir",
		Children: children,
	}
}

// sortFileNodes sorts directories first, then files, alphabetically
func sortFileNodes(nodes []*images.FileNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Type == "dir" && nodes[j].Type != "dir" {
			return true
		}
		if nodes[i].Type != "dir" && nodes[j].Type == "dir" {
			return false
		}
		return nodes[i].Name < nodes[j].Name
	})
}

// formatOctalPerms converts octal permission string (e.g., "755") to rwx format
func formatOctalPerms(octal string) string {
	if len(octal) < 3 {
		return octal
	}

	// Take last 3 digits
	if len(octal) > 3 {
		octal = octal[len(octal)-3:]
	}

	rwx := func(digit byte) string {
		n := digit - '0'
		r := "-"
		w := "-"
		x := "-"
		if n&4 != 0 {
			r = "r"
		}
		if n&2 != 0 {
			w = "w"
		}
		if n&1 != 0 {
			x = "x"
		}
		return r + w + x
	}

	return rwx(octal[0]) + rwx(octal[1]) + rwx(octal[2])
}

// classifyExecError analyzes errors from both find and ls exec attempts and returns
// a user-friendly message that identifies the actual problem rather than always
// blaming missing commands.
func classifyExecError(findErr, lsErr string) string {
	combined := strings.ToLower(findErr + " " + lsErr)

	// Check for permission denied
	if strings.Contains(combined, "permission denied") || strings.Contains(combined, "operation not permitted") {
		return "Permission denied: the container user lacks access to this directory. Try a different path or container."
	}

	// Check for shell not found (distroless containers).
	// Some runtimes report "executable file not found", others report
	// "/bin/sh: no such file or directory" — catch both forms.
	if strings.Contains(combined, "executable file not found") && (strings.Contains(combined, "sh") || strings.Contains(combined, "shell")) {
		return "Container has no shell (/bin/sh). This is likely a distroless or scratch-based container that cannot be browsed."
	}
	if strings.Contains(combined, "/bin/sh") && strings.Contains(combined, "no such file or directory") {
		return "Container has no shell (/bin/sh). This is likely a distroless or scratch-based container that cannot be browsed."
	}

	// Check for both commands genuinely missing
	findMissing := isCommandNotFound(findErr)
	lsMissing := isCommandNotFound(lsErr)
	if findMissing && lsMissing {
		return "Container lacks 'find' and 'ls' commands. This container may be distroless or minimal."
	}

	// Check for no such file or directory (path doesn't exist)
	if strings.Contains(combined, "no such file or directory") && !findMissing && !lsMissing {
		return "Directory not found. The path may not exist in this container."
	}

	// Check for connection/network issues
	if strings.Contains(combined, "error dialing backend") || strings.Contains(combined, "connection refused") ||
		strings.Contains(combined, "transport closed") || strings.Contains(combined, "transport error") || strings.Contains(combined, "stream error") ||
		strings.Contains(combined, "websocket") || strings.Contains(combined, "upgrade") {
		return fmt.Sprintf("Failed to exec into container (connection error): %s", lsErr)
	}

	// Check for context deadline exceeded
	if strings.Contains(combined, "context deadline exceeded") || strings.Contains(combined, "context canceled") {
		return "Exec timed out. The container may be unresponsive or under heavy load."
	}

	// Default: include the actual ls error so users can diagnose
	return fmt.Sprintf("Failed to list files: %s", lsErr)
}

// shellQuote wraps a string in single quotes for safe use in sh -c commands.
// Single quotes inside the string are escaped by ending the quote, adding an
// escaped single quote, and re-opening the quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// isCommandNotFound detects errors indicating a command is not available in the container
func isCommandNotFound(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	patterns := []string{
		"executable file not found",
		"command not found",
		"not found in $path",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
