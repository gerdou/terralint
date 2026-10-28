package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Apply rewrites every file under path to follow the rules.
func Apply(path string) error {
	return forEachFile(path, func(filePath string) error {
		formattedBytes, err := getFormattedContent(filePath)
		if err != nil {
			return err
		}
		return os.WriteFile(filePath, formattedBytes, 0666)
	})
}

func isTerraformFile(path string) bool {
	ext := filepath.Ext(path)
	return ext == ".tf" || ext == ".tfvars"
}

// forEachFile runs fn on path, or, when path is a directory, once on every
// regular .tf and .tfvars file under it. Symbolic links are not followed. It
// goes on past failures and returns all of them.
func forEachFile(path string, fn func(string) error) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !isTerraformFile(path) {
			return fmt.Errorf("%s: not a .tf or .tfvars file", path)
		}
		return fn(path)
	}
	var errs []error
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
		} else if d.Type().IsRegular() && isTerraformFile(p) {
			if err := fn(p); err != nil {
				errs = append(errs, err)
			}
		}
		return nil
	})
	return errors.Join(append(errs, err)...)
}
