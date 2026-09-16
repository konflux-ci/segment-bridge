package testfixture

import "strings"

// TrimNonEmptyLines splits on newlines and returns non-empty trimmed lines.
func TrimNonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
