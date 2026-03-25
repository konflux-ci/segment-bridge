package testfixture

import (
	"fmt"
	"os"
)

func RunScriptWithInputFile(filePath, scriptPath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	return RunRepoScript(scriptPath, file, nil)
}
