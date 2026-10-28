package types

import "github.com/hashicorp/hcl/v2"

// Body is the base interface that all Terraform elements must implement
type Body interface {
	BodyType() string
}

// Root represents the top-level HCL document
type Root struct {
	Children []Body
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
}

func (a *Attribute) BodyType() string {
	return "attribute"
}
