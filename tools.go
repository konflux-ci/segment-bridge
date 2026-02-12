//go:build tools

package tools

// This file pins Go tool dependencies for hermetic builds.
// The tektoncd/results CLI is compiled in the Dockerfile.
import _ "github.com/tektoncd/results/cmd/tkn-results"
