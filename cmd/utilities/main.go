package utilities

import (
	"os"
	"path/filepath"
	"strings"
)

func AbsPath(targetPath string) (string, error) {
	if strings.Contains(targetPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		targetPath = strings.ReplaceAll(targetPath, "~", home)
	}
	return filepath.Abs(targetPath)
}
