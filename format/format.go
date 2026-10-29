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

	p := newPrinter(cfg, root.Text)
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
	keys     map[string]bool     // the names inside the items that ignore_naming markers keep whole (see formatter.readName)
	aliases  map[string][]string // the locals whose value is a plain reference path, by renamed name, and that path (see formatter.through)
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
// Naming.Labels, or they may declare or set a variable whose keys a marker
// keeps; files that do not parse are skipped.
func (f *formatter) exemptions(path string) *exemptions {
	ex := &exemptions{names: map[string]bool{}, keys: map[string]bool{}, aliases: map[string][]string{}}
	word := []byte(f.cfg.IgnorePrefix + config.RuleNaming)
	every := false
	for _, t := range declaring {
		every = every || !f.renames(t)
	}
	type sibling struct {
		path string
		src  []byte
		of   *formatter // its root is nil until load parses it
		top  *plan
	}
	load := func(s *sibling) bool {
		if s.of.root == nil {
			root, err := parser.ParseTerraformFile(s.path)
			if err != nil {
				return false
			}
			s.of.root = root
		}
		s.top = s.of.planFile()
		return true
	}
	var sibs []*sibling
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
		if err != nil {
			continue
		}
		s := &sibling{path: other, src: src, of: &formatter{cfg: f.cfg, tfvars: filepath.Ext(other) == ".tfvars"}}
		if self != nil && os.SameFile(info, self) {
			s.of.root = f.root
		}
		if (every || bytes.Contains(src, word)) && !load(s) {
			continue
		}
		sibs = append(sibs, s)
		if s.top != nil {
			for _, e := range s.top.all() {
				ex.collect(s.of, e)
			}
		}
	}
	// A marker on a variable's declaration or on a .tfvars assignment keeps
	// the keys of the declaration and of every assignment (see renamePlan),
	// so the names in all of them count as kept (D14). Reads through a local
	// alias follow what it aliases (D16), so where a read can keep its steps
	// (a marker keeps them, or outputs keep their names), the locals of
	// every file count.
	kept := !f.renames("output") || slices.ContainsFunc(ex.prefixes, func(p prefix) bool { return p.deep })
	for _, s := range sibs {
		if s.top == nil && (kept && bytes.Contains(s.src, []byte("locals")) || slices.ContainsFunc(ex.prefixes, func(p prefix) bool {
			return p.deep && len(p.parts) == 2 && p.parts[0] == "var" && bytes.Contains(s.src, []byte(p.parts[1]))
		})) && load(s) {
			for _, e := range s.top.all() {
				ex.collect(s.of, e)
			}
		}
	}
	for _, s := range sibs {
		if s.top == nil {
			continue
		}
		visit(s.top, func(e *entry, _ *plan) {
			if !s.of.on(e.off, config.RuleNaming) {
				ex.keep(e.node)
			}
		})
		for _, e := range s.top.all() {
			if s.of.keptWhole(ex, e.node) {
				ex.keep(e.node)
				if e.plan != nil {
					visit(e.plan, func(e *entry, _ *plan) { ex.keep(e.node) })
				}
			}
		}
	}
	return ex
}

// keptWhole reports whether top-level item n keeps every name in it
// because a marker on another item keeps its keys: a variable's declaration
// or .tfvars assignment (see renamePlan).
func (f *formatter) keptWhole(ex *exemptions, n types.Body) bool {
	var p []string
	switch n := n.(type) {
	case *types.Attribute:
		if f.tfvars {
			p = []string{"var", n.Name}
		}
	case *types.Block:
		p = declares(n)
	}
	_, deep := ex.covers(p)
	return p != nil && deep
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
				a, ok := le.node.(*types.Attribute)
				if !ok {
					continue
				}
				// Reads through an alias follow what it aliases (D16), so a
				// marker on it keeps only its name. Keyed by renamed name:
				// apply -d may have rewritten its file already.
				p := path(a.Value)
				if p != nil {
					ex.aliases[f.name(a.Name)] = p
				}
				if !f.on(le.off, config.RuleNaming) {
					ex.prefixes = append(ex.prefixes, prefix{[]string{"local", a.Name}, p == nil})
				}
			}
		}
	}
}

// keep records the names an item under ignore_naming keeps: its name or
// type, and the unquoted object keys in its value. Inside a marked block,
// visit reaches the items, which are under the marker too.
func (ex *exemptions) keep(n types.Body) {
	switch n := n.(type) {
	case *types.Attribute:
		ex.keys[n.Name] = true
		walk(n.Value, func(x types.Expression) {
			if o, ok := x.(*types.ObjectExpr); ok {
				for _, it := range o.Items {
					if k, ok := it.Key.(*types.ReferenceExpr); ok && len(k.Parts) == 1 {
						ex.keys[k.Parts[0]] = true
					}
				}
			}
		})
	case *types.Block:
		ex.keys[n.Type] = true
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

// path returns the names a plain reference path reads (var.x, var.x["k"],
// local.y.z, module.m.out: indexes and attribute steps only): its reference,
// then its attribute steps, without the indexes. nil for any other
// expression.
func path(e types.Expression) []string {
	switch x := e.(type) {
	case *types.ReferenceExpr:
		return slices.Clone(x.Parts)
	case *types.IndexExpr:
		return path(x.Collection)
	case *types.RelativeTraversalExpr:
		p := path(x.Source)
		for _, s := range x.Traversal {
			if p != nil && s.Type == "attr" {
				p = append(p, s.Name)
			}
		}
		return p
	}
	return nil
}

// through returns what a reference that starts at a local alias reads (D16):
// the path the local aliases, through every alias on the way, then the
// reference's parts after the local's name. ok is false for any other
// reference.
func (f *formatter) through(parts []string) (read []string, ok bool) {
	seen := map[string]bool{}
	for len(parts) >= 2 && parts[0] == "local" && !seen[f.name(parts[1])] {
		p, is := f.exempt.aliases[f.name(parts[1])]
		if !is {
			break
		}
		seen[f.name(parts[1])] = true
		parts, ok = append(slices.Clip(p), parts[2:]...), true
	}
	return parts, ok
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
