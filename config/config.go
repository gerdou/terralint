// Package config holds every rule switch and parameter of terralint. Default
// returns the built-in values; a config.yaml will override them.
package config

// Rule names: the keys of Config.Rules, and the words that follow "ignore_"
// in a section marker (# terralint ignore_naming {).
const (
	RuleOrdering   = "ordering"    // Body items in priority, then A-Z, order
	RuleObjectKeys = "object_keys" // Object keys A-Z
	RuleObjects    = "objects"     // Objects with items one item per line (off: as written, one line or not)
	RuleNaming     = "naming"      // '-' becomes '_' in names
	RuleComments   = "comments"    // Comments become # comments, one per line
	RuleLocals     = "locals"      // A file's locals blocks merge into one
	RuleLists      = "lists"       // Short lists on one line, others one element per line
)

// Config is the full set of rules and their parameters.
type Config struct {
	// Rules says which rules are on, by rule name.
	Rules map[string]bool `yaml:"rules"`

	// SectionLabel is the word that starts a marker comment:
	// # <label> <words> { ... # }, or # <label> <words> for one item.
	SectionLabel string `yaml:"section_label"`
	// IgnorePrefix, followed by a rule name, is a marker word that turns the
	// rule off.
	IgnorePrefix string `yaml:"ignore_prefix"`

	// ListWidth is the widest a list may be on one line, from [ to ].
	ListWidth int `yaml:"list_width"`

	// BlankLinesAroundMultiLine separates an item that spans lines from its
	// neighbours; BlankLinesBetweenTopLevelBlocks separates the blocks of a
	// file; BlankLinesBeforeFileEndComments separates the comments at the
	// end of a file from the last item.
	BlankLinesAroundMultiLine       int `yaml:"blank_lines_around_multi_line"`
	BlankLinesBetweenTopLevelBlocks int `yaml:"blank_lines_between_top_level_blocks"`
	BlankLinesBeforeFileEndComments int `yaml:"blank_lines_before_file_end_comments"`

	Naming Naming `yaml:"naming"`

	// TopLevel orders the items of a file; Priorities those of a block's
	// body, by block type. Types without an entry sort A-Z only.
	TopLevel   Priority            `yaml:"top_level"`
	Priorities map[string]Priority `yaml:"priorities"`
}

// Naming is what the naming rule replaces in names, and which block labels
// are names.
type Naming struct {
	Replace string `yaml:"replace"`
	With    string `yaml:"with"`
	// Labels gives, by block type, the index of the label that names the
	// block (the second label of a resource; the first of a variable), which
	// is where Terraform puts the name. A type left out keeps its names, and
	// so do the references to them in every file of the directory; without
	// "variable", so do .tfvars assignments and module call arguments.
	// Without "dynamic", a dynamic block's iterator keeps its name.
	//
	// The names read through an iterator (each.value.<key>, a dynamic
	// block's <iterator>.value.<key>, a for-expression variable's <v>.<key>)
	// are not traced to what declares them: they are renamed, inside items
	// under ignore_naming too, unless some ignore_naming marker in the
	// directory keeps a name like them (a key of a marked variable, .tfvars
	// assignment or locals entry, or a name inside a marked item).
	//
	// Without "output", a read of a module call's output keeps the output's
	// name (module.<name>.<output>, after an index or [*] too), and the keys
	// read after it are renamed, as the child renames its output's value.
	// Reads that do not start at module.<name> are renamed in full, because
	// the formatter cannot tell they read an output, and then no longer
	// match it: through a for-expression variable ([for m in module.x :
	// m.out]), a function's result (values(module.x)[0].out) or remote
	// state (outputs.<name>).
	//
	// "provider" is left out by default: provider local names keep their
	// dashes wherever they appear (required_providers and everything in it,
	// provider block labels, the provider and providers meta-arguments),
	// because Terraform rejects "_" in them (google_beta fails terraform
	// init). With "provider" here, they are renamed in all those places.
	Labels map[string]int `yaml:"labels"`
}

// Priority orders a body: prepended blocks first, then prepended attributes
// (and blocks of those names), then other attributes A-Z, then other blocks,
// then appended items last.
type Priority struct {
	PrependedBlocks []Group `yaml:"prepended_blocks"`
	Prepended       []Group `yaml:"prepended"`
	// Appended runs from the end of the body backwards: its first group is
	// the body's last.
	Appended []Group `yaml:"appended"`
}

// Group is a set of names that sort together, in the order given, with the
// blank lines that separate the group from the items around it.
type Group struct {
	Names            []string `yaml:"names"`
	BlankLinesBefore int      `yaml:"blank_lines_before"`
	BlankLinesAfter  int      `yaml:"blank_lines_after"`
}

// Default returns the built-in configuration.
func Default() *Config {
	appended := []Group{
		{Names: []string{"depends_on"}, BlankLinesBefore: 1},
		{Names: []string{"tags"}, BlankLinesBefore: 1},
		{Names: []string{"lifecycle"}, BlankLinesBefore: 1},
	}
	meta := []Group{
		{Names: []string{"count"}, BlankLinesAfter: 1},
		{Names: []string{"for_each"}, BlankLinesAfter: 1},
		{Names: []string{"provider"}, BlankLinesAfter: 1},
	}
	topLevel := Priority{
		PrependedBlocks: []Group{
			{Names: []string{"terraform"}, BlankLinesAfter: 1},
			{Names: []string{"locals"}, BlankLinesAfter: 1},
		},
		Prepended: append([]Group{{Names: []string{"source", "version"}, BlankLinesAfter: 1}}, append(meta,
			Group{Names: []string{"providers"}, BlankLinesAfter: 1})...),
		Appended: appended,
	}
	return &Config{
		Rules: map[string]bool{
			RuleOrdering:   true,
			RuleObjectKeys: true,
			RuleObjects:    true,
			RuleNaming:     true,
			RuleComments:   true,
			RuleLocals:     true,
			RuleLists:      true,
		},
		SectionLabel:                    "terralint",
		IgnorePrefix:                    "ignore_",
		ListWidth:                       80,
		BlankLinesAroundMultiLine:       1,
		BlankLinesBetweenTopLevelBlocks: 1,
		BlankLinesBeforeFileEndComments: 1,
		Naming: Naming{
			Replace: "-",
			With:    "_",
			Labels:  map[string]int{"variable": 0, "output": 0, "module": 0, "dynamic": 0, "resource": 1, "data": 1, "ephemeral": 1},
		},
		TopLevel: topLevel,
		Priorities: map[string]Priority{
			"module":   topLevel,
			"resource": {Prepended: meta, Appended: appended},
			"variable": {Prepended: []Group{{Names: []string{"type"}}}},
			"data":     {Prepended: meta},
		},
	}
}

// On reports whether the rule is on.
func (c *Config) On(rule string) bool { return c.Rules[rule] }
