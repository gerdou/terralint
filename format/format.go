// Package format rewrites a Terraform file into terralint's canonical form.
//
// File parses the file into the AST (package parser), applies the rules
// that config turns on (merging locals, naming, object key order, body
// order with sections and markers), and prints the result in canonical
// layout (lists, objects, comments, blank lines), finishing with the same
// formatting `terraform fmt` applies.
package format

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/parser"
	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// File returns the canonical form of the .tf or .tfvars file at path. It
// reads the other .tf and .tfvars files in the same directory only to find
// names that ignore_naming markers exempt from the naming rule.
func File(path string, cfg *config.Config) ([]byte, error) {
	root, err := parser.ParseTerraformFile(path)
	if err != nil {
		return nil, err
	}
	f := &formatter{cfg: cfg, root: root, tfvars: filepath.Ext(path) == ".tfvars"}
	f.exempt = f.exemptions(path)

	top := f.planFile()
	if f.on(nil, config.RuleLocals) {
		f.mergeLocals(top)
	}
	f.rename(top)
	f.sortObjects(top)
	f.order(top)

	p := &printer{cfg: cfg, text: root.Text}
	return hclwrite.Format(p.file(f, top)), nil
}

type formatter struct {
	cfg    *config.Config
	root   *types.Root
	exempt *exemptions
	tfvars bool     // the file sets variables: its attributes are named after them
	header []string // the file header the printer puts first, as planFile found it
}

// on reports whether rule applies under the rules turned off in off.
func (f *formatter) on(off offSet, rule string) bool {
	return f.cfg.On(rule) && !off[rule]
}

// exemptions are the names that ignore_naming markers keep, in the whole
// directory: references that start with one of prefixes, and the provider
// names in names.
type exemptions struct {
	prefixes [][]string
	names    map[string]bool
}

// exemptions collects the names declared under ignore_naming in path and
// the other .tf and .tfvars files of its directory. Files that do not
// mention the marker word are not parsed; files that do not parse are
// skipped.
func (f *formatter) exemptions(path string) *exemptions {
	ex := &exemptions{names: map[string]bool{}}
	word := []byte(f.cfg.IgnorePrefix + config.RuleNaming)
	self, _ := os.Stat(path)
	dir := filepath.Dir(path)
	files, _ := os.ReadDir(dir)
	for _, d := range files {
		if ext := filepath.Ext(d.Name()); !d.Type().IsRegular() && d.Type()&os.ModeSymlink == 0 || ext != ".tf" && ext != ".tfvars" {
			continue
		}
		other := filepath.Join(dir, d.Name())
		info, err := os.Stat(other)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		src, err := os.ReadFile(other)
		if err != nil || !bytes.Contains(src, word) {
			continue
		}
		root := f.root
		if self == nil || !os.SameFile(info, self) {
			if root, err = parser.ParseTerraformFile(other); err != nil {
				continue
			}
		}
		of := &formatter{cfg: f.cfg, root: root}
		for _, e := range of.planFile().all() {
			ex.collect(of, e)
		}
	}
	return ex
}

// collect records what a top-level entry declares under ignore_naming.
func (ex *exemptions) collect(f *formatter, e *entry) {
	if !f.on(e.off, config.RuleNaming) {
		ex.add(e.node)
		return
	}
	if b, ok := e.node.(*types.Block); ok && b.Type == "locals" {
		for _, le := range e.plan.all() {
			if a, ok := le.node.(*types.Attribute); ok && !f.on(le.off, config.RuleNaming) {
				ex.prefixes = append(ex.prefixes, []string{"local", a.Name})
			}
		}
	}
}

// add records the names a top-level item declares.
func (ex *exemptions) add(n types.Body) {
	b, ok := n.(*types.Block)
	if !ok {
		return
	}
	label := func(i int) string {
		if i < len(b.Labels) {
			return b.Labels[i]
		}
		return ""
	}
	switch b.Type {
	case "variable":
		ex.prefixes = append(ex.prefixes, []string{"var", label(0)})
	case "module":
		ex.prefixes = append(ex.prefixes, []string{"module", label(0)})
	case "resource":
		ex.prefixes = append(ex.prefixes, []string{label(0), label(1)})
	case "data", "ephemeral":
		ex.prefixes = append(ex.prefixes, []string{b.Type, label(0), label(1)})
	case "provider":
		ex.prefixes = append(ex.prefixes, []string{label(0)})
		ex.names[label(0)] = true
	case "locals":
		for _, c := range b.Children {
			if a, ok := c.(*types.Attribute); ok {
				ex.prefixes = append(ex.prefixes, []string{"local", a.Name})
			}
		}
	}
}

// covers reports whether a reference with these parts is exempt.
func (ex *exemptions) covers(parts []string) bool {
	for _, p := range ex.prefixes {
		if len(p) <= len(parts) {
			match := true
			for i := range p {
				if p[i] != parts[i] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}
