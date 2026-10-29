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
	"strings"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/parser"
	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// File returns the canonical form of the .tf or .tfvars file at path. It
// reads the other .tf and .tfvars files in the same directory, and those of
// local child modules, only to find the names they declare, which the
// references in this file follow.
func File(path string, cfg *config.Config) ([]byte, error) {
	root, err := parser.ParseTerraformFile(path)
	if err != nil {
		return nil, err
	}
	f := &formatter{cfg: cfg, root: root, path: path, tfvars: filepath.Ext(path) == ".tfvars"}

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
	cfg      *config.Config
	root     *types.Root
	path     string
	tfvars   bool     // the file sets variables: its attributes are named after them
	header   []string // the file header the printer puts first, as planFile found it
	names    *names   // the names the file's directory declares (see decl)
	sibsRead bool
	children map[string]*names // by directory, for local child modules (see childRenames)
}

// on reports whether rule applies under the rules turned off in off.
func (f *formatter) on(off offSet, rule string) bool {
	return f.cfg.On(rule) && !off[rule]
}

// renames reports whether the naming rule renames the names that blocks of
// this type declare.
func (f *formatter) renames(blockType string) bool {
	_, ok := f.cfg.Naming.Labels[blockType]
	return ok
}

// names are the dashed names a directory declares, by the start of the
// references to them (var.x, local.x, module.x, <type>.x, data.<type>.x,
// ephemeral.<type>.x, output.x), each true when the naming rule renames it.
type names struct {
	renamed map[string]bool
	whole   map[string]bool   // var.x that an ignore_naming marker keeps with all its names: its declaration and every .tfvars value
	sources map[string]string // module call label -> source, for local child modules
}

func newNames() *names {
	return &names{renamed: map[string]bool{}, whole: map[string]bool{}, sources: map[string]string{}}
}

// renames reports whether a declaration the references starting with key
// name exists and the naming rule renames it.
func (n *names) renames(key string) bool { return n.renamed[key] && !n.whole[key] }

// kept reports whether a declaration the references starting with key name
// exists and keeps its name.
func (n *names) kept(key string) bool {
	r, ok := n.renamed[key]
	return ok && !r || n.whole[key]
}

// collect records what the top-level entries of a planned file declare.
func (n *names) collect(f *formatter, top *plan) {
	for _, e := range top.all() {
		off := !f.on(e.off, config.RuleNaming)
		switch x := e.node.(type) {
		case *types.Attribute:
			if f.tfvars && off {
				n.whole["var."+x.Name] = true
			}
		case *types.Block:
			if x.Type == "locals" && e.plan != nil {
				for _, le := range e.plan.all() {
					if a, ok := le.node.(*types.Attribute); ok {
						n.renamed["local."+a.Name] = f.on(le.off, config.RuleNaming)
					}
				}
				continue
			}
			key := declKey(x)
			if key == "" {
				continue
			}
			n.renamed[key] = !off && f.renames(x.Type)
			if x.Type == "variable" && off {
				n.whole[key] = true
			}
			if x.Type == "module" {
				if src := source(x); strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
					n.sources[x.Labels[0]] = src
				}
			}
		}
	}
}

// declKey returns how references start that name what a top-level block
// declares, or "".
func declKey(b *types.Block) string {
	if i, ok := map[string]int{"variable": 0, "module": 0, "output": 0, "resource": 1, "data": 1, "ephemeral": 1}[b.Type]; !ok || i >= len(b.Labels) {
		return ""
	}
	switch b.Type {
	case "variable":
		return "var." + b.Labels[0]
	case "module", "output":
		return b.Type + "." + b.Labels[0]
	case "resource":
		return b.Labels[0] + "." + b.Labels[1]
	}
	return b.Type + "." + b.Labels[0] + "." + b.Labels[1]
}

// source returns a module call's source string, or "".
func source(b *types.Block) string {
	for _, c := range b.Children {
		if a, ok := c.(*types.Attribute); ok && a.Name == "source" {
			if l, ok := a.Value.(*types.LiteralValue); ok {
				s, _ := l.Value.(string)
				return s
			}
		}
	}
	return ""
}

// dirNames collects the names the .tf and .tfvars files of dir declare, but
// skip's (already collected), from files that hold a dash and parse.
func (f *formatter) dirNames(dir, skip string, into *names) {
	self, _ := os.Stat(skip)
	files, _ := os.ReadDir(dir)
	for _, d := range files {
		ext := filepath.Ext(d.Name())
		if ext != ".tf" && ext != ".tfvars" || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, d.Name())
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || self != nil && os.SameFile(info, self) {
			continue
		}
		if src, err := os.ReadFile(path); err != nil || !bytes.Contains(src, []byte(f.cfg.Naming.Replace)) {
			continue
		}
		root, err := parser.ParseTerraformFile(path)
		if err != nil {
			continue
		}
		of := &formatter{cfg: f.cfg, root: root, tfvars: ext == ".tfvars"}
		into.collect(of, of.planFile())
	}
}

// decl returns the names declared in the file's directory, reading the
// other files the first time it is needed.
func (f *formatter) decl() *names {
	if !f.sibsRead {
		f.sibsRead = true
		f.dirNames(filepath.Dir(f.path), f.path, f.names)
	}
	return f.names
}

// childRenames reports whether the local child module that module call m
// declares output o and the naming rule renames it there.
func (f *formatter) childRenames(m, o string) bool {
	src, ok := f.decl().sources[m]
	if !ok || !strings.Contains(o, f.cfg.Naming.Replace) {
		return false
	}
	dir := filepath.Join(filepath.Dir(f.path), src)
	if f.children == nil {
		f.children = map[string]*names{}
	}
	c, ok := f.children[dir]
	if !ok {
		c = newNames()
		f.dirNames(dir, "", c)
		f.children[dir] = c
	}
	return c.renames("output." + o)
}
