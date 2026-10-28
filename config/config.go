// Package config holds every rule switch and parameter of terralint. Default
// returns the built-in values; a config.yaml will override them.
package config

// Rule names: the keys of Config.Rules, and the words that follow "ignore_"
// in a section marker (# terralint ignore_naming {).
const (
	RuleOrdering   = "ordering"    // Body items in priority, then A-Z, order
	RuleObjectKeys = "object_keys" // Object keys A-Z
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
	// file.
	BlankLinesAroundMultiLine       int `yaml:"blank_lines_around_multi_line"`
	BlankLinesBetweenTopLevelBlocks int `yaml:"blank_lines_between_top_level_blocks"`

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
	// block (the second label of a resource; the first of a variable).
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
		Naming: Naming{
			Replace: "-",
			With:    "_",
			Labels:  map[string]int{"variable": 0, "output": 0, "module": 0, "provider": 0, "dynamic": 0, "resource": 1, "data": 1},
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
