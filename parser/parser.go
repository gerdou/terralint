package parser

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// ParseTerraformFile reads a Terraform file and parses it into an AST
func ParseTerraformFile(filePath string) (*types.Root, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	file, diags := hclsyntax.ParseConfig(content, filePath, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("failed to parse HCL: %s", diags.Error())
	}

	// Lex once: comments are looked up by line, heredocs by start offset,
	// and template text by position.
	c := &converter{content: content, heredocs: map[int]hclsyntax.Token{}, text: map[types.Expression][]types.TextPiece{}}
	c.tokens, _ = hclsyntax.LexConfig(content, filePath, hcl.InitialPos)
	for _, tok := range c.tokens {
		switch tok.Type {
		case hclsyntax.TokenComment:
			c.comments = append(c.comments, tok)
		case hclsyntax.TokenOHeredoc:
			c.heredocs[tok.Range.Start.Byte] = tok
		}
	}
	c.claimed = make([]bool, len(c.comments))
	c.inlines = make([]int, len(c.comments))

	root := &types.Root{Children: make([]types.Body, 0), Text: c.text}
	body := file.Body.(*hclsyntax.Body)

	// A file with only comments becomes one typeless block holding them.
	if len(body.Attributes) == 0 && len(body.Blocks) == 0 {
		if len(c.comments) > 0 {
			root.Children = append(root.Children, &types.Block{BlockComment: c.leading(0, len(content)+1)})
		}
		return root, nil
	}

	root.Children, root.EndComment, err = c.body(body, 0, 0, len(content))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	root.HeaderGroups = c.header(sortedItems(body)[0].Range().Start.Line)
	root.Header = strings.Join(root.HeaderGroups, "\n")
	return root, nil
}

type converter struct {
	content  []byte
	tokens   hclsyntax.Tokens
	comments []hclsyntax.Token       // in source order
	claimed  []bool                  // comments already held by the AST, by index
	inlines  []int                   // how many items hold each comment as their InlineComment
	heredocs map[int]hclsyntax.Token // heredoc openers by start byte
	text     map[types.Expression][]types.TextPiece
}

// stripCommentPrefix returns a comment as the AST holds it (see
// types.Root), without trailing space. A # or // comment loses its marker
// and the one space after it, so that indentation inside the text is kept.
// It is kept whole, marker included, when no space follows the marker or the
// text after it starts like a comment (#, //, /*): the text alone would then
// read as another comment. A /* */ comment is kept whole.
func stripCommentPrefix(comment string) string {
	comment = strings.TrimRightFunc(comment, unicode.IsSpace)
	for _, marker := range []string{"//", "#"} {
		if strings.HasPrefix(comment, marker) {
			text, spaced := strings.CutPrefix(comment[len(marker):], " ")
			if !spaced || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*") {
				return comment
			}
			return text
		}
	}
	return strings.TrimSpace(comment)
}

// commentsFrom returns the index of the first comment that starts on line or later.
func (c *converter) commentsFrom(line int) int {
	return sort.Search(len(c.comments), func(i int) bool { return c.comments[i].Range.Start.Line >= line })
}

// commentsIn returns the indexes of the comments not yet held that start
// in the byte range [from, to).
func (c *converter) commentsIn(from, to int) []int {
	var out []int
	for i := sort.Search(len(c.comments), func(i int) bool { return c.comments[i].Range.Start.Byte >= from }); i < len(c.comments) && c.comments[i].Range.Start.Byte < to; i++ {
		if !c.claimed[i] {
			out = append(out, i)
		}
	}
	return out
}

// join claims the comments at idx and joins their text, one per line.
func (c *converter) join(idx []int) string {
	var texts []string
	for _, i := range idx {
		c.claimed[i] = true
		texts = append(texts, stripCommentPrefix(string(c.comments[i].Bytes)))
	}
	return strings.Join(texts, "\n")
}

// leading joins the comments that start after afterLine and before beforeLine.
func (c *converter) leading(afterLine, beforeLine int) string {
	var idx []int
	for i := c.commentsFrom(afterLine + 1); i < len(c.comments) && c.comments[i].Range.Start.Line < beforeLine; i++ {
		idx = append(idx, i)
	}
	return c.join(idx)
}

// inline returns the first comment on start's line that starts after start
// and before the byte offset limit. An item inside this one that starts on
// the same line can have the same comment as its InlineComment; inside,
// called once the item's insides are converted, reports whether one does.
func (c *converter) inline(start hcl.Pos, limit int) (text string, inside func() bool) {
	for i := c.commentsFrom(start.Line); i < len(c.comments); i++ {
		tok := c.comments[i]
		if tok.Range.Start.Line > start.Line || tok.Range.Start.Byte >= limit {
			break
		}
		if tok.Range.Start.Byte > start.Byte {
			c.inlines[i]++
			n := c.inlines[i]
			return c.join([]int{i}), func() bool { return c.inlines[i] > n }
		}
	}
	return "", func() bool { return false }
}

// lastLine is the line a comment ends on; a line comment's token takes in
// the newline after it.
func lastLine(tok hclsyntax.Token) int {
	if bytes.HasSuffix(tok.Bytes, []byte("\n")) {
		return tok.Range.End.Line - 1
	}
	return tok.Range.End.Line
}

// header returns the file header: the comments above the first item, which
// starts on line first, up to the last blank line among them, in the groups
// that blank lines separate.
func (c *converter) header(first int) []string {
	var above []hclsyntax.Token
	for _, tok := range c.comments {
		if tok.Range.Start.Line >= first {
			break
		}
		above = append(above, tok)
	}
	var groups, texts []string
	for i, tok := range above {
		texts = append(texts, stripCommentPrefix(string(tok.Bytes)))
		next := first
		if i+1 < len(above) {
			next = above[i+1].Range.Start.Line
		}
		if next-lastLine(tok) >= 2 {
			groups = append(groups, strings.Join(texts, "\n"))
			texts = nil
		}
	}
	return groups
}

func sortedItems(body *hclsyntax.Body) []hclsyntax.Node {
	var items []hclsyntax.Node
	for _, attr := range body.Attributes {
		items = append(items, attr)
	}
	for _, block := range body.Blocks {
		items = append(items, block)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Range().Start.Byte < items[j].Range().Start.Byte })
	return items
}

func appendComment(field *string, text string) {
	switch {
	case text == "":
	case *field == "":
		*field = text
	default:
		*field += "\n" + text
	}
}

// body converts the attributes and blocks of a body in source order. openLine
// is the line the body opens on (0 for a file), and the body runs from byte
// startByte to endByte. It returns the comments after the last item too.
func (c *converter) body(body *hclsyntax.Body, openLine, startByte, endByte int) ([]types.Body, string, error) {
	items := sortedItems(body)
	children := make([]types.Body, 0, len(items))
	trails := make([]*string, len(items))
	prevEndLine := openLine
	for i, item := range items {
		limit := endByte
		if i+1 < len(items) {
			limit = items[i+1].Range().Start.Byte
		}
		start := item.Range().Start
		blockComment := c.leading(prevEndLine, start.Line)
		inlineComment, inlineInside := c.inline(start, limit)

		switch item := item.(type) {
		case *hclsyntax.Attribute:
			value, err := c.expr(item.Expr)
			if err != nil {
				return nil, "", fmt.Errorf("failed to convert attribute %s (line %d): %w", item.Name, start.Line, err)
			}
			c.attachValue(value, item.SrcRange)
			attr := &types.Attribute{
				Name:          item.Name,
				Value:         value,
				Range:         item.SrcRange,
				InlineComment: inlineComment,
				InlineInside:  inlineInside(),
				BlockComment:  blockComment,
			}
			children = append(children, attr)
			trails[i] = &attr.TrailComment
		case *hclsyntax.Block:
			nested, end, err := c.body(item.Body, item.OpenBraceRange.Start.Line, item.OpenBraceRange.End.Byte, item.CloseBraceRange.Start.Byte)
			if err != nil {
				return nil, "", fmt.Errorf("failed to convert block %s (line %d): %w", item.Type, start.Line, err)
			}
			block := &types.Block{
				Type:          item.Type,
				Labels:        item.Labels,
				Range:         item.Range(),
				Children:      nested,
				InlineComment: inlineComment,
				InlineInside:  inlineInside(),
				BlockComment:  blockComment,
				EndComment:    end,
			}
			// What is left in the block's range is in its type, labels or
			// opening line.
			block.TrailComment = c.join(c.commentsIn(start.Byte, item.Range().End.Byte))
			children = append(children, block)
			trails[i] = &block.TrailComment
		}
		prevEndLine = item.Range().End.Line
	}

	// The rest: comments after an item on its last line trail it; those on
	// later lines after the last item end the body. A comment before an item
	// on its line joins that item's trailing comments. Those on a block's
	// opening line are left to the block.
	var end []int
	for _, i := range c.commentsIn(startByte, endByte) {
		tok := c.comments[i]
		k := sort.Search(len(items), func(k int) bool { return items[k].Range().End.Byte > tok.Range.Start.Byte })
		switch {
		case openLine > 0 && tok.Range.Start.Line == openLine:
		case k > 0 && tok.Range.Start.Line == items[k-1].Range().End.Line:
			appendComment(trails[k-1], c.join([]int{i}))
		case k < len(items) && tok.Range.Start.Line == items[k].Range().Start.Line:
			appendComment(trails[k], c.join([]int{i}))
		default:
			end = append(end, i)
		}
	}
	return children, c.join(end), nil
}

// attachValue gives the comments left in an attribute's or object item's
// range to its value and the expressions inside it.
func (c *converter) attachValue(value types.Expression, rng hcl.Range) {
	if cs := c.commentsIn(rng.Start.Byte, rng.End.Byte); len(cs) > 0 {
		c.attach(&valueHolder{value}, cs)
	}
}

// valueHolder is the container of a value, for attach.
type valueHolder struct{ types.Expression }

// children returns the parts of e that comments attach to, in source order.
func (c *converter) children(e types.Expression) []types.Expression {
	var ch []types.Expression
	add := func(xs ...types.Expression) {
		for _, x := range xs {
			if x != nil {
				ch = append(ch, x)
			}
		}
	}
	switch e := e.(type) {
	case *valueHolder:
		add(e.Expression)
	case *keyValue:
		add(e.Key, e.Value)
	case *types.ArrayExpr:
		add(e.Items...)
	case *types.FunctionCallExpr:
		add(e.Args...)
	case *types.TemplateExpr, *types.HeredocExpr:
		for _, p := range c.text[e] {
			switch {
			case p.Expr != nil:
				add(p.Expr)
			case p.For != nil:
				add(p.For.Collection)
			}
		}
	case *types.ConditionalExpr:
		add(e.Condition, e.TrueExpr, e.FalseExpr)
	case *types.BinaryExpr:
		add(e.Left, e.Right)
	case *types.UnaryExpr:
		add(e.Expr)
	case *types.ParenExpr:
		add(e.Expression)
	case *types.ForArrayExpr:
		add(e.Collection, e.ThenValueExpr, e.Condition)
	case *types.ForMapExpr:
		add(e.Collection, e.ThenKeyExpr, e.ThenValueExpr, e.Condition)
	case *types.SplatExpr:
		add(e.Source)
	case *types.IndexExpr:
		add(e.Collection, e.Key)
	case *types.RelativeTraversalExpr:
		add(e.Source)
	}
	sort.SliceStable(ch, func(i, j int) bool { return ch[i].Range().Start.Byte < ch[j].Range().Start.Byte })
	return ch
}

type commented interface{ Comments() *types.ExprComments }

// attach gives the comments cs (indexes, in source order, inside e's range)
// to the part of e they are in or next to: a comment on the line a part ends
// trails it, any other leads the next part; after the last part, it ends a
// list, call or object, or trails the last part.
func (c *converter) attach(e types.Expression, cs []int) {
	if o, ok := e.(*types.ObjectExpr); ok {
		c.attachObject(o, cs)
		return
	}
	ch := c.children(e)
	buckets := make([][]int, len(ch))
	for _, i := range cs {
		tok := c.comments[i]
		pos := tok.Range.Start.Byte
		k := sort.Search(len(ch), func(k int) bool { return ch[k].Range().End.Byte > pos })
		switch {
		case k < len(ch) && ch[k].Range().Start.Byte <= pos:
			buckets[k] = append(buckets[k], i)
		case k > 0 && tok.Range.Start.Line == ch[k-1].Range().End.Line:
			appendComment(&ch[k-1].(commented).Comments().TailComment, c.join([]int{i}))
		case k < len(ch):
			appendComment(&ch[k].(commented).Comments().LeadComment, c.join([]int{i}))
		default:
			switch e := e.(type) {
			case *types.ArrayExpr:
				appendComment(&e.EndComment, c.join([]int{i}))
			case *types.FunctionCallExpr:
				appendComment(&e.EndComment, c.join([]int{i}))
			default:
				target := e
				if len(ch) > 0 {
					target = ch[len(ch)-1]
				}
				if t, ok := target.(commented); ok {
					appendComment(&t.Comments().TailComment, c.join([]int{i}))
				}
			}
		}
	}
	for k, b := range buckets {
		if len(b) > 0 {
			c.attach(ch[k], b)
		}
	}
}

// attachObject is attach for an object, whose items hold their own leading
// and inline comments.
func (c *converter) attachObject(o *types.ObjectExpr, cs []int) {
	buckets := make([][]int, len(o.Items))
	for _, i := range cs {
		tok := c.comments[i]
		pos := tok.Range.Start.Byte
		k := sort.Search(len(o.Items), func(k int) bool { return o.Items[k].Value.Range().End.Byte > pos })
		switch {
		case k < len(o.Items) && o.Items[k].Key.Range().Start.Byte <= pos:
			buckets[k] = append(buckets[k], i)
		case k > 0 && tok.Range.Start.Line == o.Items[k-1].Value.Range().End.Line:
			appendComment(&o.Items[k-1].TrailComment, c.join([]int{i}))
		case k == 0 && len(o.Items) > 0 && tok.Range.Start.Line == o.ExprRange.Start.Line && tok.Range.Start.Line < o.Items[0].Key.Range().Start.Line:
			// On the opening brace's line: about the whole object.
			appendComment(&o.OpenComment, c.join([]int{i}))
		case k < len(o.Items):
			key := o.Items[k].Key.(commented).Comments()
			appendComment(&key.LeadComment, c.join([]int{i}))
		default:
			appendComment(&o.EndComment, c.join([]int{i}))
		}
	}
	for k, b := range buckets {
		if len(b) > 0 {
			item := &o.Items[k]
			c.attach(&keyValue{item.Key, item.Value}, b)
		}
	}
}

// keyValue is an object item's key and value, for attach.
type keyValue struct{ Key, Value types.Expression }

func (kv *keyValue) ExpressionType() string { return "key_value" }
func (kv *keyValue) Range() hcl.Range       { return hcl.RangeBetween(kv.Key.Range(), kv.Value.Range()) }

var operators = map[*hclsyntax.Operation]string{
	hclsyntax.OpAdd: "+", hclsyntax.OpSubtract: "-", hclsyntax.OpMultiply: "*",
	hclsyntax.OpDivide: "/", hclsyntax.OpModulo: "%", hclsyntax.OpNegate: "-",
	hclsyntax.OpLogicalAnd: "&&", hclsyntax.OpLogicalOr: "||", hclsyntax.OpLogicalNot: "!",
	hclsyntax.OpEqual: "==", hclsyntax.OpNotEqual: "!=",
	hclsyntax.OpGreaterThan: ">", hclsyntax.OpGreaterThanOrEqual: ">=",
	hclsyntax.OpLessThan: "<", hclsyntax.OpLessThanOrEqual: "<=",
}

func literal(v cty.Value, rng hcl.Range) *types.LiteralValue {
	l := &types.LiteralValue{ValueType: v.Type().FriendlyName(), ExprRange: rng}
	switch {
	case v.IsNull():
		l.ValueType = "null"
	case v.Type() == cty.String:
		l.Value = v.AsString()
	case v.Type() == cty.Bool:
		l.Value = v.True()
	case v.Type() == cty.Number:
		bf := v.AsBigFloat()
		if i, acc := bf.Int64(); acc == big.Exact {
			l.Value = i
		} else {
			l.Value, _ = bf.Float64()
		}
	}
	return l
}

func (c *converter) exprs(exprs []hclsyntax.Expression) ([]types.Expression, error) {
	result := make([]types.Expression, len(exprs))
	for i, expr := range exprs {
		converted, err := c.expr(expr)
		if err != nil {
			return nil, err
		}
		result[i] = converted
	}
	return result, nil
}

// traversal converts traversal steps after a source expression.
func traversal(steps hcl.Traversal) ([]types.TraversalElem, error) {
	result := make([]types.TraversalElem, len(steps))
	for i, step := range steps {
		switch s := step.(type) {
		case hcl.TraverseAttr:
			result[i] = types.TraversalElem{Type: "attr", Name: s.Name}
		case hcl.TraverseIndex:
			result[i] = types.TraversalElem{Type: "index", Index: literal(s.Key, s.SrcRange)}
		default:
			return nil, fmt.Errorf("unsupported traversal step %T", step)
		}
	}
	return result, nil
}

// heredoc builds the HeredocExpr for a template that opens with the heredoc
// token open.
func (c *converter) heredoc(open hclsyntax.Token, parts []types.Expression, rng hcl.Range) *types.HeredocExpr {
	// The closing marker is on the last line; the body ends where that line starts.
	bodyEnd := bytes.LastIndexByte(c.content[:rng.End.Byte], '\n') + 1
	body := strings.TrimSuffix(string(c.content[open.Range.End.Byte:bodyEnd]), "\n")
	h := &types.HeredocExpr{
		Marker:    strings.TrimSpace(strings.TrimLeft(string(open.Bytes), "<-")),
		Indented:  bytes.HasPrefix(open.Bytes, []byte("<<-")),
		Parts:     parts,
		ExprRange: rng,
	}
	lines := strings.Split(body, "\n")
	if h.Indented {
		indent := -1
		for _, line := range lines {
			if n := len(line) - len(strings.TrimLeft(line, " \t")); strings.TrimSpace(line) != "" && (indent < 0 || n < indent) {
				indent = n
			}
		}
		for i, line := range lines {
			lines[i] = line[min(max(indent, 0), len(line)-len(strings.TrimLeft(line, " \t"))):]
		}
	}
	h.Content = strings.Join(lines, "\n")
	return h
}

// tokenAt returns the index of the first token that starts at or after pos.
func (c *converter) tokenAt(pos int) int {
	return sort.Search(len(c.tokens), func(i int) bool { return c.tokens[i].Range.Start.Byte >= pos })
}

// sequenceEnd returns the index of the token that closes the ${ or %{
// sequence opened by the token at i.
func (c *converter) sequenceEnd(i int) int {
	depth := 0
	for ; i < len(c.tokens); i++ {
		switch c.tokens[i].Type {
		case hclsyntax.TokenTemplateInterp, hclsyntax.TokenTemplateControl:
			depth++
		case hclsyntax.TokenTemplateSeqEnd:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(c.tokens) - 1
}

// partIn returns the first of parts, from *next on, that starts at or after
// from and ends at or before to, and moves *next past it. Parts of no width
// are skipped: HCL leaves an empty literal where it strips the indentation
// before a directive that starts a <<- heredoc line, and no interpolation or
// directive is empty.
func partIn(parts []types.Expression, next *int, from, to int) types.Expression {
	for ; *next < len(parts); *next++ {
		r := parts[*next].Range()
		if r.Start.Byte < r.End.Byte && r.Start.Byte >= from && r.End.Byte <= to {
			*next++
			return parts[*next-1]
		}
	}
	return nil
}

// templateParts returns the parts of a template or of a directive's branch
// or loop body, as converted.
func templateParts(e types.Expression) []types.Expression {
	if t, ok := e.(*types.TemplateExpr); ok {
		return t.Parts
	}
	return []types.Expression{e}
}

// templateText lays out the template with converted parts parts, which
// spans rng, as written: its literal text, and its interpolations and
// directives with the expressions they hold.
func (c *converter) templateText(rng hcl.Range, parts []types.Expression) ([]types.TextPiece, error) {
	type frame struct {
		parts []types.Expression
		next  int
		cond  *types.ConditionalExpr
	}
	stack := []*frame{{parts: parts}}
	var pieces []types.TextPiece
	i := c.tokenAt(rng.Start.Byte) + 1 // after the opening quote or heredoc marker
	for ; i < len(c.tokens) && c.tokens[i].Range.Start.Byte < rng.End.Byte; i++ {
		tok := c.tokens[i]
		top := stack[len(stack)-1]
		switch tok.Type {
		case hclsyntax.TokenQuotedLit, hclsyntax.TokenStringLit:
			if n := len(pieces); n > 0 && pieces[n-1].Kind == "text" {
				pieces[n-1].Text += string(tok.Bytes)
			} else {
				pieces = append(pieces, types.TextPiece{Kind: "text", Text: string(tok.Bytes)})
			}
		case hclsyntax.TokenCHeredoc:
			pieces = append(pieces, types.TextPiece{Kind: "close", Text: string(tok.Bytes)})
		case hclsyntax.TokenTemplateInterp, hclsyntax.TokenTemplateControl:
			end := c.sequenceEnd(i)
			p := types.TextPiece{StripLeft: bytes.HasSuffix(tok.Bytes, []byte("~")), StripRight: bytes.HasPrefix(c.tokens[end].Bytes, []byte("~"))}
			if tok.Type == hclsyntax.TokenTemplateInterp {
				p.Kind = "interp"
				p.Expr = partIn(top.parts, &top.next, tok.Range.End.Byte, c.tokens[end].Range.Start.Byte)
				if p.Expr == nil {
					return nil, fmt.Errorf("no expression for the interpolation at %s", tok.Range)
				}
			} else {
				// The keyword is the first token after %{ that is not a line
				// break or a comment.
				k := i + 1
				for k < end && (c.tokens[k].Type == hclsyntax.TokenNewline || c.tokens[k].Type == hclsyntax.TokenComment) {
					k++
				}
				p.Kind = string(c.tokens[k].Bytes)
				switch p.Kind {
				case "if":
					d, _ := partIn(top.parts, &top.next, tok.Range.Start.Byte, rng.End.Byte).(*types.ConditionalExpr)
					if d == nil {
						return nil, fmt.Errorf("no if directive at %s", tok.Range)
					}
					p.Expr = d.Condition
					stack = append(stack, &frame{parts: templateParts(d.TrueExpr), cond: d})
				case "else":
					if top.cond == nil {
						return nil, fmt.Errorf("no if directive for the else at %s", tok.Range)
					}
					top.parts, top.next = templateParts(top.cond.FalseExpr), 0
				case "for":
					t, _ := partIn(top.parts, &top.next, tok.Range.Start.Byte, rng.End.Byte).(*types.TemplateExpr)
					if t == nil || len(t.Parts) != 1 {
						return nil, fmt.Errorf("no for directive at %s", tok.Range)
					}
					p.For, _ = t.Parts[0].(*types.ForArrayExpr)
					if p.For == nil {
						return nil, fmt.Errorf("no for directive at %s", tok.Range)
					}
					stack = append(stack, &frame{parts: templateParts(p.For.ThenValueExpr)})
				case "endif", "endfor":
					if len(stack) > 1 {
						stack = stack[:len(stack)-1]
					}
				}
			}
			pieces = append(pieces, p)
			i = end
		}
	}
	return pieces, nil
}

func (c *converter) expr(expr hclsyntax.Expression) (types.Expression, error) {
	rng := expr.Range()
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		l := literal(e.Val, rng)
		if l.ValueType == "number" {
			c.text[l] = []types.TextPiece{{Kind: "text", Text: string(c.content[rng.Start.Byte:rng.End.Byte])}}
		}
		return l, nil
	case *hclsyntax.TemplateExpr:
		parts, err := c.exprs(e.Parts)
		if err != nil {
			return nil, err
		}
		var out types.Expression = &types.TemplateExpr{Parts: parts, ExprRange: rng}
		if open, ok := c.heredocs[rng.Start.Byte]; ok {
			out = c.heredoc(open, parts, rng)
		} else if len(parts) == 1 {
			if lit, ok := parts[0].(*types.LiteralValue); ok {
				lit.ExprRange = rng
				out = lit
			}
		}
		// A directive's branch or loop body is laid out with the template
		// that holds it; only quoted templates and heredocs get their own.
		if open := c.tokens[c.tokenAt(rng.Start.Byte)].Type; open == hclsyntax.TokenOQuote || open == hclsyntax.TokenOHeredoc {
			if c.text[out], err = c.templateText(rng, parts); err != nil {
				return nil, err
			}
		}
		return out, nil
	case *hclsyntax.TemplateWrapExpr:
		// "${x}": a template whose only part is x.
		wrapped, err := c.expr(e.Wrapped)
		if err != nil {
			return nil, err
		}
		out := &types.TemplateExpr{Parts: []types.Expression{wrapped}, ExprRange: rng}
		if c.text[out], err = c.templateText(rng, out.Parts); err != nil {
			return nil, err
		}
		return out, nil
	case *hclsyntax.TemplateJoinExpr:
		tuple, err := c.expr(e.Tuple)
		if err != nil {
			return nil, err
		}
		return &types.TemplateExpr{Parts: []types.Expression{tuple}, ExprRange: rng}, nil
	case *hclsyntax.ScopeTraversalExpr:
		// The name and the attribute steps after it form a reference; any
		// later step makes a relative traversal from that reference.
		n := 1
		for n < len(e.Traversal) {
			if _, ok := e.Traversal[n].(hcl.TraverseAttr); !ok {
				break
			}
			n++
		}
		parts := make([]string, n)
		for i, step := range e.Traversal[:n] {
			switch s := step.(type) {
			case hcl.TraverseRoot:
				parts[i] = s.Name
			case hcl.TraverseAttr:
				parts[i] = s.Name
			}
		}
		ref := &types.ReferenceExpr{Parts: parts, ExprRange: hcl.RangeBetween(e.Traversal[0].SourceRange(), e.Traversal[n-1].SourceRange())}
		if n == len(e.Traversal) {
			return ref, nil
		}
		steps, err := traversal(e.Traversal[n:])
		if err != nil {
			return nil, err
		}
		return &types.RelativeTraversalExpr{Source: ref, Traversal: steps, ExprRange: rng}, nil
	case *hclsyntax.RelativeTraversalExpr:
		source, err := c.expr(e.Source)
		if err != nil {
			return nil, err
		}
		steps, err := traversal(e.Traversal)
		if err != nil {
			return nil, err
		}
		return &types.RelativeTraversalExpr{Source: source, Traversal: steps, ExprRange: rng}, nil
	case *hclsyntax.FunctionCallExpr:
		args, err := c.exprs(e.Args)
		if err != nil {
			return nil, err
		}
		return &types.FunctionCallExpr{Name: e.Name, Args: args, ExpandFinal: e.ExpandFinal, ExprRange: rng}, nil
	case *hclsyntax.ObjectConsExpr:
		items := make([]types.ObjectItem, len(e.Items))
		prevEndLine := rng.Start.Line
		for i, item := range e.Items {
			limit := rng.End.Byte
			if i+1 < len(e.Items) {
				limit = e.Items[i+1].KeyExpr.Range().Start.Byte
			}
			start := item.KeyExpr.Range().Start
			// Claim the item's own comments before those inside its value.
			blockComment := c.leading(prevEndLine, start.Line)
			inlineComment, inlineInside := c.inline(start, limit)
			key, err := c.expr(item.KeyExpr)
			if err != nil {
				return nil, err
			}
			value, err := c.expr(item.ValueExpr)
			if err != nil {
				return nil, err
			}
			items[i] = types.ObjectItem{
				Key:           key,
				Value:         value,
				InlineComment: inlineComment,
				InlineInside:  inlineInside(),
				BlockComment:  blockComment,
				ExprRange:     hcl.RangeBetween(item.KeyExpr.Range(), item.ValueExpr.Range()),
			}
			prevEndLine = item.ValueExpr.Range().End.Line
		}
		return &types.ObjectExpr{Items: items, ExprRange: rng}, nil
	case *hclsyntax.ObjectConsKeyExpr:
		return c.expr(e.Wrapped)
	case *hclsyntax.TupleConsExpr:
		items, err := c.exprs(e.Exprs)
		if err != nil {
			return nil, err
		}
		return &types.ArrayExpr{Items: items, ExprRange: rng}, nil
	case *hclsyntax.BinaryOpExpr:
		left, err := c.expr(e.LHS)
		if err != nil {
			return nil, err
		}
		right, err := c.expr(e.RHS)
		if err != nil {
			return nil, err
		}
		operator, ok := operators[e.Op]
		if !ok {
			return nil, fmt.Errorf("unsupported binary operator at %s", rng)
		}
		return &types.BinaryExpr{Left: left, Operator: operator, Right: right, ExprRange: rng}, nil
	case *hclsyntax.UnaryOpExpr:
		value, err := c.expr(e.Val)
		if err != nil {
			return nil, err
		}
		operator, ok := operators[e.Op]
		if !ok {
			return nil, fmt.Errorf("unsupported unary operator at %s", rng)
		}
		return &types.UnaryExpr{Operator: operator, Expr: value, ExprRange: rng}, nil
	case *hclsyntax.ConditionalExpr:
		results, err := c.exprs([]hclsyntax.Expression{e.Condition, e.TrueResult, e.FalseResult})
		if err != nil {
			return nil, err
		}
		return &types.ConditionalExpr{Condition: results[0], TrueExpr: results[1], FalseExpr: results[2], ExprRange: rng}, nil
	case *hclsyntax.ForExpr:
		collection, err := c.expr(e.CollExpr)
		if err != nil {
			return nil, fmt.Errorf("failed to convert collection: %w", err)
		}
		var thenKey types.Expression
		if e.KeyExpr != nil {
			if thenKey, err = c.expr(e.KeyExpr); err != nil {
				return nil, fmt.Errorf("failed to convert key expression: %w", err)
			}
		}
		thenValue, err := c.expr(e.ValExpr)
		if err != nil {
			return nil, fmt.Errorf("failed to convert value expression: %w", err)
		}
		var condition types.Expression
		if e.CondExpr != nil {
			condition, err = c.expr(e.CondExpr)
			if err != nil {
				return nil, fmt.Errorf("failed to convert condition: %w", err)
			}
		}
		if e.KeyExpr != nil {
			return &types.ForMapExpr{
				KeyVar:        e.KeyVar,
				ValueVar:      e.ValVar,
				Collection:    collection,
				ThenKeyExpr:   thenKey,
				ThenValueExpr: thenValue,
				Condition:     condition,
				Group:         e.Group,
				ExprRange:     rng,
			}, nil
		}
		return &types.ForArrayExpr{
			KeyVar:        e.KeyVar,
			ValueVar:      e.ValVar,
			Collection:    collection,
			ThenValueExpr: thenValue,
			Condition:     condition,
			ExprRange:     rng,
		}, nil
	case *hclsyntax.SplatExpr:
		source, err := c.expr(e.Source)
		if err != nil {
			return nil, fmt.Errorf("failed to convert splat source: %w", err)
		}
		each, err := c.expr(e.Each)
		if err != nil {
			return nil, fmt.Errorf("failed to convert splat each: %w", err)
		}
		return &types.SplatExpr{Source: source, Each: each, ExprRange: rng}, nil
	case *hclsyntax.AnonSymbolExpr:
		return &types.SplatItemExpr{ExprRange: rng}, nil
	case *hclsyntax.IndexExpr:
		collection, err := c.expr(e.Collection)
		if err != nil {
			return nil, err
		}
		key, err := c.expr(e.Key)
		if err != nil {
			return nil, err
		}
		return &types.IndexExpr{Collection: collection, Key: key, ExprRange: rng}, nil
	case *hclsyntax.ParenthesesExpr:
		expression, err := c.expr(e.Expression)
		if err != nil {
			return nil, err
		}
		return &types.ParenExpr{Expression: expression, ExprRange: rng}, nil
	default:
		return nil, fmt.Errorf("unsupported expression type %T at %s", expr, rng)
	}
}
