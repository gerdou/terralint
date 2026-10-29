package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/format"
)

// Apply rewrites every file under path to follow the rules. It formats all
// of them before writing any: references follow the names the other files
// of a module declare, as written.
func Apply(path string) error {
	type result struct {
		path string
		out  []byte
	}
	var results []result
	err := forEachFile(path, func(filePath string) error {
		out, err := format.File(filePath, config.Default())
		if err == nil {
			results = append(results, result{filePath, out})
		}
		return err
	})
	for _, r := range results {
		err = errors.Join(err, os.WriteFile(r.path, r.out, 0666))
	}
	return err
}

func isTerraformFile(path string) bool {
	ext := filepath.Ext(path)
	return ext == ".tf" || ext == ".tfvars"
}

// forEachFile runs fn on path, or, when path is a directory, once on every
// regular .tf and .tfvars file under it. Symbolic links under it are not
// followed, nor are files and directories whose name starts with ".", which
// Terraform ignores (.terraform/ holds downloaded modules). It goes on past
// failures and returns all of them.
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
	// The trailing separator makes WalkDir follow path itself when it is a
	// link to a directory; links inside it are still skipped.
	root := path + string(filepath.Separator)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if p != root && d != nil && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
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
