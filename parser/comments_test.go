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
// of the first item's BlockComment. So is an InlineComment that InlineInside
// says an item inside holds too. Root.Text is a map and is not walked.
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
				if inside := v.FieldByName("InlineInside"); f.Name == "InlineComment" && inside.IsValid() && inside.Bool() {
					continue
				}
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

// A comment on a line where an item opens and another item inside it starts
// is the InlineComment of both; InlineInside marks the outer copy.
func TestInlineInside(t *testing.T) {
	src := `locals {
  tags = { Name = "x" # name tag
    Env = "y"
  }
  b = { c = { d = 1 /* deep */ } }
}
resource "a" "b" {
  lifecycle { create_before_destroy = true /* why */ }
}
`
	root := parseSource(t, src)
	checkHeld(t, "inline inside", []byte(src), root)
	locals := root.Children[0].(*types.Block).Children
	tags, b := locals[0].(*types.Attribute), locals[1].(*types.Attribute)
	name := tags.Value.(*types.ObjectExpr).Items[0]
	c := b.Value.(*types.ObjectExpr).Items[0]
	d := c.Value.(*types.ObjectExpr).Items[0]
	lifecycle := root.Children[1].(*types.Block).Children[0].(*types.Block)
	create := lifecycle.Children[0].(*types.Attribute)
	for _, x := range []struct {
		what   string
		inline string
		inside bool
		want   string
	}{
		{"tags", tags.InlineComment, tags.InlineInside, "name tag"},
		{"Name", name.InlineComment, !name.InlineInside, "name tag"},
		{"b", b.InlineComment, b.InlineInside, "/* deep */"},
		{"c", c.InlineComment, c.InlineInside, "/* deep */"},
		{"d", d.InlineComment, !d.InlineInside, "/* deep */"},
		{"lifecycle", lifecycle.InlineComment, lifecycle.InlineInside, "/* why */"},
		{"create_before_destroy", create.InlineComment, !create.InlineInside, "/* why */"},
	} {
		if x.inline != x.want || !x.inside {
			t.Errorf("%s: inline %q (InlineInside as expected: %v), want %q", x.what, x.inline, x.inside, x.want)
		}
	}
}

// holder returns the name of the field that holds the comment text, as
// heldComments walks them.
func holder(v reflect.Value, text string) string {
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr:
		if !v.IsNil() {
			return holder(v.Elem(), text)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(hcl.Range{}) {
			return ""
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Type.Kind() == reflect.String && strings.HasSuffix(f.Name, "Comment") {
				if inside := v.FieldByName("InlineInside"); f.Name == "InlineComment" && inside.IsValid() && inside.Bool() {
					continue
				}
				if v.Field(i).String() == text {
					return f.Name
				}
				continue
			}
			if h := holder(v.Field(i), text); h != "" {
				return h
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if h := holder(v.Index(i), text); h != "" {
				return h
			}
		}
	}
	return ""
}

// An item's end-of-line comment on its first line belongs to the part of
// the value it follows, wherever the list, call or object sits (D13): after
// an element's start, to that element; after the { of an object that is
// not the whole value, to that object; else (before the first element,
// after the value's end) to the item.
func TestInlineOwner(t *testing.T) {
	for _, c := range []struct{ value, want string }{
		{"merge(var.a, { b = 1 }) # c", "InlineComment"},
		{"merge( # c\n  var.a, { b = 1 })", "InlineComment"},
		{"var.on ? [ # c\n  \"a\"] : []", "InlineComment"},
		{"{ # c\n  a = 1 }", "InlineComment"},
		{"var.on ? [\"a\", # c\n  \"b\"] : []", "TailComment"},
		{"([\"a\", # c\n  \"b\"])", "TailComment"},
		{"[[\"a\", # c\n  \"b\"]][0]", "TailComment"},
		{"var.on ? [{ # c\n  a = 1 }] : []", "OpenComment"},
		{"var.on ? { # c\n  a = 1 } : {}", "OpenComment"},
		{"{ # c\n  a = 1 }[var.k]", "OpenComment"},
		{"merge(var.a, { # c\n  b = 1 })", "OpenComment"},
	} {
		src := "x = " + c.value + "\n"
		root := parseSource(t, src)
		checkHeld(t, c.value, []byte(src), root)
		if got := holder(reflect.ValueOf(root), "c"); got != c.want {
			t.Errorf("%s: held as %s, want %s", c.value, got, c.want)
		}
	}
}

// How comments are held: a # or // comment without its marker and one
// space, unless that text would read as another comment.
func TestCommentText(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"# note", "note"},
		{"// note", "note"},
		{"#   indented", "  indented"},
		{"#####", "#####"},
		{"### Network ###", "### Network ###"},
		{"#x", "#x"},
		{"//x", "//x"},
		{"#", "#"},
		{"# # x", "# # x"},
		{"# // x", "# // x"},
		{"# /* x */", "# /* x */"},
		{"#  */", " */"},
		{"/* x */", "/* x */"},
	} {
		if got := stripCommentPrefix(c.src + "\n"); got != c.want {
			t.Errorf("%q is held as %q, want %q", c.src, got, c.want)
		}
	}
}

// An indented heredoc whose first line is a directive: HCL leaves an empty
// literal before the directive.
func TestHeredocOpeningDirective(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"locals {\n  c = <<-EOT\n    %{ for ip in var.ips ~}\n    server ${ip}\n    %{ endfor ~}\n  EOT\n}\n", "text for text interp text endfor text close"},
		{"locals {\n  c = <<-EOT\n    %{ if var.header }\n    header\n    %{ else }\n    none\n    %{ endif }\n  EOT\n}\n", "text if text else text endif text close"},
	} {
		root := parseSource(t, c.src)
		h := root.Children[0].(*types.Block).Children[0].(*types.Attribute).Value
		var kinds []string
		for _, p := range root.Text[h] {
			kinds = append(kinds, p.Kind)
		}
		if got := strings.Join(kinds, " "); got != c.want {
			t.Errorf("pieces %q, want %q", got, c.want)
		}
	}
}
