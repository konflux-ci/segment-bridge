package testfixture

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Environment variables controlling optional container execution for script tests.
const (
	EnvTestImage        = "SEGMENT_BRIDGE_TEST_IMAGE"
	EnvContainerRuntime = "SEGMENT_BRIDGE_TEST_CONTAINER_RUNTIME"
	EnvKcovOutputDir    = "KCOV_OUTPUT_DIR"
	containerBinDir     = "/usr/local/bin"
	kubeconfigEnvVar    = "KUBECONFIG"
	envFileMode         = 0o600
)

// Basenames of scripts copied into the segment-bridge image (see Dockerfile).
var bundledScriptBaseNames = map[string]struct{}{
	"fetch-tekton-records.sh":     {},
	"fetch-konflux-op-records.sh": {},
	"fetch-namespace-records.sh":  {},
	"fetch-component-records.sh":  {},
	"get-konflux-public-info.sh":  {},
	"tekton-to-segment.sh":        {},
	"segment-uploader.sh":         {},
	"segment-mass-uploader.sh":    {},
	"mk-segment-batch-payload.sh": {},
	"tekton-main-job.sh":          {},
}

// WriteStub writes an executable shell script named name into dir with the
// given content. It is the shared implementation of the per-package writeStub
// helpers used by stub-based script tests.
func WriteStub(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteStub: %v", err)
	}
}

// WriteKubectlOcStubs writes identical kubectl and oc stubs into dir.
func WriteKubectlOcStubs(t *testing.T, dir, content string) {
	t.Helper()
	WriteStub(t, dir, "kubectl", content)
	WriteStub(t, dir, "oc", content)
}

// EnvWithStubPath returns a copy of os.Environ with stubDir prepended to PATH.
// The caller is responsible for setting KUBECONFIG and other script-specific
// variables on top of this base env.
func EnvWithStubPath(stubDir string) []string {
	env := os.Environ()
	result := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			result = append(result, "PATH="+stubDir+":"+strings.TrimPrefix(e, "PATH="))
		} else {
			result = append(result, e)
		}
	}
	return result
}

// MinimalHostEnvWithoutKubectl returns env for host script runs where bash must
// be on PATH (for kcov/shebang) but kubectl and oc should not be found.
// It builds a temp directory with symlinks to every executable in /bin and
// /usr/bin except kubectl and oc, so the test works even on CI runners where
// kubectl is installed system-wide.
func MinimalHostEnvWithoutKubectl(t *testing.T) []string {
	t.Helper()
	t.Setenv(EnvTestImage, "")
	stubDir := t.TempDir()

	excluded := map[string]struct{}{"kubectl": {}, "oc": {}}
	for _, dir := range []string{"/bin", "/usr/bin"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if _, skip := excluded[name]; skip {
				continue
			}
			dst := filepath.Join(stubDir, name)
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			_ = os.Symlink(filepath.Join(dir, name), dst)
		}
	}

	return []string{
		"PATH=" + stubDir,
		"HOME=" + stubDir,
	}
}

// ScriptBundledInBridgeImage reports whether basename matches a script installed under /usr/local/bin in the bridge image.
func ScriptBundledInBridgeImage(basename string) bool {
	_, ok := bundledScriptBaseNames[basename]
	return ok
}

// RunRepoScript runs a repository shell script on the host, or inside SEGMENT_BRIDGE_TEST_IMAGE when that variable is set.
// If env is nil, the child process inherits the current environment (host mode) or the full os.Environ() is passed into the container.
// stdin may be nil when the script does not read standard input.
func RunRepoScript(scriptPath string, stdin *os.File, env []string, args ...string) ([]byte, error) {
	image := strings.TrimSpace(os.Getenv(EnvTestImage))
	if image == "" {
		return runOnHost(scriptPath, stdin, env, args)
	}
	return runInContainer(scriptPath, stdin, env, args, image, nil)
}

// RunRepoScriptWithStderr is like RunRepoScript but also captures stderr
// separately so callers can assert on diagnostic output (e.g. warning messages).
// Both host and container modes capture and return stderr.
func RunRepoScriptWithStderr(scriptPath string, stdin *os.File, env []string, args ...string) (stdout, stderr []byte, err error) {
	image := strings.TrimSpace(os.Getenv(EnvTestImage))
	if image != "" {
		var stderrBuf bytes.Buffer
		out, runErr := runInContainer(scriptPath, stdin, env, args, image, &stderrBuf)
		return out, stderrBuf.Bytes(), runErr
	}
	return runOnHostWithStderr(scriptPath, stdin, env, args)
}

func runOnHost(scriptPath string, stdin *os.File, env []string, args []string) ([]byte, error) {
	execPath, execArgs := kcovWrap(scriptPath, args)
	cmd := exec.Command(execPath, execArgs...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error executing script: %w", err)
	}
	return out, nil
}

func runOnHostWithStderr(scriptPath string, stdin *os.File, env []string, args []string) ([]byte, []byte, error) {
	execPath, execArgs := kcovWrap(scriptPath, args)
	cmd := exec.Command(execPath, execArgs...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Env = env
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	out, err := cmd.Output()
	if err != nil {
		return nil, stderrBuf.Bytes(), fmt.Errorf("error executing script: %w", err)
	}
	return out, stderrBuf.Bytes(), nil
}

// kcovWrap prepends kcov to the command when KCOV_OUTPUT_DIR is set and kcov
// is installed. Multiple runs to the same output directory are auto-merged by
// kcov, producing a single Cobertura XML that Codecov can consume.
func kcovWrap(scriptPath string, args []string) (string, []string) {
	kcovDir := strings.TrimSpace(os.Getenv(EnvKcovOutputDir))
	if kcovDir == "" {
		return scriptPath, args
	}
	if _, err := exec.LookPath("kcov"); err != nil {
		return scriptPath, args
	}
	absScript, err := filepath.Abs(scriptPath)
	if err != nil {
		return scriptPath, args
	}
	kcovArgs := []string{
		"--include-path=" + filepath.Dir(absScript),
		kcovDir,
		absScript,
	}
	kcovArgs = append(kcovArgs, args...)
	return "kcov", kcovArgs
}

// runInContainer executes scriptPath inside the image. When stderrCapture is
// non-nil the container's stderr is written into it; otherwise it passes
// through to the test process's stderr.
func runInContainer(scriptPath string, stdin *os.File, env []string, args []string, image string, stderrCapture *bytes.Buffer) ([]byte, error) {
	base := filepath.Base(scriptPath)
	if _, ok := bundledScriptBaseNames[base]; !ok {
		return nil, fmt.Errorf("testfixture: %q is not a script bundled in the segment-bridge image (container mode)", base)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		return nil, fmt.Errorf("testfixture: script path: %w", err)
	}

	runtimePath, err := resolveContainerRuntime()
	if err != nil {
		return nil, fmt.Errorf("testfixture: container runtime: %w", err)
	}

	effectiveEnv := env
	if effectiveEnv == nil {
		effectiveEnv = os.Environ()
	}
	effectiveEnv = withContainerPathPrefix(effectiveEnv)
	envFile, err := writeContainerEnvFile(effectiveEnv)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(envFile) }()

	// Rootless podman maps the image USER (1001) to a different host UID, so a
	// 0600 kubeconfig owned by the test user is not readable in the container.
	// Relax permissions on the temp kubeconfig only for this mount.
	if kc := kubeconfigHostPathForMount(effectiveEnv); kc != "" {
		_ = os.Chmod(kc, 0o644)
	}

	entry := filepath.Join(containerBinDir, base)
	runArgs := []string{"run", "--rm", "--env-file", envFile}
	if runtime.GOOS == "linux" {
		runArgs = append(runArgs, "--network", "host")
	}

	volOpts := "ro"
	if runtime.GOOS == "linux" {
		volOpts = "ro,z"
	}

	stdinMountPath := ""
	if stdin != nil {
		stdinMountPath = regularFilePathForVolume(stdin)
		if stdinMountPath != "" {
			runArgs = append(runArgs, "-v", fmt.Sprintf("%s:%s:%s", stdinMountPath, stdinMountPath, volOpts))
		}
	}

	for _, vol := range kubeconfigVolumeArgs(effectiveEnv) {
		runArgs = append(runArgs, vol...)
	}

	var cmd *exec.Cmd
	switch {
	case stdin != nil && stdinMountPath != "":
		// Bind-mount the file and redirect on stdin inside the container. Attaching
		// large stdin to "podman run -i" can drop the tail of the stream on some
		// runners (e.g. GitHub Actions), yielding short script output.
		inner := fmt.Sprintf("exec %s%s < %s",
			quoteSingleForBash(entry),
			shellArgsQuoted(args),
			quoteSingleForBash(stdinMountPath))
		runArgs = append(runArgs, "--entrypoint", "/bin/bash", image, "-c", inner)
		cmd = exec.Command(runtimePath, runArgs...)
	default:
		runArgs = append(runArgs, "--entrypoint", entry)
		if stdin != nil {
			runArgs = append(runArgs, "-i")
		}
		runArgs = append(runArgs, image)
		runArgs = append(runArgs, args...)
		cmd = exec.Command(runtimePath, runArgs...)
		if stdin != nil {
			cmd.Stdin = stdin
		}
	}

	if stderrCapture != nil {
		cmd.Stderr = stderrCapture
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error executing script in container: %w", err)
	}
	return out, nil
}

func resolveContainerRuntime() (string, error) {
	if preferred := strings.TrimSpace(os.Getenv(EnvContainerRuntime)); preferred != "" {
		return exec.LookPath(preferred)
	}
	if p, err := exec.LookPath("podman"); err == nil {
		return p, nil
	}
	return exec.LookPath("docker")
}

// withContainerPathPrefix prepends image and standard locations to PATH so scripts find
// bundled kubectl/oc and /usr/bin/env even when the host PATH omits them (the container
// filesystem does not mirror the host).
func withContainerPathPrefix(env []string) []string {
	const prefix = "/usr/local/bin:/usr/bin:/bin"
	sep := string(os.PathListSeparator)
	out := make([]string, 0, len(env)+1)
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			v := strings.TrimPrefix(e, "PATH=")
			if v != "" {
				e = "PATH=" + prefix + sep + v
			} else {
				e = "PATH=" + prefix
			}
			found = true
		}
		out = append(out, e)
	}
	if !found {
		out = append(out, "PATH="+prefix)
	}
	return out
}

// envSliceToMap parses KEY=value lines; later entries win (same as typical shell export order).
func envSliceToMap(env []string) map[string]string {
	m := make(map[string]string)
	for _, e := range env {
		i := strings.IndexByte(e, '=')
		if i <= 0 {
			continue
		}
		m[e[:i]] = e[i+1:]
	}
	return m
}

func writeContainerEnvFile(env []string) (path string, err error) {
	m := envSliceToMap(env)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	f, err := os.CreateTemp("", "segment-bridge-test-env-*")
	if err != nil {
		return "", fmt.Errorf("testfixture: env file: %w", err)
	}
	path = f.Name()
	if err := f.Chmod(envFileMode); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("testfixture: chmod env file: %w", err)
	}
	defer f.Close()

	for _, k := range keys {
		line := fmt.Sprintf("%s=%s\n", k, m[k])
		if _, err := f.WriteString(line); err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("testfixture: write env file: %w", err)
		}
	}
	return path, nil
}

// regularFilePathForVolume returns an absolute host path when f refers to a regular file on disk
// (not a pipe or /proc fd). Used to bind-mount stdin sources into the test container.
func regularFilePathForVolume(f *os.File) string {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	name := f.Name()
	if name == "" || strings.HasPrefix(name, "/proc/") {
		return ""
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(abs); err != nil {
		return ""
	}
	return abs
}

func quoteSingleForBash(s string) string {
	return "'" + strings.ReplaceAll(s, `'`, `'\''`) + "'"
}

func shellArgsQuoted(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var b strings.Builder
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(quoteSingleForBash(a))
	}
	return b.String()
}

// kubeconfigHostPathForMount returns the absolute path to the kubeconfig file when it exists and is a regular file.
func kubeconfigHostPathForMount(env []string) string {
	m := envSliceToMap(env)
	p := strings.TrimSpace(m[kubeconfigEnvVar])
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return ""
	}
	return abs
}

// kubeconfigVolumeArgs returns a slice of []string { "-v", "host:container:ro" } for a readable KUBECONFIG file.
func kubeconfigVolumeArgs(env []string) [][]string {
	abs := kubeconfigHostPathForMount(env)
	if abs == "" {
		return nil
	}
	// Mount at the same path so kubectl/oc inside the container resolve KUBECONFIG without adjustment.
	// :z relabels for SELinux (e.g. Fedora) so user-namespace containers can read the temp kubeconfig.
	opts := "ro"
	if runtime.GOOS == "linux" {
		opts = "ro,z"
	}
	return [][]string{{"-v", fmt.Sprintf("%s:%s:%s", abs, abs, opts)}}
}
