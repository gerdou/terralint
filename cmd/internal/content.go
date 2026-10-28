package internal

import (
	"os"

	"github.com/gerdou/terralint/parser"
)

// getFormattedContent parses the file and returns its content with the rules
// applied. No rule exists yet, so the content comes back unchanged.
func getFormattedContent(filePath string) ([]byte, error) {
	if _, err := parser.ParseTerraformFile(filePath); err != nil {
		return nil, err
	}
	return os.ReadFile(filePath)
}
