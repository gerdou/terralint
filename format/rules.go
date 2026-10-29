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

// rename applies the naming rule to names: everything left of a "=" (the
// attribute names of blocks, locals, .tfvars files and module calls, and
// unquoted object keys), block types, and the labels that name things.
// Values are never renamed, but for the name part of a reference to a name
// the directory declares and the rule renames, which follows it (D18):
// var.<name>, local.<name>, module.<name>, <type>.<name>,
// data.<type>.<name>, ephemeral.<type>.<name>, and an output read from a
// local child module. A data source declared in a check block is scoped to
// it: references to it there follow it (D24). Items under ignore_naming keep
// their own names; a marker on a variable or on a .tfvars assignment of it
// keeps every name of both. A module call's arguments name the child's
// variables: one that a local child declares follows that variable, whatever
// markers the call carries, and keeps its keys when the variable is marked;
// others are ordinary names (D23). With the rule off in config, nothing is
// renamed.
func (f *formatter) rename(top *plan) {
	if f.cfg.Naming.Replace == "" || !f.cfg.On(config.RuleNaming) {
		return
	}
	f.names = newNames()
	f.names.collect(f, top) // before any renaming: references follow the names as written
	f.renamePlan(top, false)
}

// dashed reports whether s holds what the rule replaces.
func (f *formatter) dashed(s string) bool { return strings.Contains(s, f.cfg.Naming.Replace) }

// renamePlan renames in a body; keep says the rule is off for all of it.
func (f *formatter) renamePlan(pl *plan, keep bool) {
	for _, e := range pl.all() {
		on := !keep && f.on(e.off, config.RuleNaming)
		if providerPlace(pl, e.node) {
			continue // provider local names: Terraform rejects "_" in them
		}
		switch n := e.node.(type) {
		case *types.Attribute:
			// The assignments of a .tfvars file and the arguments of a module
			// call are named after variables, and keep the names those keep.
			tfvars := f.tfvars && pl.top
			arg := pl.blockType == "module" && pl.depth == 1
			if tfvars && f.dashed(n.Name) && f.decl().whole["var."+n.Name] {
				on = false
			}
			name, keys := on, on
			if tfvars || arg {
				name = on && f.renames("variable") && !(tfvars && f.dashed(n.Name) && f.decl().kept("var."+n.Name))
			}
			if v := "var." + n.Name; arg && f.callee != nil {
				// D23: an argument the local child declares follows the
				// child's variable, whatever markers the call carries.
				if _, ok := f.callee.renamed[v]; ok && f.dashed(n.Name) {
					name = f.callee.renames(v)
				}
				keys = keys && !f.callee.whole[v]
			}
			if name {
				n.Name = f.name(n.Name)
			}
			if keys {
				f.renameKeys(n.Value)
			}
			f.renameRefs(n.Value)
		case *types.Block:
			if pl.top && n.Type == "variable" && len(n.Labels) > 0 && f.dashed(n.Labels[0]) && f.decl().whole["var."+n.Labels[0]] {
				on = false
			}
			if pl.top {
				f.scope, f.callee = f.checkScope(n, e.plan), nil
				if n.Type == "module" && localSource(source(n)) {
					f.callee = f.child(source(n))
				}
			}
			if on {
				n.Type = f.name(n.Type)
				if i, ok := f.cfg.Naming.Labels[n.Type]; ok && i < len(n.Labels) && n.Type != "provider" {
					labels := append([]string{}, n.Labels...)
					labels[i] = f.name(labels[i])
					n.Labels = labels
				}
			}
			f.renamePlan(e.plan, keep || !on && n.Type == "variable")
			if pl.top {
				f.scope, f.callee = nil, nil
			}
		}
	}
}

// checkScope returns, for a check block, the data sources declared in it by
// the start of the references to them, each true when it is renamed: they
// are scoped to the check (D24). Else nil.
func (f *formatter) checkScope(b *types.Block, pl *plan) map[string]bool {
	if b.Type != "check" {
		return nil
	}
	scope := map[string]bool{}
	for _, e := range pl.all() {
		if d, ok := e.node.(*types.Block); ok && d.Type == "data" {
			if key := declKey(d); key != "" {
				scope[key] = f.on(e.off, config.RuleNaming) && f.renames("data")
			}
		}
	}
	return scope
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

// renameKeys renames the unquoted keys of the objects in e: they are left of
// a "=". Quoted keys are text and keep their dashes.
func (f *formatter) renameKeys(e types.Expression) {
	walk(e, func(x types.Expression) {
		if o, ok := x.(*types.ObjectExpr); ok {
			for _, it := range o.Items {
				if k, ok := it.Key.(*types.ReferenceExpr); ok && len(k.Parts) == 1 {
					k.Parts = []string{f.name(k.Parts[0])}
				}
			}
		}
	})
}

// renameRefs renames, in e, the name part of each reference to a declaration
// the rule renames (see rename). Nothing else in a value changes: not the
// keys a reference reads (var.x.my-key), the names read through each.value,
// an iterator or a for-expression variable, the outputs of modules the
// formatter does not format, nor the old address in a moved or removed
// block, which no declaration names.
func (f *formatter) renameRefs(e types.Expression) {
	keys := map[types.Expression]bool{}
	walk(e, func(x types.Expression) {
		switch x := x.(type) {
		case *types.ObjectExpr:
			for _, it := range x.Items {
				keys[it.Key] = true
			}
		case *types.ReferenceExpr:
			if !keys[x] {
				x.Parts = f.refParts(x.Parts)
			}
		case *types.RelativeTraversalExpr: // module.<name>[i].<output>
			if m := moduleCall(x.Source); m != "" {
				f.renameOutput(m, x.Traversal)
			}
		case *types.SplatExpr: // module.<name>[*].<output>
			if t, ok := x.Each.(*types.RelativeTraversalExpr); ok {
				if m := moduleCall(x.Source); m != "" {
					f.renameOutput(m, t.Traversal)
				}
			}
		}
	})
}

// refParts returns a reference's parts with its name part renamed when it
// names a declaration the rule renames.
func (f *formatter) refParts(parts []string) []string {
	p := append([]string{}, parts...)
	at := func(i int) {
		if i < len(p) && f.dashed(p[i]) && f.renamesRef(strings.Join(parts[:i+1], ".")) {
			p[i] = f.name(p[i])
		}
	}
	if len(p) < 2 {
		return p
	}
	switch p[0] {
	case "module":
		if len(p) > 2 && f.childRenames(parts[1], parts[2]) {
			p[2] = f.name(p[2])
		}
		at(1)
	case "data", "ephemeral":
		at(2)
	default: // var, local and <type>
		at(1)
	}
	return p
}

// renamesRef reports whether the declaration the references starting with
// key name is renamed: a data source of the check block being renamed
// (D24), else one the directory declares.
func (f *formatter) renamesRef(key string) bool {
	if r, ok := f.scope[key]; ok {
		return r
	}
	return f.decl().renames(key)
}

// moduleCall returns <name> when e is module.<name>, indexed or not.
func moduleCall(e types.Expression) string {
	for {
		switch x := e.(type) {
		case *types.IndexExpr:
			e = x.Collection
		case *types.ReferenceExpr:
			if len(x.Parts) == 2 && x.Parts[0] == "module" {
				return x.Parts[1]
			}
			return ""
		default:
			return ""
		}
	}
}

// renameOutput renames the first attribute step after module.<m>: the
// output it reads, when the local child module renames it.
func (f *formatter) renameOutput(m string, steps []types.TraversalElem) {
	for i := range steps {
		if steps[i].Type == "attr" {
			if f.childRenames(m, steps[i].Name) {
				steps[i].Name = f.name(steps[i].Name)
			}
			return
		}
	}
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
