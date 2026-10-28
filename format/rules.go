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

// rename applies the naming rule: to attribute names, block types, the
// labels that name things, and everything in expressions that is a name
// (see renameExpr). Items the rule is off for keep their own names, but
// their references to names declared elsewhere are renamed, so that they
// still match.
func (f *formatter) rename(top *plan) {
	if f.cfg.Naming.Replace == "" {
		return
	}
	f.renamePlan(top, nil)
}

// renamePlan renames in a body. kept holds the iterator names of the
// dynamic blocks around it that keep their names.
func (f *formatter) renamePlan(pl *plan, kept map[string]bool) {
	for _, e := range pl.all() {
		on := f.on(e.off, config.RuleNaming)
		switch n := e.node.(type) {
		case *types.Attribute:
			// The assignments of a .tfvars file and the arguments of a module
			// call are named after variables: they keep the names those keep.
			// An exempt variable's assignment keeps its object keys too.
			name := on
			switch {
			case f.tfvars && pl.top:
				held, deep := f.exempt.covers([]string{"var", n.Name})
				on = on && !deep
				name = on && held == 0 && f.renames("variable")
			case pl.blockType == "module" && pl.depth == 1:
				name = on && f.renames("variable") // the child module's variables
			}
			if name && !f.exempt.names[n.Name] {
				n.Name = f.name(n.Name)
			}
			f.renameExpr(n.Value, on, kept)
		case *types.Block:
			inner := kept
			if on {
				n.Type = f.name(n.Type)
				// A declaration that a marker elsewhere exempts keeps its name.
				if i, ok := f.cfg.Naming.Labels[n.Type]; ok && i < len(n.Labels) && !(pl.top && f.exempted(declares(n))) {
					labels := append([]string{}, n.Labels...)
					labels[i] = f.name(labels[i])
					n.Labels = labels
				} else if !ok && n.Type == "dynamic" && len(n.Labels) > 0 {
					inner = with(kept, n.Labels[0])
				}
			} else if n.Type == "dynamic" && len(n.Labels) > 0 {
				inner = with(kept, n.Labels[0])
				for _, c := range n.Children {
					if a, ok := c.(*types.Attribute); ok && a.Name == "iterator" {
						if r, ok := a.Value.(*types.ReferenceExpr); ok && len(r.Parts) == 1 {
							inner = with(inner, r.Parts[0])
						}
					}
				}
			}
			f.renamePlan(e.plan, inner)
		}
	}
}

// exempted reports whether an exemption covers the references that start
// with p.
func (f *formatter) exempted(p []string) bool {
	n, _ := f.exempt.covers(p)
	return p != nil && n > 0
}

// with returns kept with more names.
func with(kept map[string]bool, names ...string) map[string]bool {
	out := map[string]bool{}
	for k := range kept {
		out[k] = true
	}
	for _, n := range names {
		out[n] = true
	}
	return out
}

// rootRef returns the reference a chain of traversals, indexes and splats
// starts from, or nil.
func rootRef(e types.Expression) *types.ReferenceExpr {
	for {
		switch x := e.(type) {
		case *types.ReferenceExpr:
			return x
		case *types.RelativeTraversalExpr:
			e = x.Source
		case *types.IndexExpr:
			e = x.Collection
		case *types.SplatExpr:
			e = x.Source
		default:
			return nil
		}
	}
}

// renameExpr renames, in e: every step of a reference (but the names an
// exemption keeps, and those of references that start with a name in kept),
// for-expression variables, and unquoted object keys. Quoted text is never
// renamed. When on is false, the rule is off for e's item: its
// for-expression variables (and so the references to them) and its object
// keys stay.
func (f *formatter) renameExpr(e types.Expression, on bool, kept map[string]bool) {
	if !on {
		walk(e, func(x types.Expression) {
			switch x := x.(type) {
			case *types.ForArrayExpr:
				kept = with(kept, x.KeyVar, x.ValueVar)
			case *types.ForMapExpr:
				kept = with(kept, x.KeyVar, x.ValueVar)
			}
		})
	}
	outputs := f.renames("output")
	// renamed returns the names of ref that are renamed, parts[from:to],
	// and whether the steps after it are. After module.<name> come the
	// child module's outputs, which keep their names when outputs do.
	renamed := func(ref *types.ReferenceExpr) (from, to int, steps bool) {
		switch {
		case ref == nil:
			return 0, 0, true
		case kept[ref.Parts[0]] || len(ref.Parts) == 1 && f.exempt.names[ref.Parts[0]]:
			return 0, 0, false
		}
		n, deep := f.exempt.covers(ref.Parts)
		switch {
		case deep:
			return 0, 0, false
		case ref.Parts[0] == "module" && !outputs:
			return n, min(len(ref.Parts), 2), false
		}
		return n, len(ref.Parts), true
	}
	skip := map[types.Expression]bool{}
	walk(e, func(x types.Expression) {
		if skip[x] {
			return
		}
		switch x := x.(type) {
		case *types.ObjectExpr:
			for _, item := range x.Items {
				if k, ok := item.Key.(*types.ReferenceExpr); ok && len(k.Parts) == 1 && !on {
					skip[k] = true
				}
			}
		case *types.ReferenceExpr:
			from, to, _ := renamed(x)
			parts := append([]string{}, x.Parts...)
			for i := from; i < to; i++ {
				parts[i] = f.name(parts[i])
			}
			x.Parts = parts
		case *types.RelativeTraversalExpr:
			if _, _, steps := renamed(rootRef(x.Source)); steps {
				f.renameSteps(x.Traversal)
			}
		case *types.SplatExpr:
			if _, _, steps := renamed(rootRef(x.Source)); !steps {
				walk(x.Each, func(y types.Expression) { skip[y] = true })
			}
		case *types.ForArrayExpr:
			if on {
				x.KeyVar, x.ValueVar = f.name(x.KeyVar), f.name(x.ValueVar)
			}
		case *types.ForMapExpr:
			if on {
				x.KeyVar, x.ValueVar = f.name(x.KeyVar), f.name(x.ValueVar)
			}
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

// key places an item in its body. Within a priority group, as among other
// items, top-level blocks sort by type and labels, nested blocks by type
// only (stable, so blocks of one type keep their order), attributes by name.
func (f *formatter) key(n types.Body, p config.Priority, top bool) sortKey {
	block, isBlock := n.(*types.Block)
	name, sortName := "", ""
	switch {
	case !isBlock:
		name = n.(*types.Attribute).Name
		sortName = name
	case top:
		name = block.Type
		sortName = strings.Join(append([]string{block.Type}, block.Labels...), " ")
	default:
		name = block.Type
		sortName = effectiveType(block)
	}
	sortName = strings.ToLower(sortName)
	if isBlock {
		if o, i, ok := locate(name, p.PrependedBlocks); ok {
			return sortKey{group: 0, outer: o, inner: i, name: sortName}
		}
	}
	if o, i, ok := locate(name, p.Prepended); ok {
		return sortKey{group: 1, outer: o, inner: i, name: sortName}
	}
	if o, i, ok := locate(name, p.Appended); ok {
		return sortKey{group: 4, outer: -o, inner: i, name: sortName}
	}
	if !isBlock {
		return sortKey{group: 2, name: sortName}
	}
	return sortKey{group: 3, name: sortName}
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
