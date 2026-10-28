package parser

// printExpr renders an AST expression back to HCL source for the fidelity
// gate. It must work from the AST fields alone: never from source text or
// ranges. Keep it in step with the types in parser/types.

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gerdou/terralint/parser/types"
)

func printExpr(e types.Expression) (string, error) {
	switch e := e.(type) {
	case nil:
		return "", fmt.Errorf("nil expression")
	case *types.LiteralValue:
		return printLiteral(e)
	case *types.ReferenceExpr:
		return printParts(e.Parts)
	case *types.RelativeTraversalExpr:
		src, err := printPostfixSource(e.Source)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString(src)
		for _, el := range e.Traversal {
			switch {
			case el.Index != nil:
				k, err := printExpr(el.Index)
				if err != nil {
					return "", err
				}
				b.WriteString("[" + k + "]")
			case el.Type == "attr":
				b.WriteString("." + el.Name)
			default:
				b.WriteString(el.Name)
			}
		}
		return b.String(), nil
	case *types.FunctionCallExpr:
		args, err := printList(e.Args)
		if err != nil {
			return "", err
		}
		return e.Name + "(" + strings.Join(args, ", ") + ")", nil
	case *types.ObjectExpr:
		var b strings.Builder
		b.WriteString("{\n")
		for _, item := range e.Items {
			k, err := printExpr(item.Key)
			if err != nil {
				return "", err
			}
			v, err := printExpr(item.Value)
			if err != nil {
				return "", err
			}
			b.WriteString(k + " = " + v + "\n")
		}
		b.WriteString("}")
		return b.String(), nil
	case *types.ArrayExpr:
		return printTuple(e.Items)
	case *types.TupleExpr:
		return printTuple(e.Expressions)
	case *types.BinaryExpr:
		l, err := printExpr(e.Left)
		if err != nil {
			return "", err
		}
		r, err := printExpr(e.Right)
		if err != nil {
			return "", err
		}
		return l + " " + e.Operator + " " + r, nil
	case *types.UnaryExpr:
		x, err := printExpr(e.Expr)
		if err != nil {
			return "", err
		}
		return e.Operator + x, nil
	case *types.ConditionalExpr:
		parts, err := printList([]types.Expression{e.Condition, e.TrueExpr, e.FalseExpr})
		if err != nil {
			return "", err
		}
		return parts[0] + " ? " + parts[1] + " : " + parts[2], nil
	case *types.ParenExpr:
		x, err := printExpr(e.Expression)
		if err != nil {
			return "", err
		}
		return "(" + x + ")", nil
	case *types.IndexExpr:
		c, err := printPostfixSource(e.Collection)
		if err != nil {
			return "", err
		}
		k, err := printExpr(e.Key)
		if err != nil {
			return "", err
		}
		return c + "[" + k + "]", nil
	case *types.ForArrayExpr:
		head, err := printForHead(e.KeyVar, e.ValueVar, e.Collection)
		if err != nil {
			return "", err
		}
		v, err := printExpr(e.ThenValueExpr)
		if err != nil {
			return "", err
		}
		cond, err := printForCond(e.Condition)
		if err != nil {
			return "", err
		}
		return "[" + head + " : " + v + cond + "]", nil
	case *types.ForMapExpr:
		head, err := printForHead(e.KeyVar, e.ValueVar, e.Collection)
		if err != nil {
			return "", err
		}
		kv, err := printList([]types.Expression{e.ThenKeyExpr, e.ThenValueExpr})
		if err != nil {
			return "", err
		}
		cond, err := printForCond(e.Condition)
		if err != nil {
			return "", err
		}
		return "{" + head + " : " + kv[0] + " => " + kv[1] + cond + "}", nil
	case *types.SplatExpr:
		src, err := printPostfixSource(e.Source)
		if err != nil {
			return "", err
		}
		if e.Each != nil {
			return "", fmt.Errorf("splat with an Each expression %T", e.Each)
		}
		return src + "[*]", nil
	case *types.TemplateExpr:
		body, err := printTemplateBody(e.Parts)
		if err != nil {
			return "", err
		}
		return `"` + body + `"`, nil
	case *types.HeredocExpr:
		return `"` + escapeTemplateLiteral(e.Content) + `"`, nil
	}
	return "", fmt.Errorf("cannot print %T", e)
}

func printList(xs []types.Expression) ([]string, error) {
	out := make([]string, len(xs))
	for i, x := range xs {
		s, err := printExpr(x)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func printTuple(xs []types.Expression) (string, error) {
	items, err := printList(xs)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "[]", nil
	}
	return "[\n" + strings.Join(items, ",\n") + ",\n]", nil
}

// A splat must be parenthesised when something is applied to its result,
// or the parser would fold that step into the splat.
func printPostfixSource(e types.Expression) (string, error) {
	s, err := printExpr(e)
	if _, ok := e.(*types.SplatExpr); ok {
		s = "(" + s + ")"
	}
	return s, err
}

func printParts(parts []string) (string, error) {
	if len(parts) == 0 || parts[0] == "" {
		return "", fmt.Errorf("reference without a root: %q", parts)
	}
	var b strings.Builder
	for i, p := range parts {
		switch {
		case p == "":
			return "", fmt.Errorf("empty reference part in %q", parts)
		case strings.HasPrefix(p, "["):
			b.WriteString(p)
		case i == 0:
			b.WriteString(p)
		default:
			b.WriteString("." + p)
		}
	}
	return b.String(), nil
}

func printForHead(keyVar, valueVar string, coll types.Expression) (string, error) {
	c, err := printExpr(coll)
	if err != nil {
		return "", err
	}
	vars := valueVar
	if keyVar != "" {
		vars = keyVar + ", " + valueVar
	}
	return "for " + vars + " in " + c, nil
}

func printForCond(cond types.Expression) (string, error) {
	if cond == nil {
		return "", nil
	}
	c, err := printExpr(cond)
	return " if " + c, err
}

func printLiteral(l *types.LiteralValue) (string, error) {
	switch v := l.Value.(type) {
	case nil:
		return "null", nil
	case string:
		if l.ValueType == "dynamic" || l.ValueType == "null" {
			return "null", nil
		}
		if l.ValueType == "number" {
			return v, nil
		}
		return `"` + escapeTemplateLiteral(v) + `"`, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case *big.Float:
		return v.Text('g', -1), nil
	case *big.Int:
		return v.String(), nil
	}
	return "", fmt.Errorf("literal of Go type %T", l.Value)
}

func printTemplateBody(parts []types.Expression) (string, error) {
	var b strings.Builder
	for _, p := range parts {
		switch p := p.(type) {
		case *types.LiteralValue:
			if s, ok := p.Value.(string); ok && p.ValueType == "string" {
				b.WriteString(escapeTemplateLiteral(s))
				continue
			}
		case *types.TemplateExpr:
			// A template part that is a template around a single for
			// expression is a %{for} directive.
			if len(p.Parts) == 1 {
				if f, ok := p.Parts[0].(*types.ForArrayExpr); ok {
					head, err := printForHead(f.KeyVar, f.ValueVar, f.Collection)
					if err != nil {
						return "", err
					}
					inner, err := printTemplateInner(f.ThenValueExpr)
					if err != nil {
						return "", err
					}
					b.WriteString("%{" + head + "}" + inner + "%{endfor}")
					continue
				}
			}
		case *types.TemplateForDirective:
			head, err := printForHead(p.KeyVar, p.ValueVar, p.CollExpr)
			if err != nil {
				return "", err
			}
			inner, err := printTemplateBody(p.Content)
			if err != nil {
				return "", err
			}
			b.WriteString("%{" + head + "}" + inner + "%{endfor}")
			continue
		case *types.TemplateIfDirective:
			c, err := printExpr(p.Condition)
			if err != nil {
				return "", err
			}
			t, err := printTemplateBody(p.TrueExpr)
			if err != nil {
				return "", err
			}
			b.WriteString("%{if " + c + "}" + t)
			if len(p.FalseExpr) > 0 {
				f, err := printTemplateBody(p.FalseExpr)
				if err != nil {
					return "", err
				}
				b.WriteString("%{else}" + f)
			}
			b.WriteString("%{endif}")
			continue
		}
		s, err := printExpr(p)
		if err != nil {
			return "", err
		}
		b.WriteString("${" + s + "}")
	}
	return b.String(), nil
}

// printTemplateInner prints the body of a %{for} directive.
func printTemplateInner(e types.Expression) (string, error) {
	switch e := e.(type) {
	case *types.TemplateExpr:
		return printTemplateBody(e.Parts)
	case *types.LiteralValue:
		if s, ok := e.Value.(string); ok && e.ValueType == "string" {
			return escapeTemplateLiteral(s), nil
		}
	}
	s, err := printExpr(e)
	return "${" + s + "}", err
}

func escapeTemplateLiteral(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "${") || strings.HasPrefix(s[i:], "%{") {
			b.WriteString(s[i:i+1] + s[i:i+2])
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}
