package format

import (
	"sort"
	"strings"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/parser/types"
)

// walk calls fn on e and every expression inside it, parents first.
func walk(e types.Expression, fn func(types.Expression)) {
	if e == nil {
		return
	}
	fn(e)
	each := func(xs ...types.Expression) {
		for _, x := range xs {
			walk(x, fn)
		}
	}
	switch e := e.(type) {
	case *types.ObjectExpr:
		for _, item := range e.Items {
			each(item.Key, item.Value)
		}
	case *types.ArrayExpr:
		each(e.Items...)
	case *types.FunctionCallExpr:
		each(e.Args...)
	case *types.TemplateExpr:
		each(e.Parts...)
	case *types.HeredocExpr:
		each(e.Parts...)
	case *types.ConditionalExpr:
		each(e.Condition, e.TrueExpr, e.FalseExpr)
	case *types.BinaryExpr:
		each(e.Left, e.Right)
	case *types.UnaryExpr:
		each(e.Expr)
	case *types.ParenExpr:
		each(e.Expression)
	case *types.ForArrayExpr:
		each(e.Collection, e.ThenValueExpr, e.Condition)
	case *types.ForMapExpr:
		each(e.Collection, e.ThenKeyExpr, e.ThenValueExpr, e.Condition)
	case *types.SplatExpr:
		each(e.Source, e.Each)
	case *types.IndexExpr:
		each(e.Collection, e.Key)
	case *types.RelativeTraversalExpr:
		each(e.Source)
	}
}

// visit calls fn on every entry of the body and the bodies inside it.
func visit(pl *plan, fn func(*entry, *plan)) {
	for _, e := range pl.all() {
		fn(e, pl)
		if e.plan != nil {
			visit(e.plan, fn)
		}
	}
}

// ---- Naming ----

func (f *formatter) name(s string) string {
	return strings.ReplaceAll(s, f.cfg.Naming.Replace, f.cfg.Naming.With)
}

// rename applies the naming rule to every item it is on for: attribute
// names, block types, the labels that name things, and everything in
// expressions that is a name (see renameExpr).
func (f *formatter) rename(top *plan) {
	if f.cfg.Naming.Replace == "" {
		return
	}
	visit(top, func(e *entry, _ *plan) {
		if !f.on(e.off, config.RuleNaming) {
			return
		}
		switch n := e.node.(type) {
		case *types.Attribute:
			if !f.exempt.names[n.Name] {
				n.Name = f.name(n.Name)
			}
			f.renameExpr(n.Value)
		case *types.Block:
			n.Type = f.name(n.Type)
			if i, ok := f.cfg.Naming.Labels[n.Type]; ok && i < len(n.Labels) {
				labels := append([]string{}, n.Labels...)
				labels[i] = f.name(labels[i])
				n.Labels = labels
			}
		}
	})
}

// renameExpr renames, in e: every step of a reference (unless the reference
// is to a name an ignore_naming marker keeps), for-expression variables,
// and unquoted object keys. Quoted text is never renamed.
func (f *formatter) renameExpr(e types.Expression) {
	skip := map[types.Expression]bool{}
	walk(e, func(x types.Expression) {
		if skip[x] {
			return
		}
		switch x := x.(type) {
		case *types.ReferenceExpr:
			if f.exempt.covers(x.Parts) || len(x.Parts) == 1 && f.exempt.names[x.Parts[0]] {
				return
			}
			parts := make([]string, len(x.Parts))
			for i, p := range x.Parts {
				parts[i] = f.name(p)
			}
			x.Parts = parts
		case *types.RelativeTraversalExpr:
			if ref, ok := x.Source.(*types.ReferenceExpr); ok && f.exempt.covers(ref.Parts) {
				return
			}
			f.renameSteps(x.Traversal)
		case *types.SplatExpr:
			if ref, ok := x.Source.(*types.ReferenceExpr); ok && f.exempt.covers(ref.Parts) {
				walk(x.Each, func(y types.Expression) { skip[y] = true })
			}
		case *types.ForArrayExpr:
			x.KeyVar, x.ValueVar = f.name(x.KeyVar), f.name(x.ValueVar)
		case *types.ForMapExpr:
			x.KeyVar, x.ValueVar = f.name(x.KeyVar), f.name(x.ValueVar)
		}
	})
}

func (f *formatter) renameSteps(steps []types.TraversalElem) {
	for i := range steps {
		if steps[i].Type == "attr" {
			steps[i].Name = f.name(steps[i].Name)
		}
	}
}

// ---- Object keys ----

// sortObjects sorts the keys of every object A-Z, ignoring case, in items
// the rule is on for.
func (f *formatter) sortObjects(top *plan) {
	p := &printer{cfg: f.cfg, text: f.root.Text}
	visit(top, func(e *entry, _ *plan) {
		a, ok := e.node.(*types.Attribute)
		if !ok || !f.on(e.off, config.RuleObjectKeys) {
			return
		}
		walk(a.Value, func(x types.Expression) {
			if o, ok := x.(*types.ObjectExpr); ok {
				keys := make([]string, len(o.Items))
				for i := range o.Items {
					keys[i] = strings.ToLower(p.keyText(o.Items[i].Key))
				}
				idx := make([]int, len(o.Items))
				for i := range idx {
					idx[i] = i
				}
				sort.SliceStable(idx, func(i, j int) bool { return keys[idx[i]] < keys[idx[j]] })
				items := make([]types.ObjectItem, len(o.Items))
				for i, k := range idx {
					items[i] = o.Items[k]
				}
				o.Items = items
			}
		})
	})
}

// ---- Body order ----

// sortKey places an item in a body: group 0 prepended blocks, 1 prepended
// items, 2 other attributes, 3 other blocks, 4 appended items; then its
// place in the priority table, or its name.
type sortKey struct {
	group, outer, inner int
	name                string
}

func (a sortKey) less(b sortKey) bool {
	if a.group != b.group {
		return a.group < b.group
	}
	if a.outer != b.outer {
		return a.outer < b.outer
	}
	if a.inner != b.inner {
		return a.inner < b.inner
	}
	return a.name < b.name
}

func locate(name string, groups []config.Group) (int, int, bool) {
	for outer, g := range groups {
		for inner, n := range g.Names {
			if n == name {
				return outer, inner, true
			}
		}
	}
	return 0, 0, false
}

// priority returns the table that orders the body of a block of this type.
func (f *formatter) priority(pl *plan) config.Priority {
	if pl.top {
		return f.cfg.TopLevel
	}
	return f.cfg.Priorities[pl.blockType]
}

func (f *formatter) key(n types.Body, p config.Priority, top bool) sortKey {
	block, isBlock := n.(*types.Block)
	name := ""
	if isBlock {
		name = block.Type
		if o, i, ok := locate(name, p.PrependedBlocks); ok {
			return sortKey{group: 0, outer: o, inner: i}
		}
	} else {
		name = n.(*types.Attribute).Name
	}
	if o, i, ok := locate(name, p.Prepended); ok {
		return sortKey{group: 1, outer: o, inner: i}
	}
	if o, i, ok := locate(name, p.Appended); ok {
		return sortKey{group: 4, outer: -o, inner: i}
	}
	if !isBlock {
		return sortKey{group: 2, name: strings.ToLower(name)}
	}
	if top {
		return sortKey{group: 3, name: strings.ToLower(strings.Join(append([]string{block.Type}, block.Labels...), " "))}
	}
	return sortKey{group: 3, name: strings.ToLower(effectiveType(block))}
}

// effectiveType is the type a nested block sorts by: dynamic "x" is an x.
func effectiveType(b *types.Block) string {
	if b.Type == "dynamic" && len(b.Labels) > 0 {
		return b.Labels[0]
	}
	return b.Type
}

// order sorts every body, and every section among itself, where the
// ordering rule is on. Sorting is stable, so blocks of one type keep their
// order.
func (f *formatter) order(top *plan) {
	var sortPlan func(pl *plan)
	sortEntries := func(pl *plan, list []*entry) {
		p := f.priority(pl)
		sort.SliceStable(list, func(i, j int) bool {
			return f.key(list[i].node, p, pl.top).less(f.key(list[j].node, p, pl.top))
		})
	}
	sortPlan = func(pl *plan) {
		if f.on(pl.off, config.RuleOrdering) {
			sortEntries(pl, pl.free)
		}
		for _, s := range pl.sections {
			if f.on(s.off, config.RuleOrdering) {
				sortEntries(pl, s.entries)
			}
		}
		for _, e := range pl.all() {
			if e.plan != nil {
				sortPlan(e.plan)
			}
		}
	}
	sortPlan(top)
}
