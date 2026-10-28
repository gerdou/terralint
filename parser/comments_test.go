package parser

// Checks that the AST holds every comment and how templates were written.

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// heldComments returns every comment text the AST holds, from every field
// whose name ends in Comment. Root.Header is left out: it repeats the start
// of the first item's BlockComment. Root.Text is a map and is not walked.
func heldComments(v reflect.Value, out *[]string) {
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr:
		if !v.IsNil() {
			heldComments(v.Elem(), out)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(hcl.Range{}) {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Type.Kind() == reflect.String && strings.HasSuffix(f.Name, "Comment") {
				if s := v.Field(i).String(); s != "" {
					*out = append(*out, s)
				}
				continue
			}
			heldComments(v.Field(i), out)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			heldComments(v.Index(i), out)
		}
	}
}

func normalizedLines(texts []string) map[string]int {
	count := map[string]int{}
	for _, t := range texts {
		for _, line := range strings.Split(t, "\n") {
			n := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) || r == '#' || r == '/' || r == '*' {
					return -1
				}
				return r
			}, line)
			if n != "" {
				count[n]++
			}
		}
	}
	return count
}

// checkHeld fails unless the AST of src holds each of its comments once.
func checkHeld(t *testing.T, name string, src []byte, root *types.Root) {
	t.Helper()
	toks, _ := hclsyntax.LexConfig(src, name, hcl.InitialPos)
	var want []string
	for _, tok := range toks {
		if tok.Type == hclsyntax.TokenComment {
			want = append(want, string(tok.Bytes))
		}
	}
	var got []string
	heldComments(reflect.ValueOf(root), &got)
	if w, g := normalizedLines(want), normalizedLines(got); !reflect.DeepEqual(w, g) {
		t.Errorf("%s: comment lines in the source %v, in the AST %v", name, w, g)
	}
}

const everyPosition = `# header

# leading
resource "a" /* in the labels */ "b" { # after the opening brace
  x = 1 /* inline */ /* trailing */
  list = [
    # lead
    1, # tail
    2
    # end of list
  ]
  call = f(
    a /* after a */,
    b
    # end of call
  )
  obj = {
    k = [
      1,
    ] # after the item
    # end of object
  }
  cond = (
    var.a # after the condition
    ? 1 : 2
  )
  str = "${f(
    x, # in the interpolation
  )}"
  # end of block
} # after the brace
# end of file
`

func TestCommentsHeld(t *testing.T) {
	root := parseSource(t, everyPosition)
	checkHeld(t, "every position", []byte(everyPosition), root)

	b := root.Children[0].(*types.Block)
	attr := func(name string) *types.Attribute {
		for _, c := range b.Children {
			if a, ok := c.(*types.Attribute); ok && a.Name == name {
				return a
			}
		}
		t.Fatalf("no attribute %s", name)
		return nil
	}
	list := attr("list").Value.(*types.ArrayExpr)
	call := attr("call").Value.(*types.FunctionCallExpr)
	obj := attr("obj").Value.(*types.ObjectExpr)
	cond := attr("cond").Value.(*types.ParenExpr).Expression.(*types.ConditionalExpr)
	str := attr("str").Value.(*types.TemplateExpr).Parts[0].(*types.FunctionCallExpr)
	for _, c := range []struct{ what, got, want string }{
		{"root header", root.Header, "header"},
		{"root end", root.EndComment, "end of file"},
		{"block leading", b.BlockComment, "header\nleading"},
		{"block inline", b.InlineComment, "/* in the labels */"},
		{"block trail", b.TrailComment, "after the opening brace\nafter the brace"},
		{"block end", b.EndComment, "end of block"},
		{"attribute inline", attr("x").InlineComment, "/* inline */"},
		{"attribute trail", attr("x").TrailComment, "/* trailing */"},
		{"list lead", list.Items[0].(*types.LiteralValue).LeadComment, "lead"},
		{"list tail", list.Items[0].(*types.LiteralValue).TailComment, "tail"},
		{"list end", list.EndComment, "end of list"},
		{"call tail", call.Args[0].(*types.ReferenceExpr).TailComment, "/* after a */"},
		{"call end", call.EndComment, "end of call"},
		{"object item trail", obj.Items[0].TrailComment, "after the item"},
		{"object end", obj.EndComment, "end of object"},
		{"conditional", cond.Condition.(*types.ReferenceExpr).TailComment, "after the condition"},
		{"interpolation", str.Args[0].(*types.ReferenceExpr).TailComment, "in the interpolation"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", c.what, c.got, c.want)
		}
	}
}

// Every comment in parser/test_files is held by the AST.
func TestCommentsHeldInTestFiles(t *testing.T) {
	for _, path := range corpusFiles(t, "test_files") {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		root, err := ParseTerraformFile(path)
		if err != nil {
			continue // invalid on purpose
		}
		checkHeld(t, path, src, root)
	}
}

func TestTemplateText(t *testing.T) {
	root := parseSource(t, "locals {\n  a = \"x\\u0041 $${y} ${~ var.b ~}%{ if c }d%{ else }%{ endif }%{ for k, v in m }${k}%{ endfor }\"\n  b = <<-EOT\n    hi ${x}\n  EOT\n  c = 1.50\n  d = \"${x}\"\n}\n")
	attrs := root.Children[0].(*types.Block).Children
	value := func(i int) types.Expression { return attrs[i].(*types.Attribute).Value }
	kinds := func(e types.Expression) string {
		var ks []string
		for _, p := range root.Text[e] {
			k := p.Kind
			if p.Kind == "text" || p.Kind == "close" {
				k += ":" + p.Text
			}
			if p.StripLeft {
				k = "~" + k
			}
			if p.StripRight {
				k += "~"
			}
			ks = append(ks, k)
		}
		return strings.Join(ks, " ")
	}
	for _, c := range []struct {
		i    int
		want string
	}{
		{0, `text:x\u0041 $${y}  ~interp~ if text:d else endif for interp endfor`},
		{1, "text:    hi  interp text:\n close:  EOT"},
		{2, "text:1.50"},
		{3, "interp"},
	} {
		if got := kinds(value(c.i)); got != c.want {
			t.Errorf("attribute %d: pieces %q, want %q", c.i, got, c.want)
		}
	}
	if _, ok := value(3).(*types.TemplateExpr); !ok {
		t.Errorf(`"${x}" is %T, want a TemplateExpr`, value(3))
	}
	for _, p := range root.Text[value(0)] {
		if p.Kind == "for" && (p.For == nil || p.For.KeyVar != "k" || p.For.ValueVar != "v") {
			t.Errorf("for directive: %+v", p.For)
		}
	}
}
