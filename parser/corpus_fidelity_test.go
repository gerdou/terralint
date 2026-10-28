package parser

// Fidelity gate: ParseTerraformFile must keep everything HCL itself parsed.
//
// For every .tf/.tfvars file under test_files, and under each directory in
// $TERRALINT_CORPUS (os.PathListSeparator separated) when set, it checks that:
//   - the parser accepts exactly the files HCL accepts, and never panics;
//   - blocks, labels, attributes, their order and ranges match HCL's body;
//   - every attribute value, printed back to HCL by printExpr
//     (corpus_print_test.go) and parsed again, means the same as the source;
//   - every expression node has a source range inside the file;
//   - leading and inline comments of blocks, attributes and object items are
//     captured where the comment rules below put them.
//
// With $TERRALINT_BIN set to a built terralint binary, TestCorpusCLI runs the
// CLI over every repository (each subdirectory of a corpus root).
// $TERRALINT_REPORT, when set, receives the full list of problems, and
// $TERRALINT_INVALID_LIST the files HCL rejects.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

func corpusRoots() []string {
	roots := []string{"test_files"}
	if env := os.Getenv("TERRALINT_CORPUS"); env != "" {
		roots = append(roots, filepath.SplitList(env)...)
	}
	return roots
}

func isTerraformFile(path string) bool {
	ext := filepath.Ext(path)
	return ext == ".tf" || ext == ".tfvars"
}

func corpusFiles(t *testing.T, root string) []string {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && isTerraformFile(path) {
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

type fileResult struct {
	path     string
	valid    bool
	problems []string
	took     time.Duration
}

func runParallel[T any](items []T, fn func(T)) {
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

func reportProblems(t *testing.T, name string, checked int, problems []string) {
	t.Logf("%s: %d checked, %d problems", name, checked, len(problems))
	if path := os.Getenv("TERRALINT_REPORT"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			for _, p := range problems {
				fmt.Fprintf(f, "%s: %s\n", name, strings.ReplaceAll(p, "\n", `\n`))
			}
			f.Close()
		}
	}
	for i, p := range problems {
		if i == 50 {
			t.Errorf("... and %d more problems", len(problems)-50)
			break
		}
		t.Error(p)
	}
}

func TestFidelity(t *testing.T) {
	var files []string
	for _, root := range corpusRoots() {
		files = append(files, corpusFiles(t, root)...)
	}

	var mu sync.Mutex
	var results []fileResult
	runParallel(files, func(path string) {
		r := checkFile(path)
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	})
	sort.Slice(results, func(i, j int) bool { return results[i].path < results[j].path })

	var problems, invalid []string
	valid, slowest := 0, fileResult{}
	for _, r := range results {
		if r.valid {
			valid++
		} else {
			invalid = append(invalid, r.path)
		}
		if r.took > slowest.took {
			slowest = r
		}
		for _, p := range r.problems {
			problems = append(problems, r.path+": "+p)
		}
	}
	t.Logf("files: %d valid, %d invalid; slowest parse %s (%s)", valid, len(results)-valid, slowest.took, slowest.path)
	if path := os.Getenv("TERRALINT_INVALID_LIST"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(invalid, "\n")+"\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	reportProblems(t, "fidelity", len(results), problems)
}

// TestParsePerformance guards against parse time growing faster than the file.
func TestParsePerformance(t *testing.T) {
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
	if _, err := ParseTerraformFile(path); err != nil {
		t.Fatalf("parse: %v", err)
	}
	took := time.Since(start)
	t.Logf("parsed %d bytes in %s", b.Len(), took)
	if took > 3*time.Second {
		t.Errorf("parsing a %d-byte file took %s, want under 3s", b.Len(), took)
	}
}

const parseTimeout = time.Minute

func checkFile(path string) (r fileResult) {
	r.path = path
	content, err := os.ReadFile(path)
	if err != nil {
		r.problems = append(r.problems, "read: "+err.Error())
		return r
	}
	file, diags := hclsyntax.ParseConfig(content, path, hcl.InitialPos)
	r.valid = !diags.HasErrors()

	var root *types.Root
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		root, err = ParseTerraformFile(path)
	}()
	select {
	case <-done:
		r.took = time.Since(start)
	case <-time.After(parseTimeout):
		r.problems = append(r.problems, fmt.Sprintf("parse did not finish within %s", parseTimeout))
		return r
	}
	if strings.HasPrefix(fmt.Sprint(err), "panic:") {
		r.problems = append(r.problems, err.Error())
		return r
	}
	if !r.valid {
		if err == nil {
			r.problems = append(r.problems, "parser accepted a file HCL rejects")
		}
		return r
	}
	if err != nil {
		r.problems = append(r.problems, "parse error on valid HCL: "+err.Error())
		return r
	}
	if root == nil {
		r.problems = append(r.problems, "nil root without error")
		return r
	}

	tokens, _ := hclsyntax.LexConfig(content, path, hcl.InitialPos)
	c := &checker{path: path, content: content}
	for _, tok := range tokens {
		if tok.Type == hclsyntax.TokenComment {
			c.comments = append(c.comments, tok)
		}
	}
	body := file.Body.(*hclsyntax.Body)
	if len(body.Attributes) == 0 && len(body.Blocks) == 0 {
		c.checkCommentOnlyFile(root)
	} else {
		c.checkBody(body, root.Children, 0, len(content), "root")
	}
	seen := map[string]bool{}
	for _, p := range c.problems {
		if !seen[p] {
			seen[p] = true
			r.problems = append(r.problems, p)
		}
	}
	return r
}

type checker struct {
	path     string
	content  []byte
	comments []hclsyntax.Token
	problems []string
}

func (c *checker) fail(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

// A file with only comments may yield no children, or one typeless block
// holding all of its comments.
func (c *checker) checkCommentOnlyFile(root *types.Root) {
	if len(root.Children) == 0 {
		return
	}
	b, ok := root.Children[0].(*types.Block)
	if len(root.Children) != 1 || !ok || b.Type != "" || len(b.Labels) != 0 || len(b.Children) != 0 {
		c.fail("root: file has no blocks or attributes but AST has %d children", len(root.Children))
		return
	}
	if got, want := normComment(b.BlockComment), normComment(joinTokens(c.comments)); got != want {
		c.fail("root: comment-only file: comment %q, want %q", got, want)
	}
}

func bodyItems(body *hclsyntax.Body) []hclsyntax.Node {
	var items []hclsyntax.Node
	for _, a := range body.Attributes {
		items = append(items, a)
	}
	for _, b := range body.Blocks {
		items = append(items, b)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Range().Start.Byte < items[j].Range().Start.Byte })
	return items
}

// Comment rules:
//   - leading: every comment starting on a line after the previous sibling's
//     last line (or the container's opening line) and before the item's first
//     line, joined in order;
//   - inline: the first comment starting on the item's first line, after the
//     item's start, and before the next sibling's start or the end of the
//     container.
//
// normComment compares them ignoring whitespace and comment markers.
func (c *checker) leading(afterLine, beforeLine int) string {
	var toks []hclsyntax.Token
	for _, tok := range c.comments {
		if tok.Range.Start.Line > afterLine && tok.Range.Start.Line < beforeLine {
			toks = append(toks, tok)
		}
	}
	return joinTokens(toks)
}

func (c *checker) inline(start hcl.Pos, limit int) string {
	for _, tok := range c.comments {
		if tok.Range.Start.Line == start.Line && tok.Range.Start.Byte > start.Byte && tok.Range.Start.Byte < limit {
			return string(tok.Bytes)
		}
	}
	return ""
}

func joinTokens(toks []hclsyntax.Token) string {
	var parts []string
	for _, tok := range toks {
		parts = append(parts, string(tok.Bytes))
	}
	return strings.Join(parts, "\n")
}

func normComment(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '#' || r == '/' || r == '*' {
			return -1
		}
		return r
	}, s)
}

func (c *checker) checkComments(where, gotLead, gotInline, wantLead, wantInline string) {
	if normComment(gotLead) != normComment(wantLead) {
		c.fail("%s: leading comment %q, want %q", where, gotLead, wantLead)
	}
	if normComment(gotInline) != normComment(wantInline) {
		c.fail("%s: inline comment %q, want %q", where, gotInline, wantInline)
	}
}

func (c *checker) checkBody(body *hclsyntax.Body, children []types.Body, openLine, endByte int, where string) {
	items := bodyItems(body)
	if len(items) != len(children) {
		c.fail("%s: %d items in HCL, %d children in AST", where, len(items), len(children))
		return
	}
	prevEnd := openLine
	for i, item := range items {
		limit := endByte
		if i+1 < len(items) {
			limit = items[i+1].Range().Start.Byte
		}
		start := item.Range().Start
		wantLead := c.leading(prevEnd, start.Line)
		wantInline := c.inline(start, limit)
		switch n := item.(type) {
		case *hclsyntax.Attribute:
			loc := fmt.Sprintf("%s > attribute %s (line %d)", where, n.Name, start.Line)
			a, ok := children[i].(*types.Attribute)
			if !ok {
				c.fail("%s: AST child is %T", loc, children[i])
				break
			}
			if a.Name != n.Name {
				c.fail("%s: AST name %q", loc, a.Name)
			}
			if a.Range != n.SrcRange {
				c.fail("%s: range %v, want %v", loc, a.Range, n.SrcRange)
			}
			c.checkComments(loc, a.BlockComment, a.InlineComment, wantLead, wantInline)
			c.checkExpr(loc, n.Expr, a.Value)
		case *hclsyntax.Block:
			loc := fmt.Sprintf("%s > block %s %v (line %d)", where, n.Type, n.Labels, start.Line)
			b, ok := children[i].(*types.Block)
			if !ok {
				c.fail("%s: AST child is %T", loc, children[i])
				break
			}
			if b.Type != n.Type || !reflect.DeepEqual(append([]string{}, b.Labels...), append([]string{}, n.Labels...)) {
				c.fail("%s: AST block %s %v", loc, b.Type, b.Labels)
			}
			if b.Range != n.Range() {
				c.fail("%s: range %v, want %v", loc, b.Range, n.Range())
			}
			c.checkComments(loc, b.BlockComment, b.InlineComment, wantLead, wantInline)
			c.checkBody(n.Body, b.Children, n.OpenBraceRange.Start.Line, n.CloseBraceRange.Start.Byte, loc)
		}
		prevEnd = item.Range().End.Line
	}
}

func (c *checker) checkExpr(where string, orig hclsyntax.Expression, got types.Expression) {
	if got == nil {
		c.fail("%s: nil value", where)
		return
	}
	var wantObjs []*hclsyntax.ObjectConsExpr
	want, err := dumpExpr(orig, &wantObjs)
	if err != nil {
		c.fail("%s: dumping source expression: %v", where, err)
		return
	}

	var gotObjs []*types.ObjectExpr
	walkTypes(reflect.ValueOf(got), func(e types.Expression) {
		r := e.Range()
		if r.Filename != c.path || r.Start.Byte > r.End.Byte || r.End.Byte > len(c.content) {
			c.fail("%s: %T has range %v outside the file", where, e, r)
		}
		if o, ok := e.(*types.ObjectExpr); ok {
			gotObjs = append(gotObjs, o)
		}
	})

	printed, err := printExpr(got)
	if err != nil {
		c.fail("%s: printing AST: %v", where, err)
	} else if re, diags := hclsyntax.ParseExpression([]byte(printed), "printed.tf", hcl.InitialPos); diags.HasErrors() {
		c.fail("%s: printed AST does not parse: %s\nprinted: %s", where, diags.Error(), printed)
	} else if back, err := dumpExpr(re, new([]*hclsyntax.ObjectConsExpr)); err != nil {
		c.fail("%s: dumping printed expression: %v", where, err)
	} else if back != want {
		c.fail("%s: AST differs from source\nsource:  %s\nfrom AST: %s\nprinted: %s", where, want, back, printed)
	}

	if len(wantObjs) != len(gotObjs) {
		c.fail("%s: %d objects in HCL, %d in AST", where, len(wantObjs), len(gotObjs))
		return
	}
	for i, o := range wantObjs {
		g := gotObjs[i]
		if len(o.Items) != len(g.Items) {
			c.fail("%s: object %d has %d items in HCL, %d in AST", where, i, len(o.Items), len(g.Items))
			continue
		}
		prevEnd := o.SrcRange.Start.Line
		for j, item := range o.Items {
			limit := o.SrcRange.End.Byte
			if j+1 < len(o.Items) {
				limit = o.Items[j+1].KeyExpr.Range().Start.Byte
			}
			start := item.KeyExpr.Range().Start
			loc := fmt.Sprintf("%s > object item (line %d)", where, start.Line)
			c.checkComments(loc, g.Items[j].BlockComment, g.Items[j].InlineComment, c.leading(prevEnd, start.Line), c.inline(start, limit))
			prevEnd = item.ValueExpr.Range().End.Line
		}
	}
}

var rangeType = reflect.TypeOf(hcl.Range{})
var exprType = reflect.TypeOf((*types.Expression)(nil)).Elem()

// walkTypes visits every expression node reachable from v, parents first,
// children in field order.
func walkTypes(v reflect.Value, visit func(types.Expression)) {
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			walkTypes(v.Elem(), visit)
		}
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		if v.Type().Implements(exprType) && v.Type().Elem() != reflect.TypeOf(types.ObjectItem{}) && v.CanInterface() {
			visit(v.Interface().(types.Expression))
		}
		walkTypes(v.Elem(), visit)
	case reflect.Struct:
		if v.Type() == rangeType {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			walkTypes(v.Field(i), visit)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkTypes(v.Index(i), visit)
		}
	}
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

func dumpTraversal(tr hcl.Traversal) (string, error) {
	var b strings.Builder
	for _, step := range tr {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			b.WriteString(s.Name)
		case hcl.TraverseAttr:
			b.WriteString("." + s.Name)
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

// dumpExpr renders an HCL expression as a canonical string that ignores
// source positions and spellings that mean the same thing: parentheses,
// "${x}" for x, and how a template's literal text is split into parts.
func dumpExpr(e hclsyntax.Expression, objs *[]*hclsyntax.ObjectConsExpr) (string, error) {
	d := func(x hclsyntax.Expression) (string, error) { return dumpExpr(x, objs) }
	list := func(xs []hclsyntax.Expression) (string, error) {
		var out []string
		for _, x := range xs {
			s, err := d(x)
			if err != nil {
				return "", err
			}
			out = append(out, s)
		}
		return strings.Join(out, ", "), nil
	}
	switch e := e.(type) {
	case *hclsyntax.ParenthesesExpr:
		return d(e.Expression)
	case *hclsyntax.TemplateWrapExpr:
		return d(e.Wrapped)
	case *hclsyntax.LiteralValueExpr:
		return dumpValue(e.Val)
	case *hclsyntax.TemplateExpr:
		var parts []string
		var lit *strings.Builder
		for _, p := range e.Parts {
			if l, ok := p.(*hclsyntax.LiteralValueExpr); ok && l.Val.Type() == cty.String && !l.Val.IsNull() {
				if lit == nil {
					lit = &strings.Builder{}
				}
				lit.WriteString(l.Val.AsString())
				continue
			}
			if lit != nil && lit.Len() > 0 {
				parts = append(parts, fmt.Sprintf("s:%q", lit.String()))
			}
			lit = nil
			s, err := d(p)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		if lit != nil && lit.Len() > 0 {
			parts = append(parts, fmt.Sprintf("s:%q", lit.String()))
		}
		switch len(parts) {
		case 0:
			return `s:""`, nil
		case 1:
			return parts[0], nil
		}
		return "tmpl(" + strings.Join(parts, ", ") + ")", nil
	case *hclsyntax.TemplateJoinExpr:
		s, err := d(e.Tuple)
		return "join(" + s + ")", err
	case *hclsyntax.ScopeTraversalExpr:
		s, err := dumpTraversal(e.Traversal)
		return "trav(" + s + ")", err
	case *hclsyntax.RelativeTraversalExpr:
		src, err := d(e.Source)
		if err != nil {
			return "", err
		}
		s, err := dumpTraversal(e.Traversal)
		return "rel(" + src + ", " + s + ")", err
	case *hclsyntax.FunctionCallExpr:
		args, err := list(e.Args)
		return fmt.Sprintf("call(%s, expand=%v, %s)", e.Name, e.ExpandFinal, args), err
	case *hclsyntax.ConditionalExpr:
		s, err := list([]hclsyntax.Expression{e.Condition, e.TrueResult, e.FalseResult})
		return "cond(" + s + ")", err
	case *hclsyntax.BinaryOpExpr:
		s, err := list([]hclsyntax.Expression{e.LHS, e.RHS})
		return "bin(" + opSymbols[e.Op] + ", " + s + ")", err
	case *hclsyntax.UnaryOpExpr:
		s, err := d(e.Val)
		return "un(" + opSymbols[e.Op] + ", " + s + ")", err
	case *hclsyntax.TupleConsExpr:
		s, err := list(e.Exprs)
		return "tuple(" + s + ")", err
	case *hclsyntax.ObjectConsExpr:
		*objs = append(*objs, e)
		var items []string
		for _, item := range e.Items {
			k, err := dumpKey(item.KeyExpr, objs)
			if err != nil {
				return "", err
			}
			v, err := d(item.ValueExpr)
			if err != nil {
				return "", err
			}
			items = append(items, k+" = "+v)
		}
		return "obj(" + strings.Join(items, ", ") + ")", nil
	case *hclsyntax.ForExpr:
		parts := []string{"k=" + e.KeyVar, "v=" + e.ValVar}
		for _, x := range []hclsyntax.Expression{e.CollExpr, e.KeyExpr, e.ValExpr, e.CondExpr} {
			if x == nil {
				parts = append(parts, "-")
				continue
			}
			s, err := d(x)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return fmt.Sprintf("for(%s, group=%v)", strings.Join(parts, ", "), e.Group), nil
	case *hclsyntax.SplatExpr:
		s, err := list([]hclsyntax.Expression{e.Source, e.Each})
		return "splat(" + s + ")", err
	case *hclsyntax.AnonSymbolExpr:
		return "@", nil
	case *hclsyntax.IndexExpr:
		s, err := list([]hclsyntax.Expression{e.Collection, e.Key})
		return "idx(" + s + ")", err
	}
	return "", fmt.Errorf("unexpected expression %T", e)
}

func dumpKey(e hclsyntax.Expression, objs *[]*hclsyntax.ObjectConsExpr) (string, error) {
	k, ok := e.(*hclsyntax.ObjectConsKeyExpr)
	if !ok {
		return dumpExpr(e, objs)
	}
	if !k.ForceNonLiteral {
		if name := hcl.ExprAsKeyword(k.Wrapped); name != "" {
			return fmt.Sprintf("s:%q", name), nil
		}
	}
	s, err := dumpExpr(k.Wrapped, objs)
	return "key(" + s + ")", err
}

// TestCorpusCLI runs the terralint binary over each repository in the corpus.
// Until linter rules exist, a valid file has nothing to report or change.
func TestCorpusCLI(t *testing.T) {
	bin := os.Getenv("TERRALINT_BIN")
	if bin == "" {
		t.Skip("TERRALINT_BIN not set")
	}
	var repos []string
	for _, root := range corpusRoots() {
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

	var mu sync.Mutex
	var problems []string
	checked := 0
	add := func(ps ...string) {
		mu.Lock()
		problems = append(problems, ps...)
		mu.Unlock()
	}
	for _, repo := range repos {
		files := corpusFiles(t, repo)
		if len(files) == 0 {
			continue
		}
		checked += len(files)
		validity := map[string]bool{}
		for _, f := range files {
			content, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			_, diags := hclsyntax.ParseConfig(content, f, hcl.InitialPos)
			validity[f] = !diags.HasErrors()
		}

		runParallel(files, func(f string) {
			add(checkCLIFile(bin, f, validity[f])...)
		})
		add(checkCLIRepo(t, bin, repo, files, validity)...)
	}
	reportProblems(t, "cli", checked, problems)
}

type cliRun struct {
	code           int
	stdout, stderr string
	took           time.Duration
}

func runCLI(bin string, args ...string) cliRun {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	r := cliRun{stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
	var exitErr *exec.ExitError
	if ctx.Err() != nil {
		r.code = -1
		r.stderr += "timed out after 5m"
	} else if errors.As(err, &exitErr) {
		r.code = exitErr.ExitCode()
	} else if err != nil {
		r.code = -1
		r.stderr += err.Error()
	}
	return r
}

func panicked(r cliRun) bool {
	return strings.Contains(r.stderr, "panic:") || strings.Contains(r.stderr, "goroutine ")
}

func checkCLIFile(bin, f string, valid bool) []string {
	var ps []string
	r := runCLI(bin, "check", "-f", f)
	switch {
	case panicked(r):
		ps = append(ps, fmt.Sprintf("%s: check -f panicked: %s", f, r.stderr))
	case valid && (r.code != 0 || r.stdout != "" || r.stderr != ""):
		ps = append(ps, fmt.Sprintf("%s: check -f on a valid file: exit %d, stdout %q, stderr %q", f, r.code, trim(r.stdout), trim(r.stderr)))
	case !valid && (r.code == 0 || !strings.Contains(r.stdout+r.stderr, filepath.Base(f))):
		ps = append(ps, fmt.Sprintf("%s: check -f on an invalid file: exit %d, output does not name the file or exit is 0: %q", f, r.code, trim(r.stdout+r.stderr)))
	}
	if r.took > 5*time.Second {
		ps = append(ps, fmt.Sprintf("%s: check -f took %s", f, r.took))
	}
	return ps
}

func checkCLIRepo(t *testing.T, bin, repo string, files []string, validity map[string]bool) []string {
	var ps []string
	allValid := true
	for _, f := range files {
		allValid = allValid && validity[f]
	}

	r := runCLI(bin, "check", "-d", repo)
	out := r.stdout + r.stderr
	switch {
	case panicked(r):
		ps = append(ps, fmt.Sprintf("%s: check -d panicked: %s", repo, trim(r.stderr)))
	case allValid && (r.code != 0 || r.stdout != "" || r.stderr != ""):
		ps = append(ps, fmt.Sprintf("%s: check -d on valid files: exit %d, stdout %q, stderr %q", repo, r.code, trim(r.stdout), trim(r.stderr)))
	case !allValid && r.code == 0:
		ps = append(ps, fmt.Sprintf("%s: check -d exit 0 with invalid files", repo))
	}
	if !allValid {
		for _, f := range files {
			if !validity[f] && !strings.Contains(out, filepath.Base(f)) {
				ps = append(ps, fmt.Sprintf("%s: check -d did not report invalid file %s", repo, f))
			}
		}
	}

	// apply works on a copy; nothing may change.
	tmp := t.TempDir()
	originals := map[string][]byte{}
	for _, f := range files {
		rel, _ := filepath.Rel(repo, f)
		dst := filepath.Join(tmp, rel)
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			t.Fatal(err)
		}
		originals[dst] = content
	}
	r = runCLI(bin, "apply", "-d", tmp)
	switch {
	case panicked(r):
		ps = append(ps, fmt.Sprintf("%s: apply -d panicked: %s", repo, trim(r.stderr)))
	case allValid && (r.code != 0 || r.stderr != ""):
		ps = append(ps, fmt.Sprintf("%s: apply -d on valid files: exit %d, stderr %q", repo, r.code, trim(r.stderr)))
	case !allValid && r.code == 0:
		ps = append(ps, fmt.Sprintf("%s: apply -d exit 0 with invalid files", repo))
	}
	if r.took > 60*time.Second {
		ps = append(ps, fmt.Sprintf("%s: apply -d took %s", repo, r.took))
	}
	for dst, want := range originals {
		got, err := os.ReadFile(dst)
		if err != nil || !bytes.Equal(got, want) {
			ps = append(ps, fmt.Sprintf("%s: apply -d changed %s", repo, dst))
		}
	}
	return ps
}

func trim(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
