package format

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/parser/types"
)

// offSet is the set of rules turned off, by name. It is never changed once
// made, so entries share their parent's.
type offSet map[string]bool

func (o offSet) with(more offSet) offSet {
	if len(more) == 0 {
		return o
	}
	out := offSet{}
	for k := range o {
		out[k] = true
	}
	for k := range more {
		out[k] = true
	}
	return out
}

// String names the set, the same for equal sets.
func (o offSet) String() string {
	var rules []string
	for r := range o {
		rules = append(rules, r)
	}
	sort.Strings(rules)
	return strings.Join(rules, " ")
}

// entry is a body item as it will be printed.
type entry struct {
	node types.Body // *types.Block or *types.Attribute
	lead []string   // its leading comment lines, as stored, after markers that are not its own are split off
	off  offSet     // the rules off for the item and everything inside it
	plan *plan      // for a block, its body
}

// section is the run of items between # terralint <words> { and # }.
type section struct {
	head    []string // the comment lines above the opening marker, and the marker
	entries []*entry
	tail    []string // the comment lines after the last item, and the closing marker
	anchor  any      // the *entry or *section before it in the source; nil at the start of the body, endOfBody at its end
	before  *entry   // when set, the section goes right before this item instead (see keepTypeOrder)
	off     offSet
	chained bool // the next section's opening marker ended it, so that section must follow it
}

// endOfBody anchors a section that runs to the end of its body: it must
// stay there, or it would take in the items after it.
var endOfBody = new(int)

// plan is a body as it will be printed.
type plan struct {
	free      []*entry // the items outside sections
	sections  []*section
	end       []string // the comment lines after the last item
	off       offSet   // the rules off for the whole body
	blockType string   // the type of the block that holds the body; "" for the file
	depth     int      // 0 for the file's body, 1 for a top-level block's, and so on
	top       bool     // the body is the file's
}

// all returns every entry of the body, in sections or not.
func (pl *plan) all() []*entry {
	out := append([]*entry{}, pl.free...)
	for _, s := range pl.sections {
		out = append(out, s.entries...)
	}
	return out
}

// unit is a free item or a section, in printing order.
type unit struct {
	entry *entry
	sec   *section
}

// units returns the body in printing order: each section right after its
// anchor, the item or section that preceded it in the source, or right
// before the item keepTypeOrder chose.
func (pl *plan) units() []unit {
	after, before := map[any][]*section{}, map[*entry][]*section{}
	for _, s := range pl.sections {
		if s.before != nil {
			before[s.before] = append(before[s.before], s)
		} else {
			after[s.anchor] = append(after[s.anchor], s)
		}
	}
	var out []unit
	var follow func(anchor any)
	follow = func(anchor any) {
		for _, s := range after[anchor] {
			out = append(out, unit{sec: s})
			follow(s)
		}
	}
	follow(nil)
	for _, e := range pl.free {
		for _, s := range before[e] {
			out = append(out, unit{sec: s})
			follow(s)
		}
		out = append(out, unit{entry: e})
		follow(e)
	}
	follow(endOfBody)
	return out
}

// keepTypeOrder moves the sections of a block's body that hold nested
// blocks of a type also found outside them, so that the blocks of that type
// keep their source order whatever the anchor sorts to (D15): right after
// the last such block before the section (or the section holding it), or
// else right before the first such block after it. The first such type in
// the section decides. Sections that must stay where they are, at the end
// of the body or after the section they end, keep their anchor.
func (pl *plan) keepTypeOrder() {
	holder := map[*entry]any{}
	for _, e := range pl.free {
		holder[e] = e
	}
	for _, s := range pl.sections {
		for _, e := range s.entries {
			holder[e] = s
		}
	}
	all := pl.all()
	sort.SliceStable(all, func(i, j int) bool { return start(all[i].node) < start(all[j].node) })
	blockType := func(e *entry) string {
		if b, ok := e.node.(*types.Block); ok {
			return strings.ToLower(effectiveType(b))
		}
		return ""
	}
	for _, s := range pl.sections {
		if prev, ok := s.anchor.(*section); s.anchor == endOfBody || ok && prev.chained {
			continue
		}
		for _, e := range s.entries {
			t := blockType(e)
			var last, next *entry
			for _, o := range all {
				if t == "" || holder[o] == s || blockType(o) != t {
					continue
				}
				if start(o.node) < start(e.node) {
					last = o
				} else if next == nil {
					next = o
				}
			}
			if last == nil && next == nil {
				continue
			}
			if last != nil {
				s.anchor = holder[last]
			} else if holder[next] == next {
				s.before = next
			} // else a section holds it, which follows this one when that type decides for it
			break
		}
	}
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// marker is a parsed # terralint <words> [{] comment.
type marker struct {
	open bool   // ends in {: opens a section
	off  offSet // the rules its ignore_<rule> words turn off
}

// commentText is the text of a stored comment line, without the marker
// that a comment held whole keeps (see types.Root).
func commentText(line string) string {
	for _, m := range []string{"//", "#"} {
		if rest, ok := strings.CutPrefix(line, m); ok {
			return strings.TrimPrefix(rest, " ")
		}
	}
	return line
}

// marker parses a stored comment line; nil if it is not a marker.
func (f *formatter) marker(line string) *marker {
	fields := strings.Fields(commentText(line))
	if len(fields) == 0 || fields[0] != f.cfg.SectionLabel {
		return nil
	}
	m := &marker{off: offSet{}}
	words := fields[1:]
	if n := len(words); n > 0 && strings.HasSuffix(words[n-1], "{") {
		m.open = true
		words[n-1] = strings.TrimSuffix(words[n-1], "{")
	}
	for _, w := range words {
		if rule, ok := strings.CutPrefix(w, f.cfg.IgnorePrefix); ok {
			if _, known := f.cfg.Rules[rule]; known {
				m.off[rule] = true
			}
		}
	}
	return m
}

// commentScanner walks stored comment lines and tells which are whole line
// comments (not inside a /* */ comment), where markers can be.
type commentScanner struct{ inBlock bool }

func (s *commentScanner) whole(line string) bool {
	t := strings.TrimSpace(line)
	if s.inBlock {
		s.inBlock = !strings.HasSuffix(t, "*/")
		return false
	}
	if strings.HasPrefix(line, "/*") {
		s.inBlock = !strings.HasSuffix(t, "*/") || len(t) < 4
		return false
	}
	return true
}

// planFile plans the file's body. It leaves the AST as it is. The file
// header ends before a group of its comments that holds a marker: the marker
// and the comments right above it belong to what follows.
func (f *formatter) planFile() *plan {
	children := f.root.Children
	f.header = nil
	if len(children) > 0 {
	groups:
		for _, g := range f.root.HeaderGroups {
			var scan commentScanner
			for _, line := range lines(g) {
				if scan.whole(line) && f.marker(line) != nil {
					break groups
				}
			}
			f.header = append(f.header, lines(g)...)
		}
	}
	return f.planBody(children, f.root.EndComment, offSet{}, "", 0, len(f.header))
}

// planBody plans a body: it splits its items' leading comments into
// sections and markers. skip leading comment lines of the first item are
// the file header, which the printer puts first.
func (f *formatter) planBody(children []types.Body, end string, off offSet, blockType string, depth, skip int) *plan {
	pl := &plan{off: off, blockType: blockType, depth: depth, top: depth == 0}
	var cur *section
	var prev any
	balance := 0
	// closes reports whether line ends the open section; braces in the
	// comments inside it (commented-out code) must balance first.
	closes := func(line string, whole bool) bool {
		if cur == nil || !whole {
			return false
		}
		t := strings.TrimSpace(commentText(line))
		if t == "}" && balance == 0 {
			return true
		}
		balance += strings.Count(t, "{") - strings.Count(t, "}")
		return false
	}
	for i, child := range children {
		var scan commentScanner
		var pending []string
		itemOff := offSet{}
		ls := lines(leadingComment(child))
		if i == 0 && skip <= len(ls) {
			ls = ls[skip:]
		}
		for _, line := range ls {
			whole := scan.whole(line)
			if closes(line, whole) {
				cur.tail = append(pending, line)
				pending, prev, cur = nil, cur, nil
				continue
			}
			if m := f.marker(line); whole && m != nil {
				if m.open {
					if cur != nil {
						cur.chained, prev = true, cur
					}
					cur = &section{head: append(pending, line), anchor: prev, off: off.with(m.off)}
					pl.sections = append(pl.sections, cur)
					pending, balance = nil, 0
					continue
				}
				itemOff = itemOff.with(m.off)
			}
			pending = append(pending, line)
		}
		base := off
		if cur != nil {
			base = cur.off
		}
		e := &entry{node: child, lead: pending, off: base.with(itemOff)}
		if b, ok := child.(*types.Block); ok {
			inner := e.off
			if depth == 0 && b.Type == "module" && inner[config.RuleNaming] {
				// A module call's arguments name the child module's
				// variables, so ignore_naming on the call keeps only the
				// call's own name; its body follows the child's names.
				inner = offSet{}
				for r := range e.off {
					if r != config.RuleNaming {
						inner[r] = true
					}
				}
			}
			e.plan = f.planBody(b.Children, b.EndComment, inner, b.Type, depth+1, 0)
		}
		if cur != nil {
			cur.entries = append(cur.entries, e)
			prev = cur
		} else {
			pl.free = append(pl.free, e)
			prev = e
		}
	}
	var scan commentScanner
	for _, line := range lines(end) {
		if closes(line, scan.whole(line)) {
			cur.tail = append(pl.end, line)
			pl.end, cur = nil, nil
			continue
		}
		pl.end = append(pl.end, line)
	}
	if cur != nil {
		// An unclosed section, and the sections its marker chain started
		// from, stay at the end of the body.
		s := cur
		for {
			before, ok := s.anchor.(*section)
			if !ok || !before.chained {
				break
			}
			s = before
		}
		s.anchor = endOfBody
	}
	if depth > 0 {
		pl.keepTypeOrder()
	}
	return pl
}

// start is where an item starts in the source.
func start(n types.Body) int {
	switch n := n.(type) {
	case *types.Block:
		return n.Range.Start.Byte
	case *types.Attribute:
		return n.Range.Start.Byte
	}
	return 0
}

func leadingComment(n types.Body) string {
	switch n := n.(type) {
	case *types.Block:
		return n.BlockComment
	case *types.Attribute:
		return n.BlockComment
	}
	return ""
}

// mergeLocals merges the file's locals blocks into the first of them, which
// keeps its place. Only blocks in the same section (or none) and with the
// same rules off merge, so that every item stays under the markers it was
// under. A block that the locals rule is off for, or that would define a
// name twice, stays as it is.
func (f *formatter) mergeLocals(pl *plan) {
	type target struct {
		into  *entry
		names map[string]bool
	}
	targets := map[string]*target{}
	where := map[*entry]*section{}
	for _, s := range pl.sections {
		for _, e := range s.entries {
			where[e] = s
		}
	}
	all := pl.all()
	sort.SliceStable(all, func(i, j int) bool { return start(all[i].node) < start(all[j].node) })
	for _, e := range all {
		b, ok := e.node.(*types.Block)
		if !ok || b.Type != "locals" || !f.on(e.off, config.RuleLocals) {
			continue
		}
		key := fmt.Sprintf("%p %s", where[e], e.off)
		t := targets[key]
		if t == nil {
			t = &target{names: map[string]bool{}}
		}
		dup := false
		for _, c := range b.Children {
			if a, ok := c.(*types.Attribute); ok && t.names[a.Name] {
				dup = true
			}
		}
		if dup {
			continue
		}
		for _, c := range b.Children {
			if a, ok := c.(*types.Attribute); ok {
				t.names[a.Name] = true
			}
		}
		if t.into == nil {
			t.into = e
			targets[key] = t
			continue
		}
		f.mergeInto(pl, t.into, e)
	}
}

// mergeInto moves locals block e's items and comments into block into, and
// drops e from the body.
func (f *formatter) mergeInto(pl *plan, into, e *entry) {
	dst, src := into.node.(*types.Block), e.node.(*types.Block)
	dst.Children = append(dst.Children, src.Children...)
	into.lead = append(into.lead, e.lead...)
	if !src.InlineInside {
		into.lead = append(into.lead, lines(src.InlineComment)...)
	}
	into.lead = append(into.lead, lines(src.TrailComment)...)

	// Sections at the start of e's body follow what ends into's.
	var last any
	if us := into.plan.units(); len(us) > 0 {
		if u := us[len(us)-1]; u.sec != nil {
			last = u.sec
		} else {
			last = u.entry
		}
	}
	for _, s := range e.plan.sections {
		if s.anchor == nil {
			s.anchor = last
		}
	}
	into.plan.free = append(into.plan.free, e.plan.free...)
	into.plan.sections = append(into.plan.sections, e.plan.sections...)
	into.plan.end = append(into.plan.end, e.plan.end...)

	// Drop e; a section that followed it now follows into.
	remove := func(list []*entry) []*entry {
		out := list[:0]
		for _, x := range list {
			if x != e {
				out = append(out, x)
			}
		}
		return out
	}
	pl.free = remove(pl.free)
	for _, s := range pl.sections {
		s.entries = remove(s.entries)
		if s.anchor == e {
			s.anchor = into
		}
	}
}
