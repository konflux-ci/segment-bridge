package testfixture

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostScriptTimeoutKillsChildProcesses(t *testing.T) {
	t.Setenv(EnvKcovOutputDir, "")
	t.Setenv(EnvTestImage, "")
	script := createTempScript(t, "#!/bin/bash\nprintf 'started\\n' >&2\ntouch \"$2\"\n(sleep 1; printf orphan > \"$1\") &\nwait\n")
	defer os.Remove(script)

	for _, tc := range []struct {
		name string
		run  func(context.Context, string, string) ([]byte, []byte, error)
	}{
		{
			name: "stdout only",
			run: func(ctx context.Context, marker, started string) ([]byte, []byte, error) {
				out, err := runOnHost(ctx, script, nil, nil, []string{marker, started})
				return out, nil, err
			},
		},
		{
			name: "with stderr",
			run: func(ctx context.Context, marker, started string) ([]byte, []byte, error) {
				return runOnHostWithStderr(ctx, script, nil, nil, []string{marker, started})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "orphan-marker")
			started := filepath.Join(t.TempDir(), "started")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				stderr []byte
				err    error
			}
			resultCh := make(chan result, 1)
			go func() {
				_, stderr, err := tc.run(ctx, marker, started)
				resultCh <- result{stderr: stderr, err: err}
			}()
			deadline := time.After(5 * time.Second)
			for {
				if _, err := os.Stat(started); err == nil {
					break
				}
				select {
				case <-deadline:
					t.Fatal("script did not start")
				case <-time.After(10 * time.Millisecond):
				}
			}
			cancel()
			res := <-resultCh
			if !errors.Is(res.err, context.Canceled) {
				t.Fatalf("expected cancellation error, got %v", res.err)
			}
			if tc.name == "with stderr" && !strings.Contains(string(res.stderr), "started") {
				t.Errorf("expected script stderr before cancellation, got %q", res.stderr)
			}
			time.Sleep(1100 * time.Millisecond)
			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("child survived timeout and wrote marker: %v", statErr)
			}
		})
	}
}

func TestHostScriptExitKillsChildProcesses(t *testing.T) {
	t.Setenv(EnvKcovOutputDir, "")
	t.Setenv(EnvTestImage, "")
	script := createTempScript(t, "#!/bin/bash\n(sleep 2; printf orphan > \"$1\") &\nexit 0\n")
	defer os.Remove(script)
	marker := filepath.Join(t.TempDir(), "orphan-marker")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := runOnHost(ctx, script, nil, nil, []string{marker})
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected pipe wait error, got %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("child survived parent exit and wrote marker: %v", statErr)
	}
}
