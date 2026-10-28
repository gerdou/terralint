package types

import "github.com/hashicorp/hcl/v2"

// Body is the base interface that all Terraform elements must implement
type Body interface {
	BodyType() string
}

// Root represents the top-level HCL document
type Root struct {
	Children []Body

	// Header is the file header: the comments before the first item that a
	// blank line separates from the rest, one per line. They also start that
	// item's BlockComment, which holds every comment above the item.
	Header string
	// EndComment holds the comments after the last item, one per line.
	EndComment string
	// Text holds how every number, quoted string, template and heredoc in
	// the file was written, which HCL's parse discards: escapes, strip
	// markers, directives, heredoc lines and number spellings. It is keyed by
	// node and kept beside the tree, so that the expression types keep their
	// fields.
	Text map[Expression][]TextPiece
}

func (r *Root) BodyType() string {
	return "root"
}

// Block represents a Terraform block (resource, data, module, etc.)
type Block struct {
	Type          string    // The type of block (resource, data, variable, etc.)
	Labels        []string  // Labels/identifiers for the block (e.g., "aws_instance" "example")
	Range         hcl.Range // Source code position information
	InlineComment string    // The first comment on the block's first line, after its start and before the next sibling or the container's end
	BlockComment  string    // The comments on the lines between the previous sibling (or the container's start) and the block, one per line
	Children      []Body    // Nested blocks and attributes
	EndComment    string    // The comments inside the block after its last child (or its opening line, when it has none), one per line
	TrailComment  string    // The other comments of the block's own lines: after its closing brace, and in its type, labels and opening line besides InlineComment
}

func (b *Block) BodyType() string {
	return "block"
}

// Attribute represents a key-value pair in HCL
type Attribute struct {
	Name          string     // The name of the attribute
	Value         Expression // The value of the attribute
	Range         hcl.Range
	InlineComment string // The first comment on the attribute's first line, after its start and before the next sibling or the container's end
	BlockComment  string // The comments on the lines between the previous sibling (or the container's start) and the attribute, one per line
	TrailComment  string // The comments after the attribute on its last line, besides InlineComment
}

func (a *Attribute) BodyType() string {
	return "attribute"
}

// TextPiece is one piece of how a template, heredoc or number was written:
// literal text as written, or an interpolation or directive whose
// expression the printer rebuilds.
type TextPiece struct {
	Kind       string        // "text", "interp", "if", "else", "endif", "for", "endfor", or "close" for a heredoc's closing line
	Text       string        // For "text" and "close": the source as written
	Expr       Expression    // For "interp": the interpolated expression; for "if": the condition
	For        *ForArrayExpr // For "for": the loop, whose variables and collection the directive names
	StripLeft  bool          // The sequence opens with ~ (${~ or %{~)
	StripRight bool          // The sequence closes with ~ (~})
}
