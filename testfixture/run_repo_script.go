package testfixture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Environment variables controlling optional container execution for script tests.
const (
	EnvTestImage        = "SEGMENT_BRIDGE_TEST_IMAGE"
	EnvContainerRuntime = "SEGMENT_BRIDGE_TEST_CONTAINER_RUNTIME"
	containerBinDir     = "/usr/local/bin"
	kubeconfigEnvVar    = "KUBECONFIG"
	envFileMode         = 0o600
)

// Basenames of scripts copied into the segment-bridge image (see Dockerfile).
var bundledScriptBaseNames = map[string]struct{}{
	"emit-removal-event.sh":       {},
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
	return runInContainer(scriptPath, stdin, env, args, image)
}

func runOnHost(scriptPath string, stdin *os.File, env []string, args []string) ([]byte, error) {
	cmd := exec.Command(scriptPath, args...)
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

func runInContainer(scriptPath string, stdin *os.File, env []string, args []string, image string) ([]byte, error) {
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
