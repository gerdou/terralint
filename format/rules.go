package format

import (
	"slices"
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
	for _, x := range parts(e) {
		walk(x, fn)
	}
}

// parts returns the expressions directly inside e, in the order they
// print; nil stands for an absent one.
func parts(e types.Expression) []types.Expression {
	switch e := e.(type) {
	case *types.ObjectExpr:
		var out []types.Expression
		for _, item := range e.Items {
			out = append(out, item.Key, item.Value)
		}
		return out
	case *types.ArrayExpr:
		return e.Items
	case *types.FunctionCallExpr:
		return e.Args
	case *types.TemplateExpr:
		return e.Parts
	case *types.HeredocExpr:
		return e.Parts
	case *types.ConditionalExpr:
		return []types.Expression{e.Condition, e.TrueExpr, e.FalseExpr}
	case *types.BinaryExpr:
		return []types.Expression{e.Left, e.Right}
	case *types.UnaryExpr:
		return []types.Expression{e.Expr}
	case *types.ParenExpr:
		return []types.Expression{e.Expression}
	case *types.ForArrayExpr:
		return []types.Expression{e.Collection, e.ThenValueExpr, e.Condition}
	case *types.ForMapExpr:
		return []types.Expression{e.Collection, e.ThenKeyExpr, e.ThenValueExpr, e.Condition}
	case *types.SplatExpr:
		return []types.Expression{e.Source, e.Each}
	case *types.IndexExpr:
		return []types.Expression{e.Collection, e.Key}
	case *types.RelativeTraversalExpr:
		return []types.Expression{e.Source}
	}
	return nil
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
	f.renamePlan(top, nil, false)
}

// renamePlan renames in a body. iters holds the iterator names of the
// dynamic blocks around it, true for those that keep their names; keep says
// the rule is off for the whole body.
func (f *formatter) renamePlan(pl *plan, iters map[string]bool, keep bool) {
	providers := f.renames("provider")
	for _, e := range pl.all() {
		on := !keep && f.on(e.off, config.RuleNaming)
		if !providers && providerPlace(pl, e.node) {
			continue // provider local names: Terraform rejects "_" in them
		}
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
			f.renameExpr(n.Value, on, iters)
		case *types.Block:
			// A declaration exempt with its keys (by a .tfvars marker) keeps
			// every name in it, as under a marker of its own.
			_, deep := f.exempt.covers(declares(n))
			deep = deep && pl.top
			on = on && !deep
			inner := iters
			if n.Type == "dynamic" && len(n.Labels) > 0 {
				// Its iterator: the label, which keeps its name where the
				// label does, or the iterator argument's name.
				inner = with(iters, !on || !f.renames("dynamic"), n.Labels[0])
				for _, c := range n.Children {
					if a, ok := c.(*types.Attribute); ok && a.Name == "iterator" {
						if r, ok := a.Value.(*types.ReferenceExpr); ok && len(r.Parts) == 1 {
							inner = with(inner, !on, r.Parts[0])
						}
					}
				}
			}
			if on {
				n.Type = f.name(n.Type)
				// A declaration that a marker elsewhere exempts keeps its name.
				if i, ok := f.cfg.Naming.Labels[n.Type]; ok && i < len(n.Labels) && !(pl.top && f.exempted(declares(n))) {
					labels := append([]string{}, n.Labels...)
					labels[i] = f.name(labels[i])
					n.Labels = labels
				}
			}
			f.renamePlan(e.plan, inner, keep || deep)
		}
	}
}

// providerPlace reports whether a body item holds only provider local
// names: a required_providers block, the provider meta-argument of a
// resource, data, ephemeral or import block, or a module call's providers.
func providerPlace(pl *plan, n types.Body) bool {
	switch n := n.(type) {
	case *types.Block:
		return n.Type == "required_providers"
	case *types.Attribute:
		switch n.Name {
		case "provider":
			return slices.Contains([]string{"resource", "data", "ephemeral", "import"}, pl.blockType)
		case "providers":
			return pl.blockType == "module" && pl.depth == 1
		}
	}
	return false
}

// exempted reports whether an exemption covers the references that start
// with p.
func (f *formatter) exempted(p []string) bool {
	n, _ := f.exempt.covers(p)
	return p != nil && n > 0
}

// with returns iters with more iterator names, which keep their names or
// not.
func with(iters map[string]bool, keep bool, names ...string) map[string]bool {
	out := map[string]bool{}
	for k, v := range iters {
		out[k] = v
	}
	for _, n := range names {
		if n != "" {
			out[n] = keep
		}
	}
	return out
}

// readName renames a name read through an iterator: each.value, a dynamic
// block's iterator or a for-expression variable. The formatter cannot tell
// which declaration such a read reaches, so a name some ignore_naming
// marker keeps as a key keeps its dashes (D14).
func (f *formatter) readName(s string) string {
	if f.exempt.keys[s] {
		return s
	}
	return f.name(s)
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
// exemption keeps, and the names read through an iterator that a marker
// keeps as keys, see readName), for-expression variables, and unquoted object
// keys. Quoted text is never renamed. When on is false, the rule is off for
// e's item: its for-expression variables (and so the references to them)
// and its object keys stay. iters holds the iterator names around e (see
// renamePlan).
func (f *formatter) renameExpr(e types.Expression, on bool, iters map[string]bool) {
	walk(e, func(x types.Expression) {
		switch x := x.(type) {
		case *types.ForArrayExpr:
			iters = with(iters, !on, x.KeyVar, x.ValueVar)
		case *types.ForMapExpr:
			iters = with(iters, !on, x.KeyVar, x.ValueVar)
		}
	})
	// iter reports whether ref reads through an iterator (each.value counts
	// as one), and whether its first part keeps its name.
	iter := func(ref *types.ReferenceExpr) (keep, ok bool) {
		if ref == nil {
			return false, false
		}
		if keep, ok := iters[ref.Parts[0]]; ok {
			return keep, true
		}
		return true, ref.Parts[0] == "each"
	}
	outputs := f.renames("output")
	// renamed returns which parts of ref keep their names, and whether the
	// steps after it are renamed. After module.<name> comes the name of one
	// of the child module's outputs, which keeps its name when outputs do
	// (output): the third part, or else the first attribute step after the
	// reference. What follows it reads the output's value, whose keys the
	// child renames. A reference through a local alias keeps the local's
	// name as the local does, and the rest as a read of what the local
	// aliases would (D16).
	renamed := func(ref *types.ReferenceExpr) (keep func(int) bool, output, steps bool) {
		switch {
		case ref == nil:
			return func(int) bool { return false }, false, true
		case len(ref.Parts) == 1 && f.exempt.names[ref.Parts[0]]:
			return func(int) bool { return true }, false, false
		}
		read, alias := f.through(ref.Parts)
		own, _ := f.exempt.covers(ref.Parts[:min(2, len(ref.Parts))])
		shift := len(read) - len(ref.Parts) // read[i+shift] is ref.Parts[i], from the third part on
		n, deep := f.exempt.covers(read)
		output = !deep && read[0] == "module" && !outputs
		return func(i int) bool {
			if alias && i < 2 {
				return i < own
			}
			i += shift
			return deep || i < n || output && i == 2
		}, output, !deep
	}
	first := map[types.Expression]bool{} // traversals whose first attribute step is an output's name
	keys := map[types.Expression]bool{}  // traversals that read through an iterator
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
			parts := append([]string{}, x.Parts...)
			if keep, ok := iter(x); ok {
				for i := range parts {
					if i > 0 {
						parts[i] = f.readName(parts[i])
					} else if !keep {
						parts[i] = f.name(parts[i])
					}
				}
				x.Parts = parts
				break
			}
			keep, _, _ := renamed(x)
			for i := range parts {
				if !keep(i) {
					parts[i] = f.name(parts[i])
				}
			}
			x.Parts = parts
		case *types.RelativeTraversalExpr:
			if _, ok := iter(rootRef(x.Source)); ok || keys[x] {
				f.renameSteps(x.Traversal, false, f.readName)
				break
			}
			_, output, steps := renamed(rootRef(x.Source))
			first[x] = first[x] || output && !f.readsOutput(x.Source)
			if steps {
				f.renameSteps(x.Traversal, first[x], f.name)
			}
		case *types.SplatExpr:
			if _, ok := iter(rootRef(x.Source)); ok {
				walk(x.Each, func(y types.Expression) { keys[y] = true })
				break
			}
			_, output, steps := renamed(rootRef(x.Source))
			if !steps {
				walk(x.Each, func(y types.Expression) { skip[y] = true })
			} else if each, ok := x.Each.(*types.RelativeTraversalExpr); ok && output && !f.readsOutput(x.Source) {
				first[each] = true
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

// renameSteps renames the attribute steps of a traversal with rename, but
// the first one when keepFirst.
func (f *formatter) renameSteps(steps []types.TraversalElem, keepFirst bool, rename func(string) string) {
	for i := range steps {
		if steps[i].Type == "attr" {
			if !keepFirst {
				steps[i].Name = rename(steps[i].Name)
			}
			keepFirst = false
		}
	}
}

// readsOutput reports whether a chain that starts at module.<name>, or at a
// local alias of it, has an attribute step after those two parts: the name
// of the output it reads.
func (f *formatter) readsOutput(e types.Expression) bool {
	switch x := e.(type) {
	case *types.ReferenceExpr:
		read, _ := f.through(x.Parts)
		return len(read) > 2
	case *types.RelativeTraversalExpr:
		return f.readsOutput(x.Source) || slices.ContainsFunc(x.Traversal, func(s types.TraversalElem) bool { return s.Type == "attr" })
	case *types.IndexExpr:
		return f.readsOutput(x.Collection)
	case *types.SplatExpr:
		return f.readsOutput(x.Source) || f.readsOutput(x.Each)
	}
	return false
}

// ---- Object keys ----

// sortObjects sorts the keys of every object A-Z, ignoring case, in items
// the rule is on for.
func (f *formatter) sortObjects(top *plan) {
	p := newPrinter(f.cfg, f.root.Text)
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
