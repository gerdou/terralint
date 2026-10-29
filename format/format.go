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
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	children map[string]*names // by directory, for local child modules (see child)
	cfgKey   string            // cfg as text, for fileCache
	want     map[string]bool   // the file's dashed words (see lookups)
	scope    map[string]bool   // while renaming a check block: its data sources, renamed or not (D24)
	callee   *names            // while renaming a call of a local child module: the child's names (D23)
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
	whole   map[string]bool   // var.x that an ignore_naming marker keeps with all its names: its declaration and every .tfvars value in its own directory (not var files elsewhere)
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
				if src := source(x); localSource(src) {
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
// skip's (already collected). It parses only the files that can declare a
// name the formatted file looks up (see fileInfo.needed).
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
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if fi := f.fileInfo(path, src); fi.needed(f.lookups()) {
			if n := fi.load(f.cfg, path, src); n != nil {
				into.merge(n)
			}
		}
	}
}

// fileCache holds a fileInfo per file contents and configuration, so that
// formatting every file of a directory parses each file once at most, not
// once per file of the directory.
// shortcut: never emptied; it grows with the distinct files a process
// reads, fine for the CLI; bound it if format runs in a long-lived server.
var fileCache = struct {
	sync.Mutex
	m map[fileKey]*fileInfo
}{m: map[fileKey]*fileInfo{}}

type fileKey struct {
	sum    [sha256.Size]byte
	tfvars bool
	cfg    string
}

// fileInfo is what the naming rule needs from another file.
type fileInfo struct {
	decls map[string]bool // its dashed words not right after a ".": where a name it declares can be
	other bool            // it holds a local module source or an ignore_naming marker
	once  sync.Once
	names *names // what it declares, once loaded; nil when it does not parse
}

func (f *formatter) fileInfo(path string, src []byte) *fileInfo {
	if f.cfgKey == "" {
		f.cfgKey = fmt.Sprintf("%#v", *f.cfg)
	}
	key := fileKey{sha256.Sum256(src), filepath.Ext(path) == ".tfvars", f.cfgKey}
	fileCache.Lock()
	defer fileCache.Unlock()
	fi, ok := fileCache.m[key]
	if !ok {
		fi = &fileInfo{decls: map[string]bool{}}
		fi.other = bytes.Contains(src, []byte("./")) || bytes.Contains(src, []byte(f.cfg.IgnorePrefix+config.RuleNaming))
		f.words(src, func(w string, dot bool) {
			if !dot {
				fi.decls[w] = true
			}
		})
		fileCache.m[key] = fi
	}
	return fi
}

// needed reports whether the file can declare a name a file with these
// dashed words looks up: every name looked up is a dashed word of the file
// that looks it up (a reference, a .tfvars or argument name, a variable
// label), and a file that declares it holds it as a word not right after a
// "."; module sources and markers are looked up by undashed names too.
func (fi *fileInfo) needed(lookups map[string]bool) bool {
	if fi.other {
		return true
	}
	a, b := fi.decls, lookups
	if len(b) < len(a) {
		a, b = b, a
	}
	for w := range a {
		if b[w] {
			return true
		}
	}
	return false
}

// load returns the names the file declares, parsing it the first time.
func (fi *fileInfo) load(cfg *config.Config, path string, src []byte) *names {
	fi.once.Do(func() {
		root, err := parser.Parse(src, path)
		if err != nil {
			return
		}
		of := &formatter{cfg: cfg, root: root, tfvars: filepath.Ext(path) == ".tfvars"}
		fi.names = newNames()
		fi.names.collect(of, of.planFile())
	})
	return fi.names
}

// lookups returns the dashed words of the file being formatted.
func (f *formatter) lookups() map[string]bool {
	if f.want == nil {
		f.want = map[string]bool{}
		src, _ := os.ReadFile(f.path)
		f.words(src, func(w string, _ bool) { f.want[w] = true })
	}
	return f.want
}

// words calls fn on each run of name bytes in src (letters, digits, "_",
// "-" and non-ASCII bytes) that holds what the naming rule replaces; dot
// says a "." comes right before it.
func (f *formatter) words(src []byte, fn func(w string, dot bool)) {
	start := -1
	for i := 0; i <= len(src); i++ {
		if i < len(src) {
			if c := src[i]; c >= 0x80 || c == '_' || c == '-' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' {
				if start < 0 {
					start = i
				}
				continue
			}
		}
		if start >= 0 && bytes.Contains(src[start:i], []byte(f.cfg.Naming.Replace)) {
			fn(string(src[start:i]), start > 0 && src[start-1] == '.')
		}
		start = -1
	}
}

// merge adds what o declares, as collect would have.
func (n *names) merge(o *names) {
	for k, v := range o.renamed {
		n.renamed[k] = v
	}
	for k := range o.whole {
		n.whole[k] = true
	}
	for k, v := range o.sources {
		n.sources[k] = v
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

// localSource reports whether a module source is a local child module's.
func localSource(src string) bool {
	return strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../")
}

// child returns the names the local child module at src declares.
func (f *formatter) child(src string) *names {
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
	return c
}

// childRenames reports whether the local child module that module call m
// declares output o and the naming rule renames it there.
func (f *formatter) childRenames(m, o string) bool {
	if !f.dashed(o) {
		return false
	}
	src, ok := f.decl().sources[m]
	return ok && f.child(src).renames("output."+o)
}
