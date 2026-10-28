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
	"slices"

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

// exemptions are the names that keep their dashes, in the whole directory:
// those that ignore_naming markers keep, and those declared by blocks whose
// type is not in Naming.Labels. References that start with one of prefixes
// keep those names; the provider names in names keep theirs everywhere.
type exemptions struct {
	prefixes []prefix
	names    map[string]bool
}

// prefix is the start of the references to an exempt name. When deep, the
// steps after it keep their names too: an item under ignore_naming keeps
// its object keys, which those steps name.
type prefix struct {
	parts []string
	deep  bool
}

// declaring are the block types whose names references use (see declares),
// but provider: provider local names the rule does not rename keep theirs
// where they appear (see providerPlace), with no exemption to collect.
var declaring = []string{"variable", "module", "resource", "data", "ephemeral"}

// exemptions collects the exempt names declared in path and the other .tf
// and .tfvars files of its directory. Files that do not mention the marker
// word are not parsed, unless a declaring block type is missing from
// Naming.Labels; files that do not parse are skipped.
func (f *formatter) exemptions(path string) *exemptions {
	ex := &exemptions{names: map[string]bool{}}
	word := []byte(f.cfg.IgnorePrefix + config.RuleNaming)
	every := false
	for _, t := range declaring {
		every = every || !f.renames(t)
	}
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
		if err != nil || !every && !bytes.Contains(src, word) {
			continue
		}
		root := f.root
		if self == nil || !os.SameFile(info, self) {
			if root, err = parser.ParseTerraformFile(other); err != nil {
				continue
			}
		}
		of := &formatter{cfg: f.cfg, root: root, tfvars: filepath.Ext(other) == ".tfvars"}
		for _, e := range of.planFile().all() {
			ex.collect(of, e)
		}
	}
	return ex
}

// renames reports whether the naming rule renames the names that blocks of
// this type declare.
func (f *formatter) renames(blockType string) bool {
	_, ok := f.cfg.Naming.Labels[blockType]
	return ok
}

// collect records the exempt names a top-level entry declares: all of them
// under ignore_naming, and those of a block type the rule does not rename.
// Under ignore_naming, a .tfvars assignment exempts the variable it sets.
func (ex *exemptions) collect(f *formatter, e *entry) {
	off := !f.on(e.off, config.RuleNaming)
	switch n := e.node.(type) {
	case *types.Attribute:
		if off && f.tfvars {
			ex.prefixes = append(ex.prefixes, prefix{[]string{"var", n.Name}, true})
		}
	case *types.Block:
		renamed := f.renames(n.Type)
		if p := declares(n); p != nil && (off || !renamed) && (renamed || n.Type != "provider") {
			// The steps after module.<name> are the child module's outputs,
			// which follow the child's own declarations.
			ex.prefixes = append(ex.prefixes, prefix{p, off && n.Type != "module"})
			if n.Type == "provider" {
				ex.names[p[0]] = true
			}
		}
		if n.Type == "locals" {
			for _, le := range e.plan.all() {
				if a, ok := le.node.(*types.Attribute); ok && !f.on(le.off, config.RuleNaming) {
					ex.prefixes = append(ex.prefixes, prefix{[]string{"local", a.Name}, true})
				}
			}
		}
	}
}

// declares returns how references start that name what a top-level block
// declares (var.<name>, <type>.<name>, ...), or nil.
func declares(b *types.Block) []string {
	label := func(i int) string {
		if i < len(b.Labels) {
			return b.Labels[i]
		}
		return ""
	}
	switch b.Type {
	case "variable":
		return []string{"var", label(0)}
	case "module":
		return []string{"module", label(0)}
	case "resource":
		return []string{label(0), label(1)}
	case "data", "ephemeral":
		return []string{b.Type, label(0), label(1)}
	case "provider":
		return []string{label(0)}
	}
	return nil
}

// covers returns how many leading parts of a reference an exemption keeps,
// and whether it keeps the steps after them too.
func (ex *exemptions) covers(parts []string) (n int, deep bool) {
	for _, p := range ex.prefixes {
		if len(p.parts) <= len(parts) && slices.Equal(p.parts, parts[:len(p.parts)]) {
			n, deep = max(n, len(p.parts)), deep || p.deep
		}
	}
	return n, deep
}
