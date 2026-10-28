package parser

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"

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

	// Lex once: comments are looked up by line, heredocs by start offset.
	c := &converter{content: content, heredocs: map[int]hclsyntax.Token{}}
	tokens, _ := hclsyntax.LexConfig(content, filePath, hcl.InitialPos)
	for _, tok := range tokens {
		switch tok.Type {
		case hclsyntax.TokenComment:
			c.comments = append(c.comments, tok)
		case hclsyntax.TokenOHeredoc:
			c.heredocs[tok.Range.Start.Byte] = tok
		}
	}

	root := &types.Root{Children: make([]types.Body, 0)}
	body := file.Body.(*hclsyntax.Body)

	// A file with only comments becomes one typeless block holding them.
	if len(body.Attributes) == 0 && len(body.Blocks) == 0 {
		if len(c.comments) > 0 {
			root.Children = append(root.Children, &types.Block{BlockComment: c.leading(0, len(content)+1)})
		}
		return root, nil
	}

	root.Children, err = c.body(body, 0, len(content))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	return root, nil
}

type converter struct {
	content  []byte
	comments []hclsyntax.Token       // in source order
	heredocs map[int]hclsyntax.Token // heredoc openers by start byte
}

func stripCommentPrefix(comment string) string {
	// Remove trailing newlines first
	comment = strings.TrimRight(comment, "\n")

	// Strip comment prefix
	if strings.HasPrefix(comment, "//") {
		return strings.TrimSpace(comment[2:])
	} else if strings.HasPrefix(comment, "#") {
		return strings.TrimSpace(comment[1:])
	}
	return strings.TrimSpace(comment)
}

// commentsFrom returns the comments that start on line or later.
func (c *converter) commentsFrom(line int) []hclsyntax.Token {
	i := sort.Search(len(c.comments), func(i int) bool { return c.comments[i].Range.Start.Line >= line })
	return c.comments[i:]
}

// leading joins the comments that start after afterLine and before beforeLine.
func (c *converter) leading(afterLine, beforeLine int) string {
	var comments []string
	for _, tok := range c.commentsFrom(afterLine + 1) {
		if tok.Range.Start.Line >= beforeLine {
			break
		}
		comments = append(comments, stripCommentPrefix(string(tok.Bytes)))
	}
	return strings.Join(comments, "\n")
}

// inline returns the first comment on start's line that starts after start
// and before the byte offset limit.
func (c *converter) inline(start hcl.Pos, limit int) string {
	for _, tok := range c.commentsFrom(start.Line) {
		if tok.Range.Start.Line > start.Line || tok.Range.Start.Byte >= limit {
			break
		}
		if tok.Range.Start.Byte > start.Byte {
			return stripCommentPrefix(string(tok.Bytes))
		}
	}
	return ""
}

// body converts the attributes and blocks of a body in source order.
// openLine is the line the body opens on (0 for a file) and endByte the
// offset where it ends.
func (c *converter) body(body *hclsyntax.Body, openLine, endByte int) ([]types.Body, error) {
	var items []hclsyntax.Node
	for _, attr := range body.Attributes {
		items = append(items, attr)
	}
	for _, block := range body.Blocks {
		items = append(items, block)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Range().Start.Byte < items[j].Range().Start.Byte })

	children := make([]types.Body, 0, len(items))
	prevEndLine := openLine
	for i, item := range items {
		limit := endByte
		if i+1 < len(items) {
			limit = items[i+1].Range().Start.Byte
		}
		start := item.Range().Start
		blockComment, inlineComment := c.leading(prevEndLine, start.Line), c.inline(start, limit)

		switch item := item.(type) {
		case *hclsyntax.Attribute:
			value, err := c.expr(item.Expr)
			if err != nil {
				return nil, fmt.Errorf("failed to convert attribute %s (line %d): %w", item.Name, start.Line, err)
			}
			children = append(children, &types.Attribute{
				Name:          item.Name,
				Value:         value,
				Range:         item.SrcRange,
				InlineComment: inlineComment,
				BlockComment:  blockComment,
			})
		case *hclsyntax.Block:
			nested, err := c.body(item.Body, item.OpenBraceRange.Start.Line, item.CloseBraceRange.Start.Byte)
			if err != nil {
				return nil, fmt.Errorf("failed to convert block %s (line %d): %w", item.Type, start.Line, err)
			}
			children = append(children, &types.Block{
				Type:          item.Type,
				Labels:        item.Labels,
				Range:         item.Range(),
				Children:      nested,
				InlineComment: inlineComment,
				BlockComment:  blockComment,
			})
		}
		prevEndLine = item.Range().End.Line
	}
	return children, nil
}

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

func (c *converter) expr(expr hclsyntax.Expression) (types.Expression, error) {
	rng := expr.Range()
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		return literal(e.Val, rng), nil
	case *hclsyntax.TemplateExpr:
		parts, err := c.exprs(e.Parts)
		if err != nil {
			return nil, err
		}
		if open, ok := c.heredocs[rng.Start.Byte]; ok {
			return c.heredoc(open, parts, rng), nil
		}
		if len(parts) == 1 {
			if lit, ok := parts[0].(*types.LiteralValue); ok {
				lit.ExprRange = rng
				return lit, nil
			}
		}
		return &types.TemplateExpr{Parts: parts, ExprRange: rng}, nil
	case *hclsyntax.TemplateWrapExpr:
		return c.expr(e.Wrapped)
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
			key, err := c.expr(item.KeyExpr)
			if err != nil {
				return nil, err
			}
			value, err := c.expr(item.ValueExpr)
			if err != nil {
				return nil, err
			}
			limit := rng.End.Byte
			if i+1 < len(e.Items) {
				limit = e.Items[i+1].KeyExpr.Range().Start.Byte
			}
			start := item.KeyExpr.Range().Start
			items[i] = types.ObjectItem{
				Key:           key,
				Value:         value,
				InlineComment: c.inline(start, limit),
				BlockComment:  c.leading(prevEndLine, start.Line),
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
			thenKey, err := c.expr(e.KeyExpr)
			if err != nil {
				return nil, fmt.Errorf("failed to convert key expression: %w", err)
			}
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
