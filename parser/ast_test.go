package parser

// Checks that the AST holds what the source says for constructs that
// parser/test_files does not cover.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2"
)

func parseSource(t *testing.T, src string) *types.Root {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.tf")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := ParseTerraformFile(path)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return root
}

// clearRanges zeroes every hcl.Range so that ASTs compare by content.
func clearRanges(v reflect.Value) {
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr:
		if !v.IsNil() {
			clearRanges(v.Elem())
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(hcl.Range{}) {
			v.Set(reflect.Zero(v.Type()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			clearRanges(v.Field(i))
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			clearRanges(v.Index(i))
		}
	}
}

func lit(v interface{}, typ string) *types.LiteralValue {
	return &types.LiteralValue{Value: v, ValueType: typ}
}

func ref(parts ...string) *types.ReferenceExpr {
	return &types.ReferenceExpr{Parts: parts}
}

func TestExpressionShapes(t *testing.T) {
	item := &types.SplatItemExpr{}
	attrID := []types.TraversalElem{{Type: "attr", Name: "id"}}
	tests := []struct {
		src  string
		want types.Expression
	}{
		{"aws_instance.x[*].id", &types.SplatExpr{Source: ref("aws_instance", "x"), Each: &types.RelativeTraversalExpr{Source: item, Traversal: attrID}}},
		{"aws_instance.x.*.id", &types.SplatExpr{Source: ref("aws_instance", "x"), Each: &types.RelativeTraversalExpr{Source: item, Traversal: attrID}}},
		{"var.list[*]", &types.SplatExpr{Source: ref("var", "list"), Each: item}},
		{"local.m[var.k].id", &types.RelativeTraversalExpr{Source: &types.IndexExpr{Collection: ref("local", "m"), Key: ref("var", "k")}, Traversal: attrID}},
		{"element(var.s, 0).id", &types.RelativeTraversalExpr{Source: &types.FunctionCallExpr{Name: "element", Args: []types.Expression{ref("var", "s"), lit(int64(0), "number")}}, Traversal: attrID}},
		{`aws_instance.x[0]["k"].id`, &types.RelativeTraversalExpr{Source: ref("aws_instance", "x"), Traversal: []types.TraversalElem{
			{Type: "index", Index: lit(int64(0), "number")}, {Type: "index", Index: lit("k", "string")}, {Type: "attr", Name: "id"}}}},
		{`provider::aws::arn_parse("a")`, &types.FunctionCallExpr{Name: "provider::aws::arn_parse", Args: []types.Expression{lit("a", "string")}}},
		{"merge(var.maps...)", &types.FunctionCallExpr{Name: "merge", Args: []types.Expression{ref("var", "maps")}, ExpandFinal: true}},
		{"{for k, v in var.m : v => k...}", &types.ForMapExpr{KeyVar: "k", ValueVar: "v", Collection: ref("var", "m"), ThenKeyExpr: ref("v"), ThenValueExpr: ref("k"), Group: true}},
		{"null", lit(nil, "null")},
		{"-1.5", &types.UnaryExpr{Operator: "-", Expr: lit(1.5, "number")}},
		{"\"a-${join(\",\", [\n  \"b\",\n])}\"", &types.TemplateExpr{Parts: []types.Expression{lit("a-", "string"),
			&types.FunctionCallExpr{Name: "join", Args: []types.Expression{lit(",", "string"), &types.ArrayExpr{Items: []types.Expression{lit("b", "string")}}}}}}},
		{"<<-SCRIPT\n    #!/bin/bash\n    echo ${var.name}\n\n    echo done\n  SCRIPT\n", &types.HeredocExpr{Marker: "SCRIPT", Indented: true,
			Content: "#!/bin/bash\necho ${var.name}\n\necho done",
			Parts:   []types.Expression{lit("#!/bin/bash\necho ", "string"), ref("var", "name"), lit("\n\necho done\n", "string")}}},
		{"<<EOT\n  plain\nEOT\n", &types.HeredocExpr{Marker: "EOT", Content: "  plain", Parts: []types.Expression{lit("  plain\n", "string")}}},
	}
	for _, tt := range tests {
		root := parseSource(t, "locals {\n  a = "+tt.src+"\n}\n")
		got := root.Children[0].(*types.Block).Children[0].(*types.Attribute).Value
		clearRanges(reflect.ValueOf(got))
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", tt.src, got, tt.want)
		}
	}
}

// The heredoc in 01_complex_module.tf keeps the marker, the indent flag and
// the content written in createComplexModuleExpected.
func TestHeredocMatchesExpected(t *testing.T) {
	find := func(children []types.Body) *types.HeredocExpr {
		for _, c := range children[0].(*types.Block).Children {
			if a, ok := c.(*types.Attribute); ok && a.Name == "user_data" {
				return a.Value.(*types.HeredocExpr)
			}
		}
		t.Fatal("no user_data attribute")
		return nil
	}
	want := find(createComplexModuleExpected().(*types.Root).Children)
	root, err := ParseTerraformFile("test_files/complex_terraform_split/01_complex_module.tf")
	if err != nil {
		t.Fatal(err)
	}
	got := find(root.Children)
	if got.Marker != want.Marker || got.Indented != want.Indented || got.Content != want.Content {
		t.Errorf("heredoc: got marker %q indented %v content\n%q\nwant marker %q indented %v content\n%q",
			got.Marker, got.Indented, got.Content, want.Marker, want.Indented, want.Content)
	}
}

func TestInlineComments(t *testing.T) {
	root := parseSource(t, `variable "a" { default = 1 } # about a
locals {
  tags = {
    Name = "x" # why
    Env  = "y"
  }
}
`)
	v := root.Children[0].(*types.Block)
	if v.InlineComment != "about a" || v.Children[0].(*types.Attribute).InlineComment != "" {
		t.Errorf("one-line block: block inline %q, attribute inline %q; want the comment on the block only",
			v.InlineComment, v.Children[0].(*types.Attribute).InlineComment)
	}
	items := root.Children[1].(*types.Block).Children[0].(*types.Attribute).Value.(*types.ObjectExpr).Items
	if items[0].InlineComment != "why" || items[1].InlineComment != "" {
		t.Errorf("object items: inline %q, %q; want \"why\", \"\"", items[0].InlineComment, items[1].InlineComment)
	}
}
