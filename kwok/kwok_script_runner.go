package kwok

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed kwok_container_template.tmpl
var KwokServiceManifest string

// SetKubeconfig sets KUBECONFIG to the kwok directory's kubeconfig file (fixed port 8080).
// For tests that use the containerfixture, use SetKubeconfigWithPort(deployment.WebPort) instead
// so the config points at the dynamically allocated host port.
func SetKubeconfig() error {
	path, err := getKubeconfigPath()
	if err != nil {
		return err
	}
	os.Setenv("KUBECONFIG", path)
	return nil
}

// SetKubeconfigWithPort sets KUBECONFIG to a temporary kubeconfig that points at the given host port.
// Use this when the kwok pod is started with a dynamic host port (e.g. deployment.WebPort from containerfixture).
// The file is created in the system temp directory so it works in read-only or unwritable clone environments.
func SetKubeconfigWithPort(hostPort string) error {
	f, err := os.CreateTemp("", "kwok-kubeconfig-")
	if err != nil {
		return fmt.Errorf("create temp kubeconfig: %w", err)
	}
	path := f.Name()
	config := fmt.Sprintf(`apiVersion: v1
clusters:
- cluster:
    server: http://127.0.0.1:%s
  name: kwok
contexts:
- context:
    cluster: kwok
    user: ""
  name: kwok
current-context: kwok
kind: Config
preferences: {}
users: null
`, hostPort)
	if _, err := f.Write([]byte(config)); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write kubeconfig: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("close kubeconfig: %w", err)
	}
	os.Setenv("KUBECONFIG", path)
	return nil
}

func getKwokDir() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to find the path of the current script")
	}
	return filepath.Dir(filename), nil
}

func getKubeconfigPath() (string, error) {
	dir, err := getKwokDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kubeconfig"), nil
}
