package format_test

// Formatter gate. For every valid .tf/.tfvars file under parser/test_files,
// and under each directory in $TERRALINT_CORPUS when set, format.File's
// output must:
//   - parse, and mean the same as the source once the intended changes are
//     applied to both (names with '-' renamed, locals merged, body items and
//     object keys sorted, nested blocks of one type kept in source order);
//   - keep every comment line, heredoc and file header;
//   - follow the default rules (order, objects, lists, naming, locals,
//     comment style, blank lines);
//   - come back unchanged when formatted again;
//   - pass `terraform fmt -check` (when terraform is on PATH; required when
//     $TERRALINT_REQUIRE_TERRAFORM is set).
// With $TERRALINT_BIN set, TestFormatCLI runs check and apply over copies of
// every repository; files under a name starting with "." must be left alone. $TERRALINT_REPORT, when set, receives every problem.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/format"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

func roots() []string {
	r := []string{"../parser/test_files"}
	if env := os.Getenv("TERRALINT_CORPUS"); env != "" {
		r = append(r, filepath.SplitList(env)...)
	}
	return r
}

func terraformFiles(t *testing.T, root string) []string {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		ext := filepath.Ext(path)
		if d.Type().IsRegular() && (ext == ".tf" || ext == ".tfvars") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func isValid(content []byte, path string) bool {
	_, diags := hclsyntax.ParseConfig(content, path, hcl.InitialPos)
	return !diags.HasErrors()
}

func parallel[T any](items []T, fn func(T)) {
	ch := make(chan T)
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				fn(it)
			}
		}()
	}
	for _, it := range items {
		ch <- it
	}
	close(ch)
	wg.Wait()
}

type problems struct {
	mu   sync.Mutex
	list []string
}

func (p *problems) add(format string, args ...any) {
	p.mu.Lock()
	p.list = append(p.list, fmt.Sprintf(format, args...))
	p.mu.Unlock()
}

func (p *problems) report(t *testing.T, name string, checked int) {
	sort.Strings(p.list)
	t.Logf("%s: %d checked, %d problems", name, checked, len(p.list))
	if path := os.Getenv("TERRALINT_REPORT"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			for _, line := range p.list {
				fmt.Fprintf(f, "%s: %s\n", name, strings.ReplaceAll(line, "\n", `\n`))
			}
			f.Close()
		}
	}
	for i, line := range p.list {
		if i == 50 {
			t.Errorf("... and %d more problems", len(p.list)-50)
			break
		}
		t.Error(line)
	}
}

const fileTimeout = time.Minute

// formatFile runs format.File with a timeout and turns a panic into an error.
func formatFile(path string) (out []byte, err error, took time.Duration) {
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		out, err = format.File(path, config.Default())
	}()
	select {
	case <-done:
		return out, err, time.Since(start)
	case <-time.After(fileTimeout):
		return nil, fmt.Errorf("did not finish within %s", fileTimeout), fileTimeout
	}
}

type formatted struct {
	root, rel string // rel is relative to root
	src, out  []byte
}

func TestFormatCorpus(t *testing.T) {
	var p problems
	var mu sync.Mutex
	var results []formatted
	checked := 0
	mirror := t.TempDir()

	for i, root := range roots() {
		files := terraformFiles(t, root)
		checked += len(files)
		parallel(files, func(path string) {
			src, err := os.ReadFile(path)
			if err != nil {
				p.add("%s: read: %v", path, err)
				return
			}
			out, err, took := formatFile(path)
			if !isValid(src, path) {
				if err == nil || strings.HasPrefix(err.Error(), "panic") {
					p.add("%s: invalid HCL, want an error from format.File, got %v", path, err)
				}
				return
			}
			if err != nil {
				p.add("%s: format.File: %v", path, err)
				return
			}
			if took > 5*time.Second {
				p.add("%s: format.File took %s", path, took)
			}
			for _, prob := range checkOutput(path, src, out) {
				p.add("%s: %s", path, prob)
			}
			rel, _ := filepath.Rel(root, path)
			mu.Lock()
			results = append(results, formatted{root: filepath.Join(mirror, fmt.Sprint(i)), rel: rel, src: src, out: out})
			mu.Unlock()
		})
	}

	// Idempotence: format each output again, inside a mirror tree of outputs.
	for _, r := range results {
		dst := filepath.Join(r.root, r.rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, r.out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	parallel(results, func(r formatted) {
		dst := filepath.Join(r.root, r.rel)
		again, err, _ := formatFile(dst)
		if err != nil {
			p.add("%s: formatting the output again: %v", r.rel, err)
		} else if !bytes.Equal(again, r.out) {
			p.add("%s: not idempotent: %s", r.rel, firstDiff(r.out, again))
		}
	})

	fmtMirror := t.TempDir()
	for _, r := range results {
		dst := filepath.Join(fmtMirror, strings.TrimPrefix(r.root, mirror), r.rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, plainLabels(r.out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	checkTerraformFmt(t, fmtMirror, &p)
	p.report(t, "format", checked)
}

func checkTerraformFmt(t *testing.T, dir string, p *problems) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		if os.Getenv("TERRALINT_REQUIRE_TERRAFORM") != "" {
			p.add("terraform not found on PATH")
		} else {
			t.Log("terraform not on PATH: skipping the terraform fmt check")
		}
		return
	}
	cmd := exec.Command(tf, "fmt", "-check", "-recursive", "-list=true", "-no-color", dir)
	cmd.Env = append(os.Environ(), "CHECKPOINT_DISABLE=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" {
			p.add("%s: terraform fmt would change this output", line)
		}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() != 3 {
		p.add("terraform fmt failed (exit %d): %s", exitErr.ExitCode(), trim(errb.String()))
	} else if err != nil && !errors.As(err, &exitErr) {
		p.add("terraform fmt: %v", err)
	}
}

// plainLabels replaces % and $ in block labels with _, because terraform fmt
// 1.9.7 deletes a label that does not lex as one quoted literal, as
// `resource "a" "%[1]s" {}` does.
func plainLabels(src []byte) []byte {
	f, diags := hclsyntax.ParseConfig(src, "x.tf", hcl.InitialPos)
	if diags.HasErrors() {
		return src
	}
	out := append([]byte{}, src...)
	var walk func(b *hclsyntax.Body)
	walk = func(b *hclsyntax.Body) {
		for _, blk := range b.Blocks {
			for _, r := range blk.LabelRanges {
				for i := r.Start.Byte; i < r.End.Byte; i++ {
					if out[i] == '%' || out[i] == '$' {
						out[i] = '_'
					}
				}
			}
			walk(blk.Body)
		}
	}
	walk(f.Body.(*hclsyntax.Body))
	return out
}

func firstDiff(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d: first %q, then %q", i+1, x, y)
		}
	}
	return "same lines, different bytes"
}

func trim(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// checkOutput returns every way out fails the gate for source src.
func checkOutput(path string, src, out []byte) []string {
	var ps []string
	fail := func(format string, args ...any) { ps = append(ps, fmt.Sprintf(format, args...)) }

	outFile, diags := hclsyntax.ParseConfig(out, path, hcl.InitialPos)
	if diags.HasErrors() {
		fail("output does not parse: %s", diags.Error())
		return ps
	}
	srcFile, _ := hclsyntax.ParseConfig(src, path, hcl.InitialPos)

	// Meaning. In the source, references to names the directory declares
	// follow their rename; the output's references are compared as written.
	want, err := (&dumper{refs: declsFor(filepath.Dir(path))}).dumpBody(srcFile.Body.(*hclsyntax.Body), true)
	if err != nil {
		fail("dumping source: %v", err)
	} else if got, err := (&dumper{}).dumpBody(outFile.Body.(*hclsyntax.Body), true); err != nil {
		fail("dumping output: %v", err)
	} else if got != want {
		fail("output means something else: %s", firstSemDiff(want, got))
	}

	srcToks, _ := hclsyntax.LexConfig(src, path, hcl.InitialPos)
	outToks, _ := hclsyntax.LexConfig(out, path, hcl.InitialPos)

	// Comments: every comment line is kept, and only # comments remain.
	if a, b := commentLines(srcToks), commentLines(outToks); !equalMultiset(a, b) {
		fail("comment lines differ: lost %q, added %q", missing(a, b), missing(b, a))
	}
	for _, tok := range outToks {
		if tok.Type == hclsyntax.TokenComment && !bytes.HasPrefix(tok.Bytes, []byte("#")) {
			fail("line %d: comment not in # form: %q", tok.Range.Start.Line, tok.Bytes)
		}
	}

	// Heredocs are kept as heredocs, with their markers.
	if a, b := heredocs(srcToks), heredocs(outToks); !equalMultiset(a, b) {
		fail("heredoc openers differ: source %q, output %q", a, b)
	}

	// A file header comment that a blank line separates from the first item stays first.
	if header := headerLines(srcToks); len(header) > 0 {
		got := commentLines(leadingComments(outToks))
		if len(got) < len(header) || strings.Join(got[:len(header)], "\n") != strings.Join(header, "\n") {
			fail("file header comment %q is no longer at the top", header)
		}
	}

	// Blank lines and the end of the file.
	if line, ok := doubleBlank(outToks); ok {
		fail("line %d: two blank lines in a row", line)
	}
	if len(out) > 0 && (!bytes.HasSuffix(out, []byte("\n")) || bytes.HasSuffix(out, []byte("\n\n")) || out[0] == '\n') {
		fail("output must not start with a blank line and must end with exactly one newline")
	}

	c := &ruleChecker{out: out, toks: outToks}
	c.body(outFile.Body.(*hclsyntax.Body), "root", true)
	return append(ps, c.problems...)
}

// doubleBlank finds two blank lines in a row outside heredoc and string text.
// A line comment's token carries its own newline.
func doubleBlank(toks hclsyntax.Tokens) (int, bool) {
	run := 0
	for _, tok := range toks {
		switch {
		case tok.Type == hclsyntax.TokenNewline:
			run++
		case tok.Type == hclsyntax.TokenComment && bytes.HasSuffix(tok.Bytes, []byte("\n")):
			run = 1
			continue
		default:
			run = 0
			continue
		}
		if run >= 3 {
			return tok.Range.Start.Line, true
		}
	}
	return 0, false
}

// ---- Meaning: a canonical dump with the intended changes applied. ----

func ren(s string) string { return strings.ReplaceAll(s, "-", "_") }

// Labels that name things, by block type: the label index that is renamed.
// Provider block labels are not here: provider local names are never renamed
// (D12), because Terraform rejects "_" in them. A dynamic block's label is a
// block type of the provider's schema, not a name.
var namedLabel = map[string]int{"variable": 0, "output": 0, "module": 0, "resource": 1, "data": 1, "ephemeral": 1}

// dumper renders expressions canonically. With refs, the name part of a
// reference to a dashed name the directory declares is renamed, as the
// naming rule renames it; without, references are rendered as written.
type dumper struct{ refs *dirDecls }

// dirDecls are the dashed names a directory's .tf files declare, by the
// start of the references to them, and the dashed outputs of its local child
// modules, by module call label.
type dirDecls struct {
	names map[string]bool
	outs  map[string]map[string]bool
}

func (r *dirDecls) has(key string) bool { return strings.Contains(key, "-") && r.names[key] }

var declCache sync.Map

func declsFor(dir string) *dirDecls {
	if v, ok := declCache.Load(dir); ok {
		return v.(*dirDecls)
	}
	r := &dirDecls{names: map[string]bool{}, outs: map[string]map[string]bool{}}
	eachBody(dir, func(b *hclsyntax.Body) {
		for _, blk := range b.Blocks {
			l := blk.Labels
			switch {
			case blk.Type == "variable" && len(l) > 0:
				r.names["var."+l[0]] = true
			case blk.Type == "locals":
				for name := range blk.Body.Attributes {
					r.names["local."+name] = true
				}
			case blk.Type == "module" && len(l) > 0:
				r.names["module."+l[0]] = true
				if src, ok := blk.Body.Attributes["source"]; ok {
					if v, diags := src.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String {
						if s := v.AsString(); strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
							outs := map[string]bool{}
							eachBody(filepath.Join(dir, s), func(cb *hclsyntax.Body) {
								for _, o := range cb.Blocks {
									if o.Type == "output" && len(o.Labels) > 0 {
										outs[o.Labels[0]] = true
									}
								}
							})
							r.outs[l[0]] = outs
						}
					}
				}
			case blk.Type == "resource" && len(l) > 1:
				r.names[l[0]+"."+l[1]] = true
			case (blk.Type == "data" || blk.Type == "ephemeral") && len(l) > 1:
				r.names[blk.Type+"."+l[0]+"."+l[1]] = true
			}
		}
	})
	declCache.Store(dir, r)
	return r
}

// eachBody calls fn with the body of every .tf file in dir that parses,
// following links to files, as Terraform loads a module's files.
func eachBody(dir string, fn func(*hclsyntax.Body)) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".tf" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err != nil || !info.Mode().IsRegular() {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if f, diags := hclsyntax.ParseConfig(src, e.Name(), hcl.InitialPos); !diags.HasErrors() {
			fn(f.Body.(*hclsyntax.Body))
		}
	}
}

// absNames returns the steps of an absolute traversal that the naming rule
// renames: the name part of a reference to a declared name, and the output
// read from a local child module.
func (d *dumper) absNames(tr hcl.Traversal) map[int]bool {
	out := map[int]bool{}
	if d.refs == nil {
		return out
	}
	attr := func(i int) string {
		if i < len(tr) {
			if a, ok := tr[i].(hcl.TraverseAttr); ok {
				return a.Name
			}
		}
		return ""
	}
	switch root := tr.RootName(); root {
	case "data", "ephemeral":
		out[2] = d.refs.has(root + "." + attr(1) + "." + attr(2))
	case "module":
		out[1] = d.refs.has("module." + attr(1))
		for i := 2; i < len(tr); i++ {
			if a, ok := tr[i].(hcl.TraverseAttr); ok {
				out[i] = strings.Contains(a.Name, "-") && d.refs.outs[attr(1)][a.Name]
				break
			}
		}
	default:
		out[1] = d.refs.has(root + "." + attr(1))
	}
	return out
}

// relNames returns the steps of a relative traversal that the rule renames:
// the output, when it reads module call m's local child module.
func (d *dumper) relNames(m string, tr hcl.Traversal) map[int]bool {
	out := map[int]bool{}
	if d.refs == nil || m == "" {
		return out
	}
	for i, step := range tr {
		if a, ok := step.(hcl.TraverseAttr); ok {
			out[i] = strings.Contains(a.Name, "-") && d.refs.outs[m][a.Name]
			break
		}
	}
	return out
}

// moduleOf returns <name> when e is module.<name>, indexed or not.
func moduleOf(e hclsyntax.Expression) string {
	switch x := e.(type) {
	case *hclsyntax.IndexExpr:
		return moduleOf(x.Collection)
	case *hclsyntax.ScopeTraversalExpr:
		if len(x.Traversal) == 2 && x.Traversal.RootName() == "module" {
			if a, ok := x.Traversal[1].(hcl.TraverseAttr); ok {
				return a.Name
			}
		}
	}
	return ""
}

func blockLabels(b *hclsyntax.Block) []string {
	labels := append([]string{}, b.Labels...)
	if i, ok := namedLabel[b.Type]; ok && i < len(labels) {
		labels[i] = ren(labels[i])
	}
	return labels
}

// effectiveType is the type a nested block contributes: dynamic "x" is an x.
func effectiveType(b *hclsyntax.Block) string {
	if b.Type == "dynamic" && len(b.Labels) > 0 {
		return b.Labels[0]
	}
	return ren(b.Type)
}

func (d *dumper) dumpBody(b *hclsyntax.Body, top bool) (string, error) {
	return d.dumpBodyOf(b, top, "")
}

// legacyType gives the dump of the type expression `terraform fmt` rewrites a
// pre-0.12 variable type to (terraform's testdata/fmt/variable_type_out.tf):
// "string" to string, "list"/"map" to list(string)/map(string), and bare
// list/map/set to list(any)/map(any)/set(any).
func legacyType(e hclsyntax.Expression) (string, bool) {
	switch x := e.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		if len(x.Traversal) == 1 {
			switch word := x.Traversal.RootName(); word {
			case "list", "map", "set":
				return "call(" + word + ", expand=false, trav(any))", true
			}
		}
	case *hclsyntax.TemplateExpr:
		if len(x.Parts) == 1 {
			if l, ok := x.Parts[0].(*hclsyntax.LiteralValueExpr); ok && l.Val.Type() == cty.String {
				switch word := l.Val.AsString(); word {
				case "string":
					return "trav(string)", true
				case "list", "map":
					return "call(" + word + ", expand=false, trav(string))", true
				}
			}
		}
	}
	return "", false
}

func (d *dumper) dumpBodyOf(b *hclsyntax.Body, top bool, blockType string) (string, error) {
	var attrs []string
	for name, a := range b.Attributes {
		v, err := d.dumpExpr(a.Expr)
		if err != nil {
			return "", err
		}
		if blockType == "variable" && name == "type" {
			if t, ok := legacyType(a.Expr); ok {
				v = t
			}
		}
		attrs = append(attrs, ren(name)+" = "+v)
	}
	type blk struct{ key, dump string }
	var blocks []blk
	for _, nb := range b.Blocks {
		if top && nb.Type == "locals" {
			for name, a := range nb.Body.Attributes {
				v, err := d.dumpExpr(a.Expr)
				if err != nil {
					return "", err
				}
				attrs = append(attrs, "local "+ren(name)+" = "+v)
			}
			continue
		}
		typ := ""
		if top {
			typ = nb.Type
		}
		inner, err := d.dumpBodyOf(nb.Body, false, typ)
		if err != nil {
			return "", err
		}
		d := fmt.Sprintf("%s %q {%s}", ren(nb.Type), blockLabels(nb), inner)
		key := effectiveType(nb)
		if top {
			key = d
		}
		blocks = append(blocks, blk{key, d})
	}
	sort.Strings(attrs)
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].key < blocks[j].key })
	parts := attrs
	for _, x := range blocks {
		parts = append(parts, x.dump)
	}
	return strings.Join(parts, "; "), nil
}

func firstSemDiff(want, got string) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	from := i - 80
	if from < 0 {
		from = 0
	}
	cut := func(s string) string {
		end := i + 160
		if end > len(s) {
			end = len(s)
		}
		if from > len(s) {
			return ""
		}
		return s[from:end]
	}
	return fmt.Sprintf("source …%s…, output …%s…", cut(want), cut(got))
}

var opSymbols = map[*hclsyntax.Operation]string{
	hclsyntax.OpAdd: "+", hclsyntax.OpSubtract: "-", hclsyntax.OpMultiply: "*",
	hclsyntax.OpDivide: "/", hclsyntax.OpModulo: "%", hclsyntax.OpNegate: "neg",
	hclsyntax.OpLogicalAnd: "&&", hclsyntax.OpLogicalOr: "||", hclsyntax.OpLogicalNot: "!",
	hclsyntax.OpEqual: "==", hclsyntax.OpNotEqual: "!=",
	hclsyntax.OpGreaterThan: ">", hclsyntax.OpGreaterThanOrEqual: ">=",
	hclsyntax.OpLessThan: "<", hclsyntax.OpLessThanOrEqual: "<=",
}

func dumpValue(v cty.Value) (string, error) {
	switch {
	case v.IsNull():
		return "null", nil
	case v.Type() == cty.String:
		return fmt.Sprintf("s:%q", v.AsString()), nil
	case v.Type() == cty.Number:
		return "n:" + v.AsBigFloat().Text('g', 60), nil
	case v.Type() == cty.Bool:
		return fmt.Sprintf("b:%v", v.True()), nil
	}
	return "", fmt.Errorf("unexpected literal of type %s", v.Type().FriendlyName())
}

func (d *dumper) dumpTraversal(tr hcl.Traversal, rename map[int]bool) (string, error) {
	var b strings.Builder
	for i, step := range tr {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			b.WriteString(s.Name)
		case hcl.TraverseAttr:
			if rename[i] {
				b.WriteString("." + ren(s.Name))
			} else {
				b.WriteString("." + s.Name)
			}
		case hcl.TraverseIndex:
			k, err := dumpValue(s.Key)
			if err != nil {
				return "", err
			}
			b.WriteString("[" + k + "]")
		case hcl.TraverseSplat:
			b.WriteString("[*]")
		default:
			return "", fmt.Errorf("unexpected traverser %T", step)
		}
	}
	return b.String(), nil
}

// dumpExpr renders an expression canonically: parentheses, "${x}" for x and
// how template text is split into parts are ignored; unquoted object keys
// are renamed (they are left of "=") and object items sorted; references
// follow d.refs.
func (d *dumper) dumpExpr(e hclsyntax.Expression) (string, error) {
	list := func(xs ...hclsyntax.Expression) (string, error) {
		var out []string
		for _, x := range xs {
			if x == nil {
				out = append(out, "-")
				continue
			}
			s, err := d.dumpExpr(x)
			if err != nil {
				return "", err
			}
			out = append(out, s)
		}
		return strings.Join(out, ", "), nil
	}
	switch e := e.(type) {
	case *hclsyntax.ParenthesesExpr:
		return d.dumpExpr(e.Expression)
	case *hclsyntax.TemplateWrapExpr:
		return d.dumpExpr(e.Wrapped)
	case *hclsyntax.LiteralValueExpr:
		return dumpValue(e.Val)
	case *hclsyntax.TemplateExpr:
		var parts []string
		var lit strings.Builder
		flush := func() {
			if lit.Len() > 0 {
				parts = append(parts, fmt.Sprintf("s:%q", lit.String()))
				lit.Reset()
			}
		}
		for _, p := range e.Parts {
			if l, ok := p.(*hclsyntax.LiteralValueExpr); ok && l.Val.Type() == cty.String && !l.Val.IsNull() {
				lit.WriteString(l.Val.AsString())
				continue
			}
			flush()
			s, err := d.dumpExpr(p)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		flush()
		switch len(parts) {
		case 0:
			return `s:""`, nil
		case 1:
			return parts[0], nil
		}
		return "tmpl(" + strings.Join(parts, ", ") + ")", nil
	case *hclsyntax.TemplateJoinExpr:
		s, err := d.dumpExpr(e.Tuple)
		return "join(" + s + ")", err
	case *hclsyntax.ScopeTraversalExpr:
		s, err := d.dumpTraversal(e.Traversal, d.absNames(e.Traversal))
		return "trav(" + s + ")", err
	case *hclsyntax.RelativeTraversalExpr:
		src, err := d.dumpExpr(e.Source)
		if err != nil {
			return "", err
		}
		s, err := d.dumpTraversal(e.Traversal, d.relNames(moduleOf(e.Source), e.Traversal))
		return "rel(" + src + ", " + s + ")", err
	case *hclsyntax.FunctionCallExpr:
		args, err := list(e.Args...)
		return fmt.Sprintf("call(%s, expand=%v, %s)", e.Name, e.ExpandFinal, args), err
	case *hclsyntax.ConditionalExpr:
		s, err := list(e.Condition, e.TrueResult, e.FalseResult)
		return "cond(" + s + ")", err
	case *hclsyntax.BinaryOpExpr:
		s, err := list(e.LHS, e.RHS)
		return "bin(" + opSymbols[e.Op] + ", " + s + ")", err
	case *hclsyntax.UnaryOpExpr:
		s, err := d.dumpExpr(e.Val)
		return "un(" + opSymbols[e.Op] + ", " + s + ")", err
	case *hclsyntax.TupleConsExpr:
		s, err := list(e.Exprs...)
		return "tuple(" + s + ")", err
	case *hclsyntax.ObjectConsExpr:
		var items []string
		for _, item := range e.Items {
			k, err := d.dumpKey(item.KeyExpr)
			if err != nil {
				return "", err
			}
			v, err := d.dumpExpr(item.ValueExpr)
			if err != nil {
				return "", err
			}
			items = append(items, k+" = "+v)
		}
		sort.Strings(items)
		return "obj(" + strings.Join(items, ", ") + ")", nil
	case *hclsyntax.ForExpr:
		s, err := list(e.CollExpr, e.KeyExpr, e.ValExpr, e.CondExpr)
		return fmt.Sprintf("for(k=%s, v=%s, %s, group=%v)", e.KeyVar, e.ValVar, s, e.Group), err
	case *hclsyntax.SplatExpr:
		if rel, ok := e.Each.(*hclsyntax.RelativeTraversalExpr); ok {
			if m := moduleOf(e.Source); m != "" { // module.<name>[*].<output>
				src, err := d.dumpExpr(e.Source)
				if err != nil {
					return "", err
				}
				each, err := d.dumpTraversal(rel.Traversal, d.relNames(m, rel.Traversal))
				return "splat(" + src + ", rel(@, " + each + "))", err
			}
		}
		s, err := list(e.Source, e.Each)
		return "splat(" + s + ")", err
	case *hclsyntax.AnonSymbolExpr:
		return "@", nil
	case *hclsyntax.IndexExpr:
		s, err := list(e.Collection, e.Key)
		return "idx(" + s + ")", err
	}
	return "", fmt.Errorf("unexpected expression %T", e)
}

// dumpKey renders an object key: an unquoted key is a renamed literal name,
// a quoted key keeps its text.
func (d *dumper) dumpKey(e hclsyntax.Expression) (string, error) {
	k, ok := e.(*hclsyntax.ObjectConsKeyExpr)
	if !ok {
		return d.dumpExpr(e)
	}
	if !k.ForceNonLiteral {
		if _, isTrav := k.Wrapped.(*hclsyntax.ScopeTraversalExpr); isTrav {
			if name := hcl.ExprAsKeyword(k.Wrapped); name != "" {
				return fmt.Sprintf("s:%q", ren(name)), nil
			}
		}
		if name := hcl.ExprAsKeyword(k.Wrapped); name != "" {
			return fmt.Sprintf("s:%q", name), nil
		}
	}
	s, err := d.dumpExpr(k.Wrapped)
	return "key(" + s + ")", err
}

// ---- Comments, heredocs and headers. ----

func normLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '#' || r == '/' || r == '*' {
			return -1
		}
		return r
	}, s)
}

func commentLines(toks hclsyntax.Tokens) []string {
	var lines []string
	for _, tok := range toks {
		if tok.Type != hclsyntax.TokenComment {
			continue
		}
		for _, line := range strings.Split(string(tok.Bytes), "\n") {
			if n := normLine(line); n != "" {
				lines = append(lines, n)
			}
		}
	}
	return lines
}

func heredocs(toks hclsyntax.Tokens) []string {
	var out []string
	for _, tok := range toks {
		if tok.Type == hclsyntax.TokenOHeredoc {
			out = append(out, strings.TrimSpace(string(tok.Bytes)))
		}
	}
	return out
}

// leadingComments returns the comment tokens before the first other token.
func leadingComments(toks hclsyntax.Tokens) hclsyntax.Tokens {
	var out hclsyntax.Tokens
	for _, tok := range toks {
		switch tok.Type {
		case hclsyntax.TokenComment:
			out = append(out, tok)
		case hclsyntax.TokenNewline:
		default:
			return out
		}
	}
	return out
}

// headerLines is the file header: the leading comments, when a blank line
// separates them from the first item.
func headerLines(toks hclsyntax.Tokens) []string {
	lead := leadingComments(toks)
	if len(lead) == 0 || len(lead) == len(toks) {
		return nil
	}
	last := lead[len(lead)-1]
	lastLine := last.Range.End.Line
	if bytes.HasSuffix(last.Bytes, []byte("\n")) {
		lastLine--
	}
	for _, tok := range toks[len(lead):] {
		if tok.Type == hclsyntax.TokenNewline {
			continue
		}
		if tok.Type == hclsyntax.TokenEOF || tok.Range.Start.Line-lastLine < 2 {
			return nil
		}
		break
	}
	return commentLines(lead)
}

func equalMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return len(missing(a, b)) == 0
}

// missing returns the items of a that b lacks, counting repeats.
func missing(a, b []string) []string {
	count := map[string]int{}
	for _, x := range b {
		count[x]++
	}
	var out []string
	for _, x := range a {
		if count[x] > 0 {
			count[x]--
		} else {
			out = append(out, x)
		}
	}
	return out
}

// ---- Rules: the default configuration's order, objects, lists, naming and locals. ----

type priority struct {
	prependedBlocks, prepended, appended [][]string
}

// The default tables (cmd/internal/common.go at c29a79e). Appended lists run
// from the end of the body backwards.
var rootPriority = priority{
	prependedBlocks: [][]string{{"terraform"}, {"locals"}},
	prepended:       [][]string{{"source", "version"}, {"count"}, {"for_each"}, {"provider"}, {"providers"}},
	appended:        [][]string{{"depends_on"}, {"tags"}, {"lifecycle"}},
}

var priorities = map[string]priority{
	"root":     rootPriority,
	"module":   rootPriority,
	"resource": {prepended: [][]string{{"count"}, {"for_each"}, {"provider"}}, appended: rootPriority.appended},
	"variable": {prepended: [][]string{{"type"}}},
	"data":     {prepended: [][]string{{"count"}, {"for_each"}, {"provider"}}},
}

func locate(name string, table [][]string) (int, int, bool) {
	for outer, names := range table {
		for inner, n := range names {
			if n == name {
				return outer, inner, true
			}
		}
	}
	return 0, 0, false
}

type sortKey struct {
	group        int
	outer, inner int
	name         string
}

func less(a, b sortKey) bool {
	if a.group != b.group {
		return a.group < b.group
	}
	if a.outer != b.outer {
		return a.outer < b.outer
	}
	if a.inner != b.inner {
		return a.inner < b.inner
	}
	return a.name < b.name
}

func itemKey(item hclsyntax.Node, p priority, top bool) sortKey {
	var name string
	block, isBlock := item.(*hclsyntax.Block)
	if isBlock {
		name = block.Type
	} else {
		name = item.(*hclsyntax.Attribute).Name
	}
	if isBlock {
		if o, i, ok := locate(name, p.prependedBlocks); ok {
			return sortKey{group: 0, outer: o, inner: i}
		}
	}
	if o, i, ok := locate(name, p.prepended); ok {
		return sortKey{group: 1, outer: o, inner: i}
	}
	if o, i, ok := locate(name, p.appended); ok {
		return sortKey{group: 4, outer: -o, inner: i}
	}
	if !isBlock {
		return sortKey{group: 2, name: strings.ToLower(name)}
	}
	if top {
		return sortKey{group: 3, name: strings.ToLower(block.Type + " " + strings.Join(block.Labels, " "))}
	}
	return sortKey{group: 3, name: strings.ToLower(effectiveType(block))}
}

type ruleChecker struct {
	out      []byte
	toks     hclsyntax.Tokens
	problems []string
}

func (c *ruleChecker) fail(r hcl.Range, format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf("line %d: ", r.Start.Line)+fmt.Sprintf(format, args...))
}

func (c *ruleChecker) body(b *hclsyntax.Body, blockType string, top bool) {
	var items []hclsyntax.Node
	for _, a := range b.Attributes {
		items = append(items, a)
	}
	locals := 0
	for _, nb := range b.Blocks {
		items = append(items, nb)
		if top && nb.Type == "locals" {
			locals++
		}
	}
	if locals > 1 {
		c.fail(b.SrcRange, "%d locals blocks, want one", locals)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Range().Start.Byte < items[j].Range().Start.Byte })
	p := priorities[blockType]
	for i := 1; i < len(items); i++ {
		if less(itemKey(items[i], p, top), itemKey(items[i-1], p, top)) {
			c.fail(items[i].Range(), "%s comes after %s, against the %q order", describe(items[i]), describe(items[i-1]), blockType)
		}
	}
	for _, item := range items {
		switch n := item.(type) {
		case *hclsyntax.Attribute:
			// Names left of "=" follow the naming rule, but provider local
			// names (D12): required_providers entries and a module's providers.
			provider := blockType == "required_providers" || blockType == "module" && n.Name == "providers"
			if strings.Contains(n.Name, "-") && !provider {
				c.fail(n.SrcRange, "attribute name %q still has a '-'", n.Name)
			}
			if !provider {
				c.keys(n.Expr)
			}
			c.expr(n.Expr)
		case *hclsyntax.Block:
			if i, ok := namedLabel[n.Type]; ok && i < len(n.Labels) && strings.Contains(n.Labels[i], "-") {
				c.fail(n.Range(), "label %q still has a '-'", n.Labels[i])
			}
			if n.Body.SrcRange.Start.Line == n.Body.SrcRange.End.Line && (len(n.Body.Attributes) > 0 || len(n.Body.Blocks) > 0) {
				c.fail(n.Range(), "block %s with content on one line", n.Type)
			}
			c.body(n.Body, n.Type, false)
		}
	}
}

func describe(n hclsyntax.Node) string {
	if b, ok := n.(*hclsyntax.Block); ok {
		return strings.TrimSpace("block " + b.Type + " " + strings.Join(b.Labels, " "))
	}
	return "attribute " + n.(*hclsyntax.Attribute).Name
}

// keyText is how an object key sorts: its name, its string, or its source.
func (c *ruleChecker) keyText(e hclsyntax.Expression) string {
	if k, ok := e.(*hclsyntax.ObjectConsKeyExpr); ok && !k.ForceNonLiteral {
		if name := hcl.ExprAsKeyword(k.Wrapped); name != "" {
			return name
		}
		if t, ok := k.Wrapped.(*hclsyntax.TemplateExpr); ok && len(t.Parts) == 1 {
			if l, ok := t.Parts[0].(*hclsyntax.LiteralValueExpr); ok && l.Val.Type() == cty.String {
				return l.Val.AsString()
			}
		}
	}
	r := e.Range()
	return string(c.out[r.Start.Byte:r.End.Byte])
}

func (c *ruleChecker) hasComment(r hcl.Range) bool {
	for _, tok := range c.toks {
		if tok.Type == hclsyntax.TokenComment && tok.Range.Start.Byte >= r.Start.Byte && tok.Range.Start.Byte < r.End.Byte {
			return true
		}
	}
	return false
}

func (c *ruleChecker) expr(e hclsyntax.Expression) {
	// Inside a template interpolation, objects and lists print on one line
	// (D10: terraform fmt dedents the body after a multi-line interpolation),
	// so the layout rules do not apply there.
	var templates []hcl.Range
	hclsyntax.VisitAll(e, func(n hclsyntax.Node) hcl.Diagnostics {
		switch n.(type) {
		case *hclsyntax.TemplateExpr, *hclsyntax.TemplateWrapExpr:
			templates = append(templates, n.Range())
		}
		return nil
	})
	inTemplate := func(r hcl.Range) bool {
		for _, t := range templates {
			if r.Start.Byte >= t.Start.Byte && r.End.Byte <= t.End.Byte {
				return true
			}
		}
		return false
	}
	hclsyntax.VisitAll(e, func(n hclsyntax.Node) hcl.Diagnostics {
		if inTemplate(n.Range()) {
			return nil
		}
		switch x := n.(type) {
		case *hclsyntax.ObjectConsExpr:
			c.object(x)
		case *hclsyntax.TupleConsExpr:
			c.list(x)
		}
		return nil
	})
}

// keys reports unquoted object keys in e that keep a '-'.
func (c *ruleChecker) keys(e hclsyntax.Expression) {
	hclsyntax.VisitAll(e, func(n hclsyntax.Node) hcl.Diagnostics {
		if k, ok := n.(*hclsyntax.ObjectConsKeyExpr); ok && !k.ForceNonLiteral {
			if _, trav := k.Wrapped.(*hclsyntax.ScopeTraversalExpr); trav {
				if name := hcl.ExprAsKeyword(k.Wrapped); strings.Contains(name, "-") {
					c.fail(k.Range(), "object key %q still has a '-'", name)
				}
			}
		}
		return nil
	})
}

func (c *ruleChecker) object(o *hclsyntax.ObjectConsExpr) {
	if len(o.Items) == 0 {
		return
	}
	if o.Items[0].KeyExpr.Range().Start.Line == o.SrcRange.Start.Line ||
		o.Items[len(o.Items)-1].ValueExpr.Range().End.Line == o.SrcRange.End.Line {
		c.fail(o.SrcRange, "object with items opens or closes on an item's line")
	}
	for i := 1; i < len(o.Items); i++ {
		if o.Items[i].KeyExpr.Range().Start.Line == o.Items[i-1].ValueExpr.Range().End.Line {
			c.fail(o.Items[i].KeyExpr.Range(), "two object items on one line")
		}
		a, b := c.keyText(o.Items[i-1].KeyExpr), c.keyText(o.Items[i].KeyExpr)
		if strings.ToLower(b) < strings.ToLower(a) {
			c.fail(o.Items[i].KeyExpr.Range(), "object key %q comes after %q", b, a)
		}
	}
}

const listWidth = 80

func (c *ruleChecker) list(l *hclsyntax.TupleConsExpr) {
	r := l.SrcRange
	oneLine := r.Start.Line == r.End.Line
	if oneLine {
		if w := r.End.Byte - r.Start.Byte; w > listWidth {
			c.fail(r, "one-line list is %d characters wide, over %d", w, listWidth)
		}
		return
	}
	if len(l.Exprs) == 0 || c.hasComment(r) {
		return
	}
	// A list may span lines only when it has to: an element spans lines, or
	// its one-line form would be too wide.
	width := 2 + 2*(len(l.Exprs)-1)
	for _, x := range l.Exprs {
		xr := x.Range()
		if xr.Start.Line != xr.End.Line {
			return
		}
		width += xr.End.Byte - xr.Start.Byte
	}
	if width <= listWidth {
		c.fail(r, "list fits on one line (%d characters) but spans lines", width)
	}
	for i := 1; i < len(l.Exprs); i++ {
		if l.Exprs[i].Range().Start.Line == l.Exprs[i-1].Range().End.Line {
			c.fail(l.Exprs[i].Range(), "multi-line list with two elements on one line")
		}
	}
}

// TestFormatPerformance guards against formatting time growing faster than the file.
func TestFormatPerformance(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "# resource %d\nresource \"aws_instance\" \"r%d\" { # inline\n", i, i)
		fmt.Fprintf(&b, "  ami = \"ami-%d\"\n  count = %d\n  name = \"${var.prefix}-%d\"\n", i, i, i)
		b.WriteString("  tags = {\n    # leading\n    Name = \"n\" # inline\n    Env  = var.env\n    Team = local.team\n  }\n")
		b.WriteString("  list = [for x in var.xs : upper(x) if x != \"\"]\n}\n\n")
	}
	path := filepath.Join(t.TempDir(), "big.tf")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := format.File(path, config.Default()); err != nil {
		t.Fatalf("format: %v", err)
	}
	took := time.Since(start)
	t.Logf("formatted %d bytes in %s", b.Len(), took)
	if took > 3*time.Second {
		t.Errorf("formatting a %d-byte file took %s, want under 3s", b.Len(), took)
	}
}

// ---- CLI ----

func runCLI(bin string, args ...string) (code int, output string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return -1, "timed out after 5m"
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), out.String()
	case err != nil:
		return -1, err.Error()
	}
	return 0, out.String()
}

func panicked(output string) bool {
	return strings.Contains(output, "panic:") || strings.Contains(output, "goroutine ")
}

// TestFormatCLI runs check and apply over a copy of every repository.
func TestFormatCLI(t *testing.T) {
	bin := os.Getenv("TERRALINT_BIN")
	if bin == "" {
		t.Skip("TERRALINT_BIN not set")
	}
	var repos []string
	for _, root := range roots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != ".git" {
				repos = append(repos, filepath.Join(root, e.Name()))
			}
		}
	}
	var p problems
	checked := 0
	for _, repo := range repos {
		files := terraformFiles(t, repo)
		if len(files) == 0 {
			continue
		}
		checked += len(files)
		checkRepoCLI(t, bin, repo, files, &p)
	}
	p.report(t, "cli", checked)
}

func checkRepoCLI(t *testing.T, bin, repo string, files []string, p *problems) {
	tmp := t.TempDir()
	type entry struct {
		copy       string
		src, want  []byte
		valid      bool
		needsApply bool
		hidden     bool // under a name starting with ".", which directory mode skips (D11)
	}
	entries := make([]*entry, len(files))
	for i, f := range files {
		rel, _ := filepath.Rel(repo, f)
		dst := filepath.Join(tmp, rel)
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, src, 0o644); err != nil {
			t.Fatal(err)
		}
		hidden := false
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			hidden = hidden || strings.HasPrefix(part, ".")
		}
		entries[i] = &entry{copy: dst, src: src, valid: isValid(src, f), hidden: hidden}
	}
	parallel(entries, func(e *entry) {
		if !e.valid {
			return
		}
		want, err, _ := formatFile(e.copy)
		if err != nil {
			p.add("%s: format.File: %v", e.copy, err)
			return
		}
		e.want, e.needsApply = want, !bytes.Equal(want, e.src)
	})

	// check -f on each file, and check -d on the repository.
	parallel(entries, func(e *entry) {
		code, out := runCLI(bin, "check", "-f", e.copy)
		switch {
		case panicked(out):
			p.add("%s: check -f panicked: %s", e.copy, trim(out))
		case e.valid && !e.needsApply && (code != 0 || out != ""):
			p.add("%s: check -f on a formatted file: exit %d, output %q", e.copy, code, trim(out))
		case (!e.valid || e.needsApply) && (code != 1 || !strings.Contains(out, e.copy)):
			p.add("%s: check -f on a file that needs changes or does not parse: exit %d, output does not name it: %q", e.copy, code, trim(out))
		}
	})
	code, out := runCLI(bin, "check", "-d", tmp)
	clean := true
	for _, e := range entries {
		if e.hidden {
			if strings.Contains(out, e.copy) {
				p.add("%s: check -d named %s, which is under a name starting with \".\"", repo, e.copy)
			}
			continue
		}
		if !e.valid || e.needsApply {
			clean = false
			if !strings.Contains(out, e.copy) {
				p.add("%s: check -d did not name %s", repo, e.copy)
			}
		}
	}
	if panicked(out) || (clean && (code != 0 || out != "")) || (!clean && code != 1) {
		p.add("%s: check -d: exit %d (clean=%v), output %q", repo, code, clean, trim(out))
	}

	// apply -d writes format.File's output and leaves invalid files alone.
	allValid := true
	for _, e := range entries {
		allValid = allValid && (e.valid || e.hidden)
	}
	for round := 1; round <= 2; round++ {
		code, out := runCLI(bin, "apply", "-d", tmp)
		if panicked(out) || (allValid && code != 0) || (!allValid && code == 0) {
			p.add("%s: apply -d (run %d): exit %d, output %q", repo, round, code, trim(out))
		}
		for _, e := range entries {
			got, err := os.ReadFile(e.copy)
			want := e.src
			if e.valid && !e.hidden {
				want = e.want
			}
			if e.valid && !e.hidden && e.want == nil {
				continue
			}
			if err != nil || !bytes.Equal(got, want) {
				p.add("%s: after apply -d (run %d), %s is not format.File's output (or, if invalid, unchanged)", repo, round, e.copy)
			}
		}
	}
	code, out = runCLI(bin, "check", "-d", tmp)
	if panicked(out) || (allValid && (code != 0 || out != "")) || (!allValid && code != 1) {
		p.add("%s: check -d after apply: exit %d, output %q", repo, code, trim(out))
	}
	for _, e := range entries {
		if (e.valid || e.hidden) && strings.Contains(out, e.copy) {
			p.add("%s: check -d after apply still names %s", repo, e.copy)
		}
	}
}
