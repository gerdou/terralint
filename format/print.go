package format

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/parser"
	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// printer prints the planned AST. It writes no indentation and leaves the
// spacing inside lines to hclwrite.Format, but it spaces tokens the way
// Format does, so that the width of a one-line list is known here.
type printer struct {
	cfg  *config.Config
	text map[types.Expression][]types.TextPiece
	off  offSet // the rules off for the item being printed
	// hoist receives the comments of the item being printed that have no
	// line break to stay at: the printer puts them above the item.
	hoist *[]string
	// inlined counts the expressions printed with their comments in place,
	// so that a call or list can tell whether an argument holds any.
	inlined int
	// endTail is the last tail comment printed in place, with the space
	// before it, for arg; endOnly says it was the only comment of its
	// expression.
	endTail string
	endOnly bool
	// inTemplate is set while printing the expressions of a template's
	// interpolations and directives.
	inTemplate bool
	// texts holds what expr printed for each expression, and force the
	// calls, parentheses and for expressions that must break after their
	// opening bracket (see settle).
	texts map[types.Expression]string
	force map[types.Expression]bool
	// items holds the object items printed, which print the same each time
	// settle prints their item again.
	items map[*types.ObjectItem]printedItem
}

type printedItem struct {
	text      string
	commented bool
	inlined   int
}

func newPrinter(cfg *config.Config, text map[types.Expression][]types.TextPiece) *printer {
	return &printer{cfg: cfg, text: text, texts: map[types.Expression]string{}, force: map[types.Expression]bool{}, items: map[*types.ObjectItem]printedItem{}}
}

func (p *printer) on(rule string) bool { return p.cfg.On(rule) && !p.off[rule] }

// ---- Comments ----

// comments returns the output lines for stored comment text (see
// types.Root): # comments as written, // comments as # comments, and, with
// the comments rule on, /* */ comments as # comments, one per line, a blank
// line becoming #; with the rule off, /* */ comments as written.
func (p *printer) comments(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	standard := p.on(config.RuleComments)
	var block []string // the lines of an unfinished /* */ comment
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimRightFunc(line, unicode.IsSpace)
		trimmed := strings.TrimSpace(t)
		if block == nil && !strings.HasPrefix(t, "/*") {
			switch {
			case strings.HasPrefix(t, "#"): // held whole: as written
				out = append(out, t)
			case strings.HasPrefix(t, "//"): // held whole: # for //
				out = append(out, strings.TrimSpace("# "+strings.TrimPrefix(t[2:], " ")))
			case t == "":
				out = append(out, "#")
			default:
				out = append(out, "# "+t)
			}
			continue
		}
		first := block == nil
		block = append(block, t)
		closed := strings.HasSuffix(trimmed, "*/") && !(first && trimmed == "/*")
		if standard {
			body := trimmed
			if first {
				body = strings.TrimPrefix(body, "/*")
			}
			if closed {
				body = strings.TrimSuffix(body, "*/")
			}
			body = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(body), "*"))
			switch {
			case body != "":
				out = append(out, "# "+body)
			case !first && !closed:
				out = append(out, "#")
			}
		}
		if closed {
			if !standard {
				out = append(out, strings.Join(block, "\n"))
			}
			block = nil
		}
	}
	if block != nil && !standard {
		// Text that only looked like the start of a /* */ comment: printed
		// raw, it would comment out what follows.
		for _, line := range block {
			out = append(out, strings.TrimSpace("# "+line))
		}
	}
	return out
}

var heredocOpener = regexp.MustCompile(`<<-?[A-Za-z_][A-Za-z0-9_-]*$`)

// place lays out an item: the comment lines above it, its text, and its
// inline and trailing comments at the end of its first and last lines
// where they fit (else above it).
func place(above []string, text string, inline, trail []string) string {
	ls := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	closed := strings.HasSuffix(text, "\n") // ends in a heredoc's closing line
	if len(ls) == 1 && !closed {
		ends := append(append([]string{}, inline...), trail...)
		if len(ends) > 0 && one(ends[:1]) {
			ls[0] += " " + ends[0]
			ends = ends[1:]
		}
		above = append(above, ends...)
	} else {
		if one(inline) && !heredocOpener.MatchString(ls[0]) {
			ls[0] += " " + inline[0]
		} else {
			above = append(above, inline...)
		}
		if one(trail) && !closed {
			ls[len(ls)-1] += " " + trail[0]
		} else {
			above = append(above, trail...)
		}
	}
	return strings.Join(append(above, ls...), "\n")
}

// one reports whether comment lines c are a single line.
func one(c []string) bool { return len(c) == 1 && !strings.Contains(c[0], "\n") }

// settle prints an item with print, which returns its text, and decides
// where its inline comment goes. It ends the item's first line only where
// parsing the output again gives it back to the item (D13): not after an
// argument of a call has started on that line, nor after the { of an
// object that is not the item's whole value. The value is printed again
// with the outermost such call one argument per line, or with the
// parentheses or for expression around such an object broken after their
// opening bracket; when that does not help, the comment goes with the
// trailing ones, after the value's end, where it is the item's too.
func (p *printer) settle(value types.Expression, print func() string, inline, trail []string) (string, []string, []string) {
	text := print()
	if !one(inline) || !strings.Contains(strings.TrimSuffix(text, "\n"), "\n") || owns(text) {
		return text, inline, trail
	}
	if x := p.opener(value, true, nil); x != nil {
		p.force[x] = true
		forced := print()
		delete(p.force, x)
		if owns(forced) {
			return forced, inline, trail
		}
	}
	// The forced print hoisted the same comments as the first.
	return text, nil, append(inline, trail...)
}

// owns reports whether a comment at the end of the first line of text, an
// attribute or object item as printed, is the item's own when parsed.
func owns(text string) bool {
	first, rest, _ := strings.Cut(text, "\n")
	root, err := parser.Parse([]byte("x = {\n"+first+" # c\n"+rest+"\n}\n"), "item.tf")
	if err != nil || len(root.Children) != 1 {
		return false
	}
	a, ok := root.Children[0].(*types.Attribute)
	if !ok {
		return false
	}
	o, ok := a.Value.(*types.ObjectExpr)
	return ok && len(o.Items) == 1 && o.Items[0].InlineComment == "c" && !o.Items[0].InlineInside
}

// opener returns what to break after its opening bracket so that the first
// line of e's text (as printed last) no longer ends inside a part of the
// value: the outermost call whose arguments start on that line, or, when
// an object that is not the item's value (value) opens there first, the
// outermost parentheses or for expression around it (around). nil when
// there is none.
func (p *printer) opener(e types.Expression, value bool, around types.Expression) types.Expression {
	s := p.texts[e]
	if !strings.Contains(s, "\n") {
		return nil
	}
	switch x := e.(type) {
	case *types.FunctionCallExpr:
		if strings.HasPrefix(s, x.Name+"(\n") {
			return nil
		}
		return x
	case *types.ObjectExpr:
		if value {
			return nil
		}
		return around
	case *types.ParenExpr, *types.ForArrayExpr, *types.ForMapExpr:
		if s[1] == '\n' { // broken after its bracket already
			return nil
		}
		if around == nil {
			around = e
		}
	case *types.ArrayExpr, *types.TemplateExpr, *types.HeredocExpr, *types.LiteralValue:
		return nil
	}
	for _, x := range parts(e) {
		if x != nil && strings.Contains(p.texts[x], "\n") {
			return p.opener(x, false, around)
		}
	}
	return nil
}

// ---- Bodies ----

func (p *printer) file(f *formatter, top *plan) []byte {
	p.off = offSet{}
	var parts []string
	if header := p.comments(strings.Join(f.header, "\n")); len(header) > 0 {
		parts = append(parts, strings.Join(header, "\n"))
	}
	if body := p.body(f, top); body != "" {
		parts = append(parts, body)
	}
	if len(parts) == 0 {
		return nil
	}
	return []byte(strings.Join(parts, "\n\n") + "\n")
}

// chunk is a printed unit of a body.
type chunk struct {
	text  string
	multi bool   // the item itself spans lines, or it is a section
	entry *entry // nil for a section
}

func (p *printer) body(f *formatter, pl *plan) string {
	var chunks []chunk
	for _, u := range pl.units() {
		if u.entry != nil {
			chunks = append(chunks, p.entry(f, pl, u.entry))
			continue
		}
		var inner []chunk
		for _, e := range u.sec.entries {
			inner = append(inner, p.entry(f, pl, e))
		}
		save := p.off
		p.off = u.sec.off
		var ls []string
		if head := p.comments(strings.Join(u.sec.head, "\n")); len(head) > 0 {
			ls = append(ls, strings.Join(head, "\n"))
		}
		if body := p.join(f, pl, inner); body != "" {
			ls = append(ls, body)
		}
		if tail := p.comments(strings.Join(u.sec.tail, "\n")); len(tail) > 0 {
			ls = append(ls, strings.Join(tail, "\n"))
		}
		p.off = save
		chunks = append(chunks, chunk{text: strings.Join(ls, "\n"), multi: true})
	}
	out := p.join(f, pl, chunks)
	save := p.off
	p.off = pl.off
	end := p.comments(strings.Join(pl.end, "\n"))
	p.off = save
	if len(end) > 0 {
		sep := "\n"
		if pl.top {
			sep += strings.Repeat("\n", p.cfg.BlankLinesBeforeFileEndComments)
		}
		if out == "" {
			sep = ""
		}
		out += sep + strings.Join(end, "\n")
	}
	return out
}

// join puts chunks one after another, with the blank lines between them
// that the configuration asks for.
func (p *printer) join(f *formatter, pl *plan, chunks []chunk) string {
	var b strings.Builder
	for i, c := range chunks {
		if c.text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n" + strings.Repeat("\n", p.blank(f, pl, chunks[i-1], c)))
		}
		b.WriteString(c.text)
	}
	return b.String()
}

// group is the priority group an item belongs to.
type group struct {
	id            string
	before, after int
}

func (f *formatter) group(pl *plan, n types.Body) group {
	p := f.priority(pl)
	name := ""
	if b, ok := n.(*types.Block); ok {
		name = b.Type
		if o, _, ok := locate(name, p.PrependedBlocks); ok {
			return group{fmt.Sprint("b", o), p.PrependedBlocks[o].BlankLinesBefore, p.PrependedBlocks[o].BlankLinesAfter}
		}
	} else {
		name = n.(*types.Attribute).Name
	}
	if o, _, ok := locate(name, p.Prepended); ok {
		return group{fmt.Sprint("p", o), p.Prepended[o].BlankLinesBefore, p.Prepended[o].BlankLinesAfter}
	}
	if o, _, ok := locate(name, p.Appended); ok {
		return group{fmt.Sprint("a", o), p.Appended[o].BlankLinesBefore, p.Appended[o].BlankLinesAfter}
	}
	return group{}
}

// blank returns the number of blank lines between two chunks of a body.
func (p *printer) blank(f *formatter, pl *plan, a, b chunk) int {
	n := 0
	isBlock := func(c chunk) bool {
		if c.entry == nil {
			return false
		}
		_, ok := c.entry.node.(*types.Block)
		return ok
	}
	if pl.top && (a.entry == nil || b.entry == nil || isBlock(a) || isBlock(b)) {
		n = max(n, p.cfg.BlankLinesBetweenTopLevelBlocks)
	}
	if a.multi || b.multi {
		n = max(n, p.cfg.BlankLinesAroundMultiLine)
	}
	if a.entry != nil && b.entry != nil {
		ga, gb := f.group(pl, a.entry.node), f.group(pl, b.entry.node)
		if ga.id != gb.id {
			n = max(n, ga.after, gb.before)
		}
	}
	return n
}

// entry prints a body item with its comments.
func (p *printer) entry(f *formatter, pl *plan, e *entry) chunk {
	saveOff, saveHoist := p.off, p.hoist
	defer func() { p.off, p.hoist = saveOff, saveHoist }()
	p.off = e.off
	above := p.comments(strings.Join(e.lead, "\n"))
	var hoisted []string
	p.hoist = &hoisted

	var text string
	var inline, trail []string
	switch n := e.node.(type) {
	case *types.Attribute:
		root := n.Value
		if t, ok := n.Value.(*types.TemplateExpr); ok && len(p.text[t]) == 1 && p.text[t][0].Kind == "interp" {
			root = p.text[t][0].Expr // value prints it alone
		}
		print := func() string {
			hoisted = hoisted[:0]
			return n.Name + " = " + p.value(n, pl)
		}
		text, inline, trail = p.settle(root, print, p.inline(n.InlineComment, n.InlineInside), p.comments(n.TrailComment))
	case *types.Block:
		if n.Type == "" { // a file of comments only
			return chunk{text: strings.Join(above, "\n"), entry: e}
		}
		head := n.Type
		for _, l := range n.Labels {
			head += " " + quote(l)
		}
		inner := p.body(f, e.plan)
		p.off = e.off
		if inner == "" {
			text = head + " {}"
		} else {
			text = head + " {\n" + inner + "\n}"
		}
		inline, trail = p.inline(n.InlineComment, n.InlineInside), p.comments(n.TrailComment)
	}
	return chunk{
		text:  place(append(above, hoisted...), text, inline, trail),
		multi: strings.Contains(strings.TrimSuffix(text, "\n"), "\n"),
		entry: e,
	}
}

// inline returns the lines of an item's inline comment, unless an item
// inside it holds the comment too and prints it.
func (p *printer) inline(text string, inside bool) []string {
	if inside {
		return nil
	}
	return p.comments(text)
}

// value prints an attribute's value, with two rewrites `terraform fmt`
// makes: a value that is only "${x}" becomes x, and a top-level variable's
// type in the pre-0.12 forms ("string", list) becomes its current form.
func (p *printer) value(a *types.Attribute, pl *plan) string {
	if pl.blockType == "variable" && pl.depth == 1 && a.Name == "type" {
		if s, ok := p.legacyType(a.Value); ok {
			return s
		}
	}
	if t, ok := a.Value.(*types.TemplateExpr); ok {
		if pieces := p.text[t]; len(pieces) == 1 && pieces[0].Kind == "interp" {
			n := p.inlined
			s := p.sub(pieces[0].Expr, false)
			if strings.Contains(s, "\n") && !(strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")) {
				s = p.wrap(n, "(", s, ")") // as the parentheses print on the next run
			}
			return p.withComments(a.Value, s, false)
		}
	}
	return p.sub(a.Value, false)
}

func (p *printer) legacyType(e types.Expression) (string, bool) {
	switch e := e.(type) {
	case *types.LiteralValue:
		if pieces := p.text[e]; e.ValueType == "string" && len(pieces) == 1 && pieces[0].Kind == "text" {
			switch pieces[0].Text {
			case "string":
				return p.withComments(e, "string", false), true
			case "list", "map":
				return p.withComments(e, pieces[0].Text+"(string)", false), true
			}
		}
	case *types.ReferenceExpr:
		if len(e.Parts) == 1 {
			switch e.Parts[0] {
			case "list", "map", "set":
				return p.withComments(e, e.Parts[0]+"(any)", false), true
			}
		}
	}
	return "", false
}

func quote(s string) string {
	return string(hclwrite.TokensForValue(cty.StringVal(s)).Bytes())
}

// ---- Expressions ----

// sub prints e with its own comments: in place when nl says line breaks
// are allowed here, else above the item.
func (p *printer) sub(e types.Expression, nl bool) string {
	return p.withComments(e, p.expr(e, nl), nl)
}

func exprComments(e types.Expression) *types.ExprComments {
	if c, ok := e.(interface{ Comments() *types.ExprComments }); ok {
		return c.Comments()
	}
	return &types.ExprComments{}
}

// withComments puts e's comments around s, its text. Where line breaks are
// allowed, a tail comment ends the line s ends on, so the next token starts
// a new line; the others go on lines of their own above s. Otherwise they
// go above the item.
func (p *printer) withComments(e types.Expression, s string, nl bool) string {
	c := exprComments(e)
	if c.LeadComment == "" && c.TailComment == "" {
		return s
	}
	lead, tail := p.comments(c.LeadComment), p.comments(c.TailComment)
	if !nl {
		if p.hoist != nil {
			*p.hoist = append(*p.hoist, append(lead, tail...)...)
		}
		return s
	}
	p.inlined++
	if len(tail) > 0 && !strings.HasSuffix(s, "\n") && !strings.Contains(tail[0], "\n") {
		p.endTail = " " + tail[0]
		s += p.endTail + "\n"
		tail = tail[1:]
		p.endOnly = len(lead) == 0 && len(tail) == 0
	}
	if above := append(lead, tail...); len(above) > 0 {
		s = "\n" + strings.Join(above, "\n") + "\n" + strings.TrimPrefix(s, "\n")
	}
	return s
}

// expr prints e without its own comments. nl tells whether a line break is
// allowed around e (inside brackets) or not (in an item's value).
func (p *printer) expr(e types.Expression, nl bool) string {
	s := p.layout(e, nl)
	p.texts[e] = s
	return s
}

// since returns p.inlined, less one when settle asks e to break after its
// opening bracket, so that wrap breaks it as if a comment were inside.
func (p *printer) since(e types.Expression) int {
	if p.force[e] {
		return p.inlined - 1
	}
	return p.inlined
}

func (p *printer) layout(e types.Expression, nl bool) string {
	switch e := e.(type) {
	case *types.LiteralValue:
		return p.literal(e)
	case *types.ReferenceExpr:
		var b strings.Builder
		for i, part := range e.Parts {
			if i > 0 && !strings.HasPrefix(part, "[") {
				b.WriteString(".")
			}
			b.WriteString(part)
		}
		return b.String()
	case *types.RelativeTraversalExpr:
		var b strings.Builder
		b.WriteString(p.postfixSource(e.Source, nl))
		for _, step := range e.Traversal {
			if step.Type == "attr" {
				b.WriteString("." + step.Name)
			} else {
				b.WriteString("[" + p.expr(step.Index, false) + "]")
			}
		}
		return b.String()
	case *types.SplatExpr:
		return p.postfixSource(e.Source, nl) + "[*]" + p.expr(e.Each, nl)
	case *types.SplatItemExpr:
		return ""
	case *types.IndexExpr:
		return p.postfixSource(e.Collection, nl) + "[" + p.sub(e.Key, false) + "]"
	case *types.ParenExpr:
		n := p.since(e)
		return p.wrap(n, "(", p.sub(e.Expression, true), ")")
	case *types.UnaryExpr:
		return e.Operator + p.sub(e.Expr, nl)
	case *types.BinaryExpr:
		return p.sub(e.Left, nl) + " " + e.Operator + " " + p.sub(e.Right, nl)
	case *types.ConditionalExpr:
		return p.sub(e.Condition, nl) + " ? " + p.sub(e.TrueExpr, nl) + " : " + p.sub(e.FalseExpr, nl)
	case *types.ForArrayExpr:
		n := p.since(e)
		s := "for " + forVars(e.KeyVar, e.ValueVar) + " in " + p.sub(e.Collection, true) + " : " + p.sub(e.ThenValueExpr, true)
		if e.Condition != nil {
			s += " if " + p.sub(e.Condition, true)
		}
		return p.wrap(n, "[", s, "]")
	case *types.ForMapExpr:
		n := p.since(e)
		s := "for " + forVars(e.KeyVar, e.ValueVar) + " in " + p.sub(e.Collection, true) + " : " + p.sub(e.ThenKeyExpr, true) + " => " + p.sub(e.ThenValueExpr, true)
		if e.Group {
			s += "..."
		}
		if e.Condition != nil {
			s += " if " + p.sub(e.Condition, true)
		}
		if p.inlined == n {
			return "{ " + s + " }"
		}
		return p.wrap(n, "{", s, "}")
	case *types.FunctionCallExpr:
		return p.call(e)
	case *types.ArrayExpr:
		return p.list(e)
	case *types.ObjectExpr:
		return p.object(e)
	case *types.TemplateExpr, *types.HeredocExpr:
		if s, ok := p.template(e); ok {
			return s
		}
		if t, ok := e.(*types.TemplateExpr); ok {
			return `"` + p.templateParts(t.Parts) + `"`
		}
		h := e.(*types.HeredocExpr)
		return `"` + p.templateParts(h.Parts) + `"`
	}
	panic(fmt.Sprintf("format: cannot print %T", e))
}

// wrap returns s between open and close. When comments were printed in
// place in s (p.inlined moved past n), s goes on lines of its own: a comment
// that ended the line open is on would read as its item's own on the next
// run.
func (p *printer) wrap(n int, open, s, close string) string {
	if p.inlined == n {
		return open + s + close
	}
	return open + "\n" + strings.Trim(s, "\n") + "\n" + close
}

func forVars(key, value string) string {
	if key != "" {
		return key + ", " + value
	}
	return value
}

// postfixSource prints what a traversal, splat or index applies to; a splat
// needs parentheses there, or the step would join the splat.
func (p *printer) postfixSource(e types.Expression, nl bool) string {
	n := p.inlined
	s := p.sub(e, nl)
	if _, ok := e.(*types.SplatExpr); ok {
		s = p.wrap(n, "(", s, ")") // as the parentheses print on the next run
	}
	return s
}

func (p *printer) literal(l *types.LiteralValue) string {
	if pieces, ok := p.text[l]; ok {
		if l.ValueType == "number" {
			return pieces[0].Text
		}
		s, _ := p.template(l)
		return s
	}
	switch v := l.Value.(type) {
	case nil:
		return "null"
	case string:
		return quote(v)
	case bool:
		return strconv.FormatBool(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	return fmt.Sprint(l.Value)
}

// template prints a quoted template or heredoc as written, rebuilding only
// the expressions in its interpolations and directives.
func (p *printer) template(e types.Expression) (string, bool) {
	pieces, ok := p.text[e]
	if !ok {
		return "", false
	}
	tilde := func(on bool) string {
		if on {
			return "~"
		}
		return ""
	}
	save := p.inTemplate
	p.inTemplate = true
	defer func() { p.inTemplate = save }()
	var b strings.Builder
	h, heredoc := e.(*types.HeredocExpr)
	if heredoc {
		b.WriteString("<<")
		if h.Indented {
			b.WriteString("-")
		}
		b.WriteString(h.Marker + "\n")
	} else {
		b.WriteString(`"`)
	}
	for _, pc := range pieces {
		open, close := "${"+tilde(pc.StripLeft), tilde(pc.StripRight)+"}"
		if pc.Kind != "interp" {
			open = "%{" + tilde(pc.StripLeft)
		}
		switch pc.Kind {
		case "text":
			b.WriteString(pc.Text)
		case "close":
			b.WriteString(pc.Text + "\n")
		case "interp":
			n := p.inlined
			b.WriteString(p.wrap(n, open, p.sub(pc.Expr, true), close))
		case "if":
			n := p.inlined
			b.WriteString(p.wrap(n, open+"if", " "+p.sub(pc.Expr, true), close))
		case "for":
			n := p.inlined
			b.WriteString(p.wrap(n, open+"for "+forVars(pc.For.KeyVar, pc.For.ValueVar)+" in", " "+p.sub(pc.For.Collection, true), close))
		default: // else, endif, endfor
			b.WriteString(open + pc.Kind + close)
		}
	}
	if !heredoc {
		b.WriteString(`"`)
	}
	return b.String(), true
}

// templateParts prints template parts that have no recorded text, as
// HCL's parse sees them.
func (p *printer) templateParts(parts []types.Expression) string {
	save := p.inTemplate
	p.inTemplate = true
	defer func() { p.inTemplate = save }()
	var b strings.Builder
	for _, part := range parts {
		if l, ok := part.(*types.LiteralValue); ok && l.ValueType == "string" {
			if s, ok := l.Value.(string); ok {
				q := quote(s)
				b.WriteString(q[1 : len(q)-1])
				continue
			}
		}
		n := p.inlined
		b.WriteString(p.wrap(n, "${", p.sub(part, true), "}"))
	}
	return b.String()
}

// list prints a list on one line when it has no comments (its elements'
// included), no element that spans lines, and fits the configured width
// (any width inside a template interpolation); else one element per line.
func (p *printer) list(a *types.ArrayExpr) string {
	elems, ends := make([]string, len(a.Items)), make([]string, len(a.Items))
	open := p.comments(a.OpenComment)
	commented, multi := a.EndComment != "" || len(open) > 0, false
	for i, x := range a.Items {
		n := p.inlined
		elems[i], ends[i] = p.arg(x)
		c := exprComments(x)
		commented = commented || c.LeadComment != "" || c.TailComment != "" || ends[i] != "" || p.inlined > n
		multi = multi || strings.Contains(elems[i], "\n")
	}
	one := "[" + strings.Join(elems, ", ") + "]"
	fits := len(one) <= p.cfg.ListWidth
	switch {
	case p.inTemplate: // terraform fmt would dedent the lines after it
		fits = true
	case !p.on(config.RuleLists):
		fits = a.ExprRange.Start.Line == a.ExprRange.End.Line
	}
	if !commented && !multi && fits {
		return one
	}
	var b strings.Builder
	b.WriteString("[" + p.open(open) + "\n")
	for i, x := range a.Items {
		b.WriteString(p.element(x, elems[i], ends[i], ","))
	}
	if end := p.comments(a.EndComment); len(end) > 0 {
		b.WriteString(strings.Join(end, "\n") + "\n")
	}
	b.WriteString("]")
	return b.String()
}

// arg prints a list element or call argument without its own comments. It
// splits off the comment printed in place that ends it, if any: that
// comment goes after the separator, where it reads as the element's own on
// the next run.
func (p *printer) arg(x types.Expression) (s, end string) {
	p.endTail = ""
	s = p.expr(x, true)
	if p.endTail != "" && strings.HasSuffix(s, p.endTail+"\n") {
		if p.endOnly {
			// It prints as the element's own tail comment, which parses
			// back as one; those do not count as printed in place either.
			p.inlined--
		}
		return strings.TrimSuffix(s, p.endTail+"\n"), p.endTail[1:]
	}
	return s, ""
}

// element prints a list element or argument on its own lines, with its
// comments and end, the comment arg split off, followed by sep.
func (p *printer) element(x types.Expression, s, end, sep string) string {
	c := exprComments(x)
	above := p.comments(c.LeadComment)
	tail := p.comments(c.TailComment)
	if end != "" {
		tail = append([]string{end}, tail...)
	}
	line := strings.TrimPrefix(s, "\n") + sep // after a heredoc, sep starts the line below its closing marker
	if len(tail) == 1 && !strings.Contains(tail[0], "\n") && !strings.HasSuffix(line, "\n") {
		line += " " + tail[0]
	} else {
		above = append(above, tail...)
	}
	out := line
	if len(above) > 0 {
		out = strings.Join(above, "\n") + "\n" + line
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// call prints a function call: arguments on one line, unless the call or
// an argument holds comments or one before the last spans lines; then one
// per line.
func (p *printer) call(f *types.FunctionCallExpr) string {
	args, ends := make([]string, len(f.Args)), make([]string, len(f.Args))
	open := p.comments(f.OpenComment)
	commented, multi := f.EndComment != "" || len(open) > 0, false
	for i, x := range f.Args {
		n := p.inlined
		args[i], ends[i] = p.arg(x)
		c := exprComments(x)
		commented = commented || c.LeadComment != "" || c.TailComment != "" || ends[i] != "" || p.inlined > n
		multi = multi || i < len(f.Args)-1 && strings.Contains(args[i], "\n")
	}
	expand := ""
	if f.ExpandFinal {
		expand = "..."
	}
	if !commented && !multi && !p.force[f] {
		return f.Name + "(" + strings.Join(args, ", ") + expand + ")"
	}
	var b strings.Builder
	b.WriteString(f.Name + "(" + p.open(open) + "\n")
	for i, x := range f.Args {
		sep := ","
		if i == len(f.Args)-1 && f.ExpandFinal {
			sep = expand
		}
		b.WriteString(p.element(x, args[i], ends[i], sep))
	}
	if end := p.comments(f.EndComment); len(end) > 0 {
		b.WriteString(strings.Join(end, "\n") + "\n")
	}
	b.WriteString(")")
	return b.String()
}

// object prints an object one item per line; {} when it is empty. It is on
// one line, when it has no comments and no item spans lines, inside a
// template interpolation (where terraform fmt would dedent the lines after
// it) or, with the objects rule off, when it was on one line.
func (p *printer) object(o *types.ObjectExpr) string {
	open, end := p.comments(o.OpenComment), p.comments(o.EndComment)
	if len(o.Items) == 0 && len(end) == 0 {
		return "{}"
	}
	items := make([]string, len(o.Items))
	one := len(open) == 0 && len(end) == 0 &&
		(p.inTemplate || !p.on(config.RuleObjects) && o.ExprRange.Start.Line == o.ExprRange.End.Line)
	for i := range o.Items {
		var commented bool
		items[i], commented = p.objectItem(&o.Items[i])
		one = one && !commented && !strings.Contains(items[i], "\n")
	}
	if one {
		return "{ " + strings.Join(items, ", ") + " }"
	}
	var b strings.Builder
	b.WriteString("{" + p.open(open) + "\n")
	for _, item := range items {
		b.WriteString(item + "\n")
	}
	if len(end) > 0 {
		b.WriteString(strings.Join(end, "\n") + "\n")
	}
	b.WriteString("}")
	return b.String()
}

// open prints the lines of a list's, call's or object's OpenComment at the
// end of its opening bracket's line, where the next run reads the first one
// back, and counts them as printed in place.
func (p *printer) open(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	p.inlined++
	return " " + strings.Join(lines, "\n")
}

// objectItem prints an object item with its comments, and tells whether it
// has any.
func (p *printer) objectItem(it *types.ObjectItem) (string, bool) {
	if done, ok := p.items[it]; ok {
		p.inlined += done.inlined
		return done.text, done.commented
	}
	save := p.hoist
	var hoisted []string
	p.hoist = &hoisted
	n := p.inlined
	print := func() string {
		hoisted = hoisted[:0]
		return p.sub(it.Key, false) + " = " + p.sub(it.Value, false)
	}
	text, inline, trail := p.settle(it.Value, print, p.inline(it.InlineComment, it.InlineInside), p.comments(it.TrailComment))
	p.hoist = save
	above := append(p.comments(it.BlockComment), hoisted...)
	done := printedItem{place(above, text, inline, trail), len(above)+len(inline)+len(trail) > 0 || p.inlined > n, p.inlined - n}
	p.items[it] = done
	return done.text, done.commented
}

// keyText is how an object key sorts: its name, its string, or its text.
func (p *printer) keyText(k types.Expression) string {
	switch k := k.(type) {
	case *types.ReferenceExpr:
		if len(k.Parts) == 1 {
			return k.Parts[0]
		}
	case *types.LiteralValue:
		if s, ok := k.Value.(string); ok && k.ValueType == "string" {
			return s
		}
	}
	save := p.hoist
	p.hoist = nil
	defer func() { p.hoist = save }()
	return p.expr(k, false)
}
