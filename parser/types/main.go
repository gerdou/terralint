package types

import "github.com/hashicorp/hcl/v2"

// Body is the base interface that all Terraform elements must implement
type Body interface {
	BodyType() string
}

// Root represents the top-level HCL document.
//
// Every comment field (the fields whose names end in Comment) holds its
// comments one per line. A /* */ comment is held whole, over as many lines
// as it spans; it is the only comment held starting with /*. A # or //
// comment is held without its marker and the one space after it ("# note"
// is "note"), unless no space follows the marker or the text after it
// starts with #, // or /*: then it is held whole, as written ("#####",
// "#x", "# /* x */"). So, outside the lines of a /* */ comment, a line that
// starts with # or // is a comment held whole.
type Root struct {
	Children []Body

	// Header is the file header: the comments before the first item that a
	// blank line separates from the rest, one per line. They also start that
	// item's BlockComment, which holds every comment above the item.
	Header string
	// HeaderGroups is the header in the groups that blank lines separate;
	// Header joins them.
	HeaderGroups []string
	// EndComment holds the comments after the last item, one per line.
	EndComment string
	// Text holds how the numbers, templates and heredocs of the file's
	// expressions, quoted strings included, were written, which HCL's parse
	// discards: escapes, strip markers, directives, heredoc lines and number
	// spellings. The literal keys of traversal steps (x["k"], x[0]) are not
	// in it: they keep only their value. It may also hold entries for the
	// branches of directives, which are laid out with the template holding
	// them. It is keyed by node and kept beside the tree, so that the
	// expression types keep their fields.
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
	InlineInside  bool      // InlineComment is also the InlineComment of an item inside the block that starts on the same line; it follows that item
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
	InlineInside  bool   // InlineComment is also held inside the value, which prints it: as the InlineComment of an object item there that starts on the same line, or as a comment of the part of the value it belongs to by position, wherever in the value that part is: when the outermost list or call around it has an element that started before it, that element (or a part of it); when that is an object that is not the whole value, that object (D13). It follows that item or part
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
