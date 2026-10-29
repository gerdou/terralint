package types

import (
	"github.com/hashicorp/hcl/v2"
)

// Expression interface represents any valid Terraform expression
type Expression interface {
	ExpressionType() string
	Range() hcl.Range
}

// ExprComments are the comments attached to an expression that is part of
// a larger one, other than those the enclosing item, object item or list
// holds. Every expression type embeds it.
type ExprComments struct {
	LeadComment string // The comments between the previous part of the enclosing expression and this one, one per line
	// TailComment holds the comments after this expression on the line it
	// ends, one per line. When this expression is the last part of an
	// enclosing expression other than a list, call or object, it also holds
	// the comments on later lines up to the end of that expression, even
	// those before tokens that hold no comments: in b = var.list[, then a
	// comment line, then 0], the comment is var.list's TailComment, though
	// it comes before the index. A comment inside an expression that has no
	// parts, such as between the steps of a reference, is its TailComment.
	TailComment string
}

// Comments returns the expression's comments, for code that handles every
// expression type alike.
func (c *ExprComments) Comments() *ExprComments { return c }

// LiteralValue represents primitive values (string, number, bool, null)
type LiteralValue struct {
	ExprComments
	Value     interface{} // string, bool, int64 (whole numbers), float64 (other numbers), or nil for null
	ValueType string      // "string", "number", "bool" or "null"
	ExprRange hcl.Range
}

func (l *LiteralValue) ExpressionType() string {
	return "literal"
}

func (l *LiteralValue) Range() hcl.Range {
	return l.ExprRange
}

// ObjectExpr represents object/map expressions
type ObjectExpr struct {
	ExprComments
	Items       []ObjectItem
	OpenComment string // The comments on the opening brace's line, after it, when the first item starts on a later line, one per line. When the object is the whole value of an item that starts on that line, the item holds the first one as its InlineComment instead; when the object is anywhere else in that item's value (a list element, a call argument, a branch of a conditional, in parentheses), the item holds the first one too, with InlineInside set
	EndComment  string // The comments after the last item (or the opening brace, when there is none), on their own lines, one per line
	ExprRange   hcl.Range
}

type ObjectItem struct {
	Key           Expression
	Value         Expression
	InlineComment string // The first comment on the item's first line, after its start and before the next item or the closing brace
	InlineInside  bool   // InlineComment is also held inside the value, which prints it: as the InlineComment of an object item there that starts on the same line, or as a comment of the part of the value it belongs to by position, wherever in the value that part is: when the outermost list or call around it has an element that started before it, that element (or a part of it); when that is an object that is not the whole value, that object (D13). It follows that item or part
	BlockComment  string // The comments on the lines between the previous item (or the opening brace) and this one, one per line
	TrailComment  string // The comments after the item on its last line, besides InlineComment
	ExprRange     hcl.Range
}

func (o *ObjectItem) ExpressionType() string {
	return "object_item"
}

func (o *ObjectItem) Range() hcl.Range {
	return o.ExprRange
}

func (o *ObjectExpr) ExpressionType() string {
	return "object"
}

func (o *ObjectExpr) Range() hcl.Range {
	return o.ExprRange
}

// ArrayExpr represents array/list expressions
type ArrayExpr struct {
	ExprComments
	Items       []Expression
	OpenComment string // When the list is an element of a list or an argument of a call: the first comment on its opening bracket's line, after it, when it ends on that line and the first element starts on a later line (a later comment on that line, or a /* */ comment over several lines, is the first element's LeadComment). When it is also the first comment on an item's first line, the item holds it too, with InlineInside set. Elsewhere, such a comment is the item's InlineComment on the item's first line, and the first element's LeadComment on any other line
	EndComment  string // The comments after the last element (or the opening bracket, when there is none), on their own lines, one per line
	ExprRange   hcl.Range
}

func (a *ArrayExpr) ExpressionType() string {
	return "array"
}

func (a *ArrayExpr) Range() hcl.Range {
	return a.ExprRange
}

// ReferenceExpr represents variable references and attribute access
type ReferenceExpr struct {
	ExprComments
	Parts     []string // e.g., ["var", "environment"] for var.environment
	ExprRange hcl.Range
}

func (r *ReferenceExpr) ExpressionType() string {
	return "reference"
}

func (r *ReferenceExpr) Range() hcl.Range {
	return r.ExprRange
}

// FunctionCallExpr represents function calls
type FunctionCallExpr struct {
	ExprComments
	Name        string // The full name, including any namespace (e.g. "provider::aws::arn_parse")
	Args        []Expression
	ExpandFinal bool   // The last argument ends in "..." and its elements are passed as separate arguments
	OpenComment string // When the call is an element of a list or an argument of a call: the first comment after its opening parenthesis on that line, as ArrayExpr.OpenComment
	EndComment  string // The comments after the last argument (or the opening parenthesis, when there is none), on their own lines, one per line
	ExprRange   hcl.Range
}

func (f *FunctionCallExpr) ExpressionType() string {
	return "function_call"
}

func (f *FunctionCallExpr) Range() hcl.Range {
	return f.ExprRange
}

// TemplateExpr represents string interpolation
type TemplateExpr struct {
	ExprComments
	Parts     []Expression // String LiteralValues and interpolated expressions. A %{if} directive is a ConditionalExpr and a %{for} directive a TemplateExpr holding one ForArrayExpr; each branch and loop body is a string LiteralValue when it is plain text ("" when empty or absent), otherwise a TemplateExpr
	ExprRange hcl.Range
}

func (t *TemplateExpr) ExpressionType() string {
	return "template"
}

func (t *TemplateExpr) Range() hcl.Range {
	return t.ExprRange
}

// ConditionalExpr represents conditional expressions (condition ? true_val : false_val)
type ConditionalExpr struct {
	ExprComments
	Condition Expression
	TrueExpr  Expression
	FalseExpr Expression
	ExprRange hcl.Range
}

func (c *ConditionalExpr) ExpressionType() string {
	return "conditional"
}

func (c *ConditionalExpr) Range() hcl.Range {
	return c.ExprRange
}

// BinaryExpr represents binary operations
type BinaryExpr struct {
	ExprComments
	Left      Expression
	Operator  string // Should support: ==, !=, <, >, <=, >=, &&, ||, +, -, *, /, %, etc.
	Right     Expression
	ExprRange hcl.Range
}

func (b *BinaryExpr) ExpressionType() string {
	return "binary"
}

func (b *BinaryExpr) Range() hcl.Range {
	return b.ExprRange
}

// ForMapExpr represents for expressions that build an object: {for k, v in map : k => v}
type ForMapExpr struct {
	ExprComments
	// Iterator variables
	ValueVar string // The value variable ("v" in "for k, v in map", "x" in "for x in xs")
	KeyVar   string // The key variable ("k" in "for k, v in map"), empty when only one variable is given

	// Collection being iterated
	Collection Expression // The collection being iterated over (e.g., "xs" in "for x in xs")

	// Result expressions
	ThenKeyExpr   Expression // The key expression ("k" in "for k, v in map : k => v")
	ThenValueExpr Expression // The value expression ("v" in "for k, v in map : k => v")

	// Filtering and grouping
	Condition Expression // Optional "if" condition (e.g., "x != null" in "for x in xs : x if x != null")
	Group     bool       // The value expression ends in "...": values with the same key are grouped into a list

	// Source location
	ExprRange hcl.Range
}

func (f *ForMapExpr) ExpressionType() string {
	return "for_map"
}

func (f *ForMapExpr) Range() hcl.Range {
	return f.ExprRange
}

// ForArrayExpr represents for expressions that build a tuple: [for x in xs : upper(x)]
type ForArrayExpr struct {
	ExprComments
	// Iterator variables
	ValueVar string // The value variable ("x" in "for x in xs", "v" in "for i, v in xs")
	KeyVar   string // The key or index variable ("i" in "for i, v in xs"), empty when only one variable is given

	// Collection being iterated
	Collection Expression // The collection being iterated over (e.g., "xs" in "for x in xs")

	// Result expressions
	ThenValueExpr Expression // The element expression ("upper(x)" in "[for x in xs : upper(x)]")

	// Filtering and grouping
	Condition Expression // Optional "if" condition (e.g., "x != null" in "for x in xs : x if x != null")

	// Source location
	ExprRange hcl.Range
}

func (f *ForArrayExpr) ExpressionType() string {
	return "for_array"
}

func (f *ForArrayExpr) Range() hcl.Range {
	return f.ExprRange
}

// SplatExpr represents splat expressions: aws_instance.server[*].id
type SplatExpr struct {
	ExprComments
	Source    Expression // Expression being splattered ("aws_instance.server")
	Each      Expression // Expression to evaluate for each element, in which a SplatItemExpr stands for the element (".id")
	ExprRange hcl.Range
}

func (s *SplatExpr) ExpressionType() string {
	return "splat"
}

func (s *SplatExpr) Range() hcl.Range {
	return s.ExprRange
}

// SplatItemExpr stands for the current element inside SplatExpr.Each: in
// x[*].id, Each is a RelativeTraversalExpr whose Source is a SplatItemExpr,
// and in x[*], Each is the SplatItemExpr itself.
type SplatItemExpr struct {
	ExprComments
	ExprRange hcl.Range
}

func (s *SplatItemExpr) ExpressionType() string {
	return "splat_item"
}

func (s *SplatItemExpr) Range() hcl.Range {
	return s.ExprRange
}

// HeredocExpr represents heredoc strings
type HeredocExpr struct {
	ExprComments
	Marker    string       // The heredoc marker (e.g., "EOT")
	Content   string       // The lines between the marker lines as written, without the last newline; for <<- the indentation they share is removed
	Indented  bool         // Whether it's an indented heredoc (<<-)
	Parts     []Expression // The parsed template, as in TemplateExpr.Parts
	ExprRange hcl.Range
}

func (h *HeredocExpr) ExpressionType() string {
	return "heredoc"
}

func (h *HeredocExpr) Range() hcl.Range {
	return h.ExprRange
}

// IndexExpr is index access whose key is not a literal: local.m[var.k],
// x[count.index] or x[-1]. A literal key, as in list[0], map["key"] or
// each.value["name"], is an "index" step of a RelativeTraversalExpr instead.
type IndexExpr struct {
	ExprComments
	Collection Expression
	Key        Expression
	ExprRange  hcl.Range
}

func (i *IndexExpr) ExpressionType() string {
	return "index"
}

func (i *IndexExpr) Range() hcl.Range {
	return i.ExprRange
}

// ParenExpr represents parenthesized expressions
type ParenExpr struct {
	ExprComments
	Expression Expression
	ExprRange  hcl.Range
}

func (p *ParenExpr) ExpressionType() string {
	return "paren"
}

func (p *ParenExpr) Range() hcl.Range {
	return p.ExprRange
}

// UnaryExpr represents unary operations
type UnaryExpr struct {
	ExprComments
	Operator  string
	Expr      Expression
	ExprRange hcl.Range
}

func (u *UnaryExpr) ExpressionType() string {
	return "unary"
}

func (u *UnaryExpr) Range() hcl.Range {
	return u.ExprRange
}

// RelativeTraversalExpr is attribute and constant index access applied to
// Source: local.m[var.k].id, element(xs, 0).id, or aws_instance.x[0].id
// (Source is the ReferenceExpr aws_instance.x, then [0] and .id). A name
// followed by attribute steps only, like var.a.b, is a ReferenceExpr.
type RelativeTraversalExpr struct {
	ExprComments
	Source    Expression
	Traversal []TraversalElem
	ExprRange hcl.Range
}

type TraversalElem struct {
	Type  string     // "attr" or "index"
	Name  string     // The attribute name, for "attr"
	Index Expression // The key as a LiteralValue, for "index"
}

func (r *RelativeTraversalExpr) ExpressionType() string {
	return "relative_traversal"
}

func (r *RelativeTraversalExpr) Range() hcl.Range {
	return r.ExprRange
}
