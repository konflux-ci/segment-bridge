package main

import (
	"fmt"
	"os"
)

func main() {
	responseFile := os.Getenv("MOCK_TKN_RESULTS_RESPONSE_FILE")
	if responseFile == "" {
		fmt.Fprintln(os.Stderr, "ERROR: MOCK_TKN_RESULTS_RESPONSE_FILE environment variable not set")
		os.Exit(1)
	}

	data, err := os.ReadFile(responseFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to read response file: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(string(data))
}
