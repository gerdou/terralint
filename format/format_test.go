package format_test

// Unit tests for what the corpus cannot prove: sections and markers, rule
// switches, naming exemptions across files, comment placement, file headers,
// and list and object layout. Every case is also formatted a second time,
// which must change nothing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/format"
)

// formatSource writes files (name -> content) to a new directory and
// formats the first one named, checking that the result is stable.
func formatSource(t *testing.T, cfg *config.Config, name string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, content := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, name)
	out, err := format.File(path, cfg)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := format.File(path, cfg)
	if err != nil {
		t.Fatalf("formatting the output again: %v\n%s", err, out)
	}
	if string(again) != string(out) {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", out, again)
	}
	return string(out)
}

func check(t *testing.T, cfg *config.Config, src, want string) {
	t.Helper()
	if cfg == nil {
		cfg = config.Default()
	}
	got := formatSource(t, cfg, "main.tf", map[string]string{"main.tf": src})
	if got != want {
		t.Errorf("source:\n%s\ngot:\n%s\nwant:\n%s", src, got, want)
	}
}

func TestSections(t *testing.T) {
	// The section stays right after the item before it (z), wherever z
	// sorts to; its items sort among themselves.
	check(t, nil, `resource "a" "z" {}

# about the group
# terralint group {
resource "a" "c" {}
resource "a" "b" {}
# }

resource "a" "a" {}
`, `resource "a" "a" {}

resource "a" "z" {}

# about the group
# terralint group {
resource "a" "b" {}

resource "a" "c" {}
# }
`)

	// Nothing before it: the section starts the body. An unclosed section
	// runs to the end of its body, so it stays there.
	check(t, nil, `locals {
  # terralint first {
  z = 1
  y = 2
  # }
  b = 1
  a = 2
  # terralint rest {
  q = 1
  p = 2
}
`, `locals {
  # terralint first {
  y = 2
  z = 1
  # }

  a = 2
  b = 1

  # terralint rest {
  p = 2
  q = 1
}
`)

	// Braces in commented-out code inside a section do not close it; the
	// section, with nothing before it, stays first.
	check(t, nil, `locals {
  # terralint keep {
  # x = {
  #   a = 1
  # }
  d = 1
  c = 1
  # }
  b = 1
}
`, `locals {
  # terralint keep {
  c = 1
  # x = {
  #   a = 1
  # }
  d = 1
  # }

  b = 1
}
`)
}

func TestIgnoreWords(t *testing.T) {
	// ignore_ordering keeps the section's order, and the order inside it.
	check(t, nil, `locals {
  b = 1
  # terralint ignore_ordering {
  z = {
    y = 1
    x = 2
  }
  a = 1
  # }
}
`, `locals {
  b = 1

  # terralint ignore_ordering {
  z = {
    x = 2
    y = 1
  }

  a = 1
  # }
}
`)

	// ignore_object_keys and ignore_naming on one item.
	check(t, nil, `locals {
  # terralint ignore_object_keys ignore_naming
  my-map = {
    b-x = 1
    a-x = 2
  }
  other-map = {
    b-x = 1
    a-x = 2
  }
}
`, `locals {
  # terralint ignore_object_keys ignore_naming
  my-map = {
    b-x = 1
    a-x = 2
  }

  other_map = {
    a_x = 2
    b_x = 1
  }
}
`)

	// ignore_lists keeps a list's layout; ignore_comments keeps /* */.
	check(t, nil, `locals {
  # terralint ignore_lists ignore_comments {
  a = [
    1,
    2,
  ]
  /* kept
     as is */
  b = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
  # }
  c = [
    1,
    2,
  ]
}
`, `locals {
  # terralint ignore_lists ignore_comments {
  a = [
    1,
    2,
  ]

  /* kept
     as is */
  b = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
  # }

  c = [1, 2]
}
`)

	// ignore_locals keeps a locals block apart; the others merge.
	check(t, nil, `locals {
  b = 1
}

# terralint ignore_locals
locals {
  c = 1
}

locals {
  a = 1
}
`, `locals {
  a = 1
  b = 1
}

# terralint ignore_locals
locals {
  c = 1
}
`)
}

func TestNamingExemptions(t *testing.T) {
	files := map[string]string{
		"main.tf": `output "o-1" {
  value = [var.my-var, var.other-var, local.my-local, module.my-mod.out-x, aws_s3_bucket.my-b.id]
}
`,
		"vars.tf": `# terralint ignore_naming
variable "my-var" {}

variable "other-var" {}

locals {
  # terralint ignore_naming
  my-local = 1
}

# terralint ignore_naming {
module "my-mod" {
  source = "./m"
}

resource "aws_s3_bucket" "my-b" {}
# }
`,
	}
	got := formatSource(t, config.Default(), "main.tf", files)
	want := `output "o_1" {
  value = [
    var.my-var,
    var.other_var,
    local.my-local,
    module.my-mod.out_x,
    aws_s3_bucket.my-b.id,
  ]
}
`
	if got != want {
		t.Errorf("main.tf:\ngot:\n%s\nwant:\n%s", got, want)
	}
	got = formatSource(t, config.Default(), "vars.tf", files)
	if !strings.Contains(got, `variable "my-var" {}`) || !strings.Contains(got, `variable "other_var" {}`) ||
		!strings.Contains(got, "my-local = 1") || !strings.Contains(got, `module "my-mod"`) {
		t.Errorf("vars.tf: exempt declarations renamed or others kept:\n%s", got)
	}
}

func TestNaming(t *testing.T) {
	check(t, nil, `provider "google-beta" {}

resource "aws_x" "my-y" {
  provider = google-beta
  for_each = { for my-k, my-v in var.my-m : my-k => my-v... }
  name     = "${var.a-b}-c-d"
  key      = local.m["my-key"]
  quoted = {
    "Cost-Center" = 1
    my-key        = 2
  }
  dynamic "my-block" {
    for_each = var.x
    content {
      a = my-block.value
    }
  }
}
`, `provider "google-beta" {}

resource "aws_x" "my_y" {
  for_each = { for my_k, my_v in var.my_m : my_k => my_v... }

  provider = google-beta

  key  = local.m["my-key"]
  name = "${var.a_b}-c-d"

  quoted = {
    "Cost-Center" = 1
    my_key        = 2
  }

  dynamic "my_block" {
    for_each = var.x

    content {
      a = my_block.value
    }
  }
}
`)
}

func TestCommentPlacement(t *testing.T) {
	check(t, nil, `// header line one
/* header
   line two */

resource "a" "z" {
  x = 1
  // after the last item
}

resource "a" "b" { # inline
  list = [
    # before one
    1, # after one
    2
    # after the last element
  ]
  call = f(
    a, # after a
    b
    // end of the arguments
  )
  obj = {
    a = 1
    # after the last item
  }
  cond = (
    var.a # after the condition
    ? 1
    : 2
  )
  for = [
    for x in var.y : x # after x
  ]
  str = "a-${f(
    x, # in an interpolation
  )}"
  bin = (
    var.a + # after a
    var.b
  )
} # after the closing brace
# end of the file
`, `# header line one
# header
# line two

resource "a" "b" { # inline
  bin = (
    var.a # after a
    + var.b
  )

  call = f(
    a, # after a
    b,
    # end of the arguments
  )

  cond = (
    var.a # after the condition
    ? 1 : 2
  )

  for = [
    for x in var.y : x # after x
  ]

  list = [
    # before one
    1, # after one
    2,
    # after the last element
  ]

  obj = {
    a = 1
    # after the last item
  }

  str = "a-${f(
    x, # in an interpolation
  )}"
} # after the closing brace

resource "a" "z" {
  x = 1
  # after the last item
}

# end of the file
`)

	// A comment that cannot stay inside a value goes above its item; the
	// first one on the item's line is its inline comment.
	check(t, nil, `locals {
  a = var.x /* one */ + /* two */ var.y
}
`, `locals {
  # two
  a = var.x + var.y # one
}
`)

	// The file header stays first when the item under it moves.
	check(t, nil, `# Copyright

# about z
resource "a" "z" {}

terraform {}
`, `# Copyright

terraform {}

# about z
resource "a" "z" {}
`)
}

func TestLayout(t *testing.T) {
	check(t, nil, `locals {
  short = [
    "a", "b",
  ]
  long = ["aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccc", "dddddd"]
  fits = ["aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccc", "dddd"]
  objects = [{ a = 1 }, {}]
  empty = {}
  one = { a = 1 }
}
`, `locals {
  empty = {}
  fits  = ["aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccc", "dddd"]

  long = [
    "aaaaaaaaaaaaaaaaaaaa",
    "bbbbbbbbbbbbbbbbbbbb",
    "cccccccccccccccccccc",
    "dddddd",
  ]

  objects = [
    {
      a = 1
    },
    {},
  ]

  one = {
    a = 1
  }

  short = ["a", "b"]
}
`)

	// Blank lines: prepended and appended groups, multi-line items.
	check(t, nil, `module "m" {
  b = 1
  depends_on = [x]
  version = "1"
  a = 1
  source = "./m"
  count = 1
  tags = {}
}
`, `module "m" {
  source  = "./m"
  version = "1"

  count = 1

  a = 1
  b = 1

  tags = {}

  depends_on = [x]
}
`)
}

func TestTemplates(t *testing.T) {
	check(t, nil, `locals {
  a = "x\u0041 $${not} %%{not} ${ var.b ~} y %{ if c ~} z %{~ else } w %{ endif }"
  b = <<-EOT
    hello ${var.my-name}
      %{~ for k, v in var.m ~}
      ${k}
      %{ endfor }
  EOT
  c = 1.50
  d = "${var.d}"
  e = ["${var.e}"]
}

variable "v" {
  type = "map"
}
`, `locals {
  a = "x\u0041 $${not} %%{not} ${var.b~} y %{if c~} z %{~else} w %{endif}"

  b = <<-EOT
    hello ${var.my_name}
      %{~for k, v in var.m~}
      ${k}
      %{endfor}
  EOT

  c = 1.50
  d = var.d
  e = ["${var.e}"]
}

variable "v" {
  type = map(string)
}
`)
}

func TestConfigSwitches(t *testing.T) {
	cfg := config.Default()
	for rule := range cfg.Rules {
		cfg.Rules[rule] = false
	}
	src := `locals {
  b-x = [
    1,
  ]

  a = {
    z = 1
    y = 2
  }
}

locals {
  c = 1
}
`
	check(t, cfg, src, src)

	cfg = config.Default()
	cfg.ListWidth = 10
	cfg.SectionLabel = "lint"
	check(t, cfg, `locals {
  b = [1, 2, 3, 4]
  # lint ignore_ordering {
  z = 1
  y = 1
  # }
}
`, `locals {
  b = [
    1,
    2,
    3,
    4,
  ]

  # lint ignore_ordering {
  z = 1
  y = 1
  # }
}
`)
}

// Fixes from review round 1, each checked for stability by check.
func TestRound1Comments(t *testing.T) {
	// A comment on a line that opens an item and starts one inside it is
	// printed once, with the inner item (R1a-001, R1c-003).
	check(t, nil, `locals {
  tags = { Name = "x" # name tag
    Env = "y"
  }
  o = { source = "x", /* pin */ version = "2.1.0" }
}

resource "a" "b" {
  allow { protocol = "icmp" /* ping */ }
}
`, `locals {
  o = {
    source  = "x" # pin
    version = "2.1.0"
  }

  tags = {
    Env  = "y"
    Name = "x" # name tag
  }
}

resource "a" "b" {
  allow {
    protocol = "icmp" # ping
  }
}
`)

	// # comments are kept as written; // and /* */ become # (R1a-004, R1c-004).
	check(t, nil, `###############
### Network ###
#no space
# /*.json files
#   * bullet
#  */
locals {
  // slash
  /* block
     * star */
  a = 1 #tight
}
`, `###############
### Network ###
#no space
# /*.json files
#   * bullet
#  */
locals {
  # slash
  # block
  # star
  a = 1 #tight
}
`)

	// Comments on the operands of a chain or conditional stay with them, in
	// order (R1c-001); a comment inside an argument or element makes the
	// call or list one per line (R1c-002).
	check(t, nil, `locals {
  all = (
    var.a || # first
    var.b || # second
    var.c    # third
  )
  x = (
    var.a != null # set explicitly
    ? var.a       # use it
    : "default"   # fallback
  )
  name = format("%s-%s", var.prefix,
    var.suffix != null # suffix set by caller
    ? var.suffix : "default")
  y = [
    var.a,
    var.enabled # only when enabled
    ? var.b : null,
  ]
}
`, `locals {
  all = (
    var.a    # first
    || var.b # second
    || var.c # third
  )

  name = format(
    "%s-%s",
    var.prefix,
    var.suffix != null # suffix set by caller
    ? var.suffix : "default",
  )

  x = (
    var.a != null # set explicitly
    ? var.a       # use it
    : "default"   # fallback
  )

  y = [
    var.a,
    var.enabled # only when enabled
    ? var.b : null,
  ]
}
`)
}

func TestRound1ElementEnd(t *testing.T) {
	// A comment held inside the last part of an element ends up after its
	// comma, where the next run reads it as the element's own (R1c-002).
	check(t, nil, `locals {
  a = [
    var.a ? local.p : local /* first */ .x,
    var.b ? local.q : local /* second */ .y,
    3,
  ]
}
`, `locals {
  a = [
    var.a ? local.p : local.x, # first
    var.b ? local.q : local.y, # second
    3,
  ]
}
`)
}

func TestRound1Templates(t *testing.T) {
	// An indented heredoc may start with a directive (R1a-002).
	check(t, nil, `locals {
  c = <<-EOT
    %{ for ip in var.ips ~}
    server ${ip}
    %{ endfor ~}
  EOT
}
`, `locals {
  c = <<-EOT
    %{for ip in var.ips~}
    server ${ip}
    %{endfor~}
  EOT
}
`)
}

func TestRound1Naming(t *testing.T) {
	// In an exempt item, references to names declared elsewhere are renamed;
	// its own names, for-expression variables and kept dynamic iterators are
	// not (R1b-001).
	check(t, nil, `variable "bucket-name" {}

locals {
  common-tags = { team = "x" }
}

# terralint ignore_naming
resource "aws_s3_bucket" "my-bucket" {
  bucket = var.bucket-name
  tags   = { my-key = local.common-tags }
  names  = [for my-x in var.bucket-name : my-x]
  dynamic "my-rule" {
    for_each = var.bucket-name
    iterator = my-it
    content {
      id = my-it.value
    }
  }
}
`, `locals {
  common_tags = {
    team = "x"
  }
}

# terralint ignore_naming
resource "aws_s3_bucket" "my-bucket" {
  bucket = var.bucket_name
  names  = [for my-x in var.bucket_name : my-x]

  dynamic "my-rule" {
    for_each = var.bucket_name
    iterator = my-it

    content {
      id = my-it.value
    }
  }

  tags = {
    my-key = local.common_tags
  }
}

variable "bucket_name" {}
`)

	// ephemeral labels are names, like data's (R1b-008).
	check(t, nil, `ephemeral "random_password" "db-pass" {}

output "o" {
  value = ephemeral.random_password.db-pass.result
}
`, `ephemeral "random_password" "db_pass" {}

output "o" {
  value = ephemeral.random_password.db_pass.result
}
`)

	// Exemptions reach references through dynamic indexes (R1b-005) and
	// variable assignments in .tfvars files (R1b-004).
	files := map[string]string{
		"vars.tf": `# terralint ignore_naming
variable "instance-type" {}

# terralint ignore_naming
locals {
  my-map = {
    a = {
      sub-key = "x"
    }
  }
}
`,
		"main.tf": `output "o" {
  value = [local.my-map[var.k].sub-key, local.my-map[*].sub-key]
}
`,
		"terraform.tfvars": `instance-type = "t3.micro"
other-type    = "t3.small"
`,
	}
	if got, want := formatSource(t, config.Default(), "main.tf", files), `output "o" {
  value = [local.my-map[var.k].sub-key, local.my-map[*].sub-key]
}
`; got != want {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, want)
	}
	if got, want := formatSource(t, config.Default(), "terraform.tfvars", files), `instance-type = "t3.micro"
other_type    = "t3.small"
`; got != want {
		t.Errorf("terraform.tfvars:\n%s\nwant:\n%s", got, want)
	}
}

func TestRound1Order(t *testing.T) {
	// Top-level blocks in a priority group sort by labels too (R1b-006).
	check(t, nil, `provider "google" {}

provider "aws" {}

provider "azurerm" {}
`, `provider "aws" {}

provider "azurerm" {}

provider "google" {}
`)
}

func TestRound1Sections(t *testing.T) {
	// A marker in the comments at the top of the file, with a blank line
	// under it, is not part of the file header (R1b-002).
	check(t, nil, `# Copyright

# Keep these dashed
# terralint ignore_naming {

resource "a" "logs-bucket" {}
# }

resource "a" "new-bucket" {}
`, `# Copyright

# Keep these dashed
# terralint ignore_naming {
resource "a" "logs-bucket" {}
# }

resource "a" "new_bucket" {}
`)

	// Locals blocks merge only with blocks under the same section and
	// markers, so that their items keep their rule switches (R1b-003).
	check(t, nil, `locals {
  a-b = 1
}

# terralint ignore_naming {
locals {
  c-d = 2
}
# }

# terralint ignore_ordering
locals {
  y = 2
  c = 2
}

locals {
  e = local.c-d
}
`, `locals {
  a_b = 1
  e   = local.c-d
}

# terralint ignore_naming {
locals {
  c-d = 2
}
# }

# terralint ignore_ordering
locals {
  y = 2
  c = 2
}
`)
}

func TestRound1Config(t *testing.T) {
	// The blank lines before a file's end comments come from config
	// (R1b-009, R1c-007).
	cfg := config.Default()
	cfg.BlankLinesBetweenTopLevelBlocks, cfg.BlankLinesAroundMultiLine, cfg.BlankLinesBeforeFileEndComments = 0, 0, 0
	check(t, cfg, `resource "a" "x" {}
resource "a" "y" {}

# end of file
`, `resource "a" "x" {}
resource "a" "y" {}
# end of file
`)
}

func TestRound2Naming(t *testing.T) {
	// ignore_naming on a module call keeps the call's name; its arguments
	// and the outputs read from it follow the child module, whose variables
	// and outputs are renamed. A marker on an argument keeps that one
	// (R2b-002).
	check(t, nil, `# terralint ignore_naming
module "vpc-main" {
  source     = "./modules/vpc"
  cidr-block = "10.0.0.0/16"
  # terralint ignore_naming
  dashed-arg = { my-key = 1 }
}

output "o" {
  value = [module.vpc-main.vpc-id, module.vpc-main["a"].vpc-id, [for m in module.vpc-main : m.vpc-id]]
}
`, `# terralint ignore_naming
module "vpc-main" {
  source = "./modules/vpc"

  cidr_block = "10.0.0.0/16"

  # terralint ignore_naming
  dashed-arg = {
    my-key = 1
  }
}

output "o" {
  value = [
    module.vpc-main.vpc_id,
    module.vpc-main["a"].vpc_id,
    [for m in module.vpc-main : m.vpc_id],
  ]
}
`)

	// ignore_naming on a .tfvars assignment exempts the variable it sets,
	// in every file of the directory (R2b-004).
	files := map[string]string{
		"main.tf": `output "o" {
  value = var.instance-type
}

variable "instance-type" {}
`,
		"terraform.tfvars": `# terralint ignore_naming
instance-type = "t3.micro"
`,
	}
	if got := formatSource(t, config.Default(), "main.tf", files); got != files["main.tf"] {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, files["main.tf"])
	}
	if got := formatSource(t, config.Default(), "terraform.tfvars", files); got != files["terraform.tfvars"] {
		t.Errorf("terraform.tfvars:\n%s\nwant:\n%s", got, files["terraform.tfvars"])
	}

	// A block type missing from Naming.Labels keeps its names, and the
	// references to them keep theirs too (R2b-003).
	src := `provider "google-beta" {}

module "vpc-main" {
  source     = "./x"
  cidr-block = var.my-var
}

resource "google_x" "y" {
  provider = google-beta
  v        = [module.vpc-main.out-x, var.my-var.sub-key]
}

variable "my-var" {}
`
	for _, tt := range []struct{ drop, want string }{
		{"provider", `provider "google-beta" {}

module "vpc_main" {
  source = "./x"

  cidr_block = var.my_var
}

resource "google_x" "y" {
  provider = google-beta

  v = [module.vpc_main.out_x, var.my_var.sub_key]
}

variable "my_var" {}
`},
		{"module", `provider "google-beta" {}

module "vpc-main" {
  source = "./x"

  cidr_block = var.my_var
}

resource "google_x" "y" {
  provider = google-beta

  v = [module.vpc-main.out_x, var.my_var.sub_key]
}

variable "my_var" {}
`},
		{"variable", `provider "google-beta" {}

module "vpc_main" {
  source = "./x"

  cidr-block = var.my-var
}

resource "google_x" "y" {
  provider = google-beta

  v = [module.vpc_main.out_x, var.my-var.sub_key]
}

variable "my-var" {}
`},
		{"output", `provider "google-beta" {}

module "vpc_main" {
  source = "./x"

  cidr_block = var.my_var
}

resource "google_x" "y" {
  provider = google-beta

  v = [module.vpc_main.out-x, var.my_var.sub_key]
}

variable "my_var" {}
`},
	} {
		cfg := config.Default()
		delete(cfg.Naming.Labels, tt.drop)
		if got := formatSource(t, cfg, "main.tf", map[string]string{"main.tf": src}); got != tt.want {
			t.Errorf("without %s labels:\n%s\nwant:\n%s", tt.drop, got, tt.want)
		}
	}
	cfg := config.Default()
	delete(cfg.Naming.Labels, "dynamic")
	check(t, cfg, `resource "a" "b" {
  dynamic "my-rule" {
    for_each = var.rules
    content {
      id = my-rule.value
    }
  }
}
`, `resource "a" "b" {
  dynamic "my-rule" {
    for_each = var.rules

    content {
      id = my-rule.value
    }
  }
}
`)
	cfg = config.Default()
	delete(cfg.Naming.Labels, "variable")
	if got, want := formatSource(t, cfg, "a.tfvars", map[string]string{"a.tfvars": "my-var = { my-key = 1 }\n"}), "my-var = {\n  my_key = 1\n}\n"; got != want {
		t.Errorf("without variable labels, a.tfvars:\n%s\nwant:\n%s", got, want)
	}
}

func TestRound2Layout(t *testing.T) {
	// Inside a template interpolation, objects and lists print on one line,
	// whatever their width, so terraform fmt keeps the lines after it
	// indented; one that holds a comment still spans lines (R1a-005, D10).
	check(t, nil, `resource "aws_instance" "web" {
  ami       = "ami-123"
  user_data = <<-EOT
    echo '${jsonencode({
      port = 80
      long = ["aaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccc"]
    })}'
  EOT
  name      = "${jsonencode({ b = 1 /* why */ })}-x"
  tags = {
    Name = "web"
  }
}
`, `resource "aws_instance" "web" {
  ami = "ami-123"

  name = "${jsonencode({
    b = 1 # why
  })}-x"

  user_data = <<-EOT
    echo '${jsonencode({ long = ["aaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccc"], port = 80 })}'
  EOT

  tags = {
    Name = "web"
  }
}
`)

	// With the objects rule off, for the file or for an item, an object
	// keeps the shape it was written in (R2b-005).
	src := `locals {
  a = { Name = "web", Env = "prod" }
  b = {
    x = 1
  }
}
`
	cfg := config.Default()
	cfg.Rules[config.RuleObjects] = false
	check(t, cfg, src, `locals {
  a = { Env = "prod", Name = "web" }

  b = {
    x = 1
  }
}
`)
	check(t, nil, `locals {
  # terralint ignore_objects ignore_object_keys
  a = { Name = "web", Env = "prod" }
  b = { x = 1 }
}
`, `locals {
  # terralint ignore_objects ignore_object_keys
  a = { Name = "web", Env = "prod" }

  b = {
    x = 1
  }
}
`)
}

func TestRound2Comments(t *testing.T) {
	// A comment on the opening brace line of an object that does not start
	// an item belongs to the object, and stays on that line when its keys
	// sort (R2a-003, R2c-002).
	check(t, nil, `locals {
  rules = [
    { # HTTPS from anywhere
      rule = "https-443-tcp"
      cidr = "0.0.0.0/0"
    },
  ]
  all = concat(
    var.extra,
    [
      { /* allow HTTPS */
        port = 443
        cidr = "0.0.0.0/0"
      },
    ],
  )
  by_key = {
    for k, v in var.m : k => { # obj
      z = v
      a = v
    }
  }
  two = [
    { /* one */ /* two */
      z = 1
      a = 2
    },
  ]
}
`, `locals {
  all = concat(
    var.extra,
    [
      { # allow HTTPS
        cidr = "0.0.0.0/0"
        port = 443
      },
    ],
  )

  by_key = {
    for k, v in var.m : k => { # obj
      a = v
      z = v
    }
  }

  rules = [
    { # HTTPS from anywhere
      cidr = "0.0.0.0/0"
      rule = "https-443-tcp"
    },
  ]

  two = [
    { # one
      # two
      a = 2
      z = 1
    },
  ]
}
`)
}

func TestRound2Templates(t *testing.T) {
	// A line break or a comment may come between %{ and the directive's
	// keyword (R2a-002).
	check(t, nil, `locals {
  a = <<-EOT
    %{~
      if length(var.extra) > 0
    ~}
    extra
    %{~ endif ~}
  EOT
  b = "%{
  if var.x }yes%{ else }no%{ endif }"
  c = <<EOT
%{ for x in var.xs
}${x}
%{
endfor }
EOT
}
`, `locals {
  a = <<-EOT
    %{~if length(var.extra) > 0~}
    extra
    %{~endif~}
  EOT

  b = "%{if var.x}yes%{else}no%{endif}"

  c = <<EOT
%{for x in var.xs}${x}
%{endfor}
EOT
}
`)
	got := formatSource(t, config.Default(), "main.tf", map[string]string{"main.tf": `locals {
  a = "%{ /* c */ if var.x }yes%{ else }no%{ endif }"
}
`})
	if !strings.Contains(got, "%{if var.x}yes%{else}no%{endif}") || !strings.Contains(got, "# c") {
		t.Errorf("comment before a directive keyword:\n%s", got)
	}
}

func TestRound3Comments(t *testing.T) {
	// A comment after the first element of a list or call value starts, on
	// the item's first line, stays with that element when the elements go
	// on lines of their own (R3a-001).
	check(t, nil, `locals {
  cidrs = ["10.0.0.0/8", # corporate network
    "192.168.0.0/16",    # VPN users
  ]
  subnets = [{ # public
    name = "a"
    cidr = "10.0.0.0/24"
    }, { # private
    name = "b"
    cidr = "10.0.1.0/24"
  }]
  tags = merge(local.common, { # per-bucket
    Name = "logs"
  }, var.extra)
  boxed = merge(var.tags, (/* box */ (var.on ? var.a : {})))
  rules = {
    web = [{ # HTTPS
      port = 443
    }]
  }
  open  = [ # the whole list
    "a",
  ]
  after = ["a", "b"] # after the list
}
`, `locals {
  after = ["a", "b"] # after the list

  boxed = merge(
    var.tags,
    (
      # box
      (var.on ? var.a : {})
    ),
  )

  cidrs = [
    "10.0.0.0/8",     # corporate network
    "192.168.0.0/16", # VPN users
  ]

  open = ["a"] # the whole list

  rules = {
    web = [
      { # HTTPS
        port = 443
      },
    ]
  }

  subnets = [
    { # public
      cidr = "10.0.0.0/24"
      name = "a"
    },
    { # private
      cidr = "10.0.1.0/24"
      name = "b"
    },
  ]

  tags = merge(
    local.common,
    { # per-bucket
      Name = "logs"
    },
    var.extra,
  )
}
`)
}

func TestRound3Naming(t *testing.T) {
	// Provider local names keep their dashes everywhere they appear, because
	// Terraform rejects "_" in them (D12; R3b-001, R3b-002).
	src := `terraform {
  required_providers {
    google-beta = {
      source                = "hashicorp/google-beta"
      configuration_aliases = [google-beta.us-east]
    }
  }
}

provider "google-beta" {
  alias      = "us-east"
  my-setting = var.my-var
}

module "my-mod" {
  source = "./m"
  providers = {
    google-beta = google-beta.us-east
  }
}

resource "google_x" "my-y" {
  provider = google-beta.us-east
  name     = provider::google-beta::f(var.my-var)
}

import {
  provider = google-beta
  to       = google_x.my-y
  id       = "x"
}
`
	check(t, nil, src, `terraform {
  required_providers {
    google-beta = {
      configuration_aliases = [google-beta.us-east]
      source                = "hashicorp/google-beta"
    }
  }
}

provider "google-beta" {
  alias      = "us-east"
  my_setting = var.my_var
}

import {
  id       = "x"
  provider = google-beta
  to       = google_x.my_y
}

module "my_mod" {
  source = "./m"

  providers = {
    google-beta = google-beta.us-east
  }
}

resource "google_x" "my_y" {
  provider = google-beta.us-east

  name = provider::google-beta::f(var.my_var)
}
`)
	// With "provider" in Naming.Labels, they are renamed in all those places.
	cfg := config.Default()
	cfg.Naming.Labels["provider"] = 0
	got := formatSource(t, cfg, "main.tf", map[string]string{"main.tf": src})
	for _, want := range []string{"google_beta = {", "[google_beta.us_east]", `provider "google_beta"`, "google_beta = google_beta.us_east", "provider = google_beta.us_east", "provider = google_beta\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("with provider labels, no %q in:\n%s", want, got)
		}
	}

	// ignore_naming on a .tfvars assignment keeps the names in the variable's
	// declaration too, so its type still matches the value and the
	// references (R3b-004).
	files := map[string]string{
		"main.tf": `variable "db-config" {
  type = object({
    instance-class = string
  })
  default = {
    instance-class = "db.t3.micro"
  }
}

output "o" {
  value = var.db-config.instance-class
}
`,
		"terraform.tfvars": `# terralint ignore_naming
db-config = {
  instance-class = "db.t3.small"
}
`,
	}
	want := `output "o" {
  value = var.db-config.instance-class
}

variable "db-config" {
  type = object({
    instance-class = string
  })

  default = {
    instance-class = "db.t3.micro"
  }
}
`
	if got := formatSource(t, config.Default(), "main.tf", files); got != want {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, want)
	}

	// Without "output" in Naming.Labels, a read of a module call's output
	// keeps the output's name, and the keys after it are renamed, as in the
	// child's output value (R3b-005).
	cfg = config.Default()
	delete(cfg.Naming.Labels, "output")
	check(t, cfg, `output "o-1" {
  value = [
    module.net.vpc-id,
    module.net["a"].vpc-id,
    module.net[each.key].subnet-ids.private-a,
    module.net["a"].subnet-ids.private-a,
    module.net.subnet-ids.private-a,
    module.net[*].subnet-ids.private-a,
    module.net["a"].subnet-ids[0].private-a,
  ]
}

output "subnet-ids" {
  value = {
    private-a = "x"
  }
}
`, `output "o-1" {
  value = [
    module.net.vpc-id,
    module.net["a"].vpc-id,
    module.net[each.key].subnet-ids.private_a,
    module.net["a"].subnet-ids.private_a,
    module.net.subnet-ids.private_a,
    module.net[*].subnet-ids.private_a,
    module.net["a"].subnet-ids[0].private_a,
  ]
}

output "subnet-ids" {
  value = {
    private_a = "x"
  }
}
`)
}

func TestRound4Comments(t *testing.T) {
	// A comment keeps its owner when the output is parsed again (D13), so
	// formatting twice changes nothing (check). An item's own comment ends
	// the item's first line only where no argument or element has started
	// and no object other than the value opens: the call it would follow
	// goes one argument per line, the for expression around such an object
	// breaks after its bracket, and otherwise the comment ends the value's
	// last line (R4a-001, R4c-001). A comment after an element's start
	// belongs to that element wherever its list sits (R4a-002).
	check(t, nil, `variable "person" {
  type = object({ name = string, age = number }) # the person to greet
}

resource "aws_instance" "web" {
  tags      = merge(local.common_tags, { Name = "web" }) # web server tags
  user_data = templatefile("${path.module}/init.sh", { port = 8080 }) # bootstrap script
  zones     = toset(["us-east-1a", "us-east-1b", "us-east-1c", "us-east-1d", "us-east-1e", "us-east-1f"]) # all zones
  extra = merge( # written broken
    var.x,
    { a = 1 },
  )
  ebs = map(/* nested */ object({
    size = number
  }))
  cidr_blocks = var.office ? ["10.0.0.0/8", # corporate network
    "192.168.0.0/16",                       # VPN users
  ] : []
  rules = var.enabled ? [{ # HTTPS from anywhere
    port = 443
    }, { # SSH
    port = 22
  }] : []
  grouped = (["a", # first
  "b"])
  picked = [["a", # first
  "b"]][0]
  open     = var.on ? { # the object
    a = 1
  } : {}
  either   = var.on ? { a = 1 } : {} # the whole value
  subnets  = [for s in var.subnets : { id = s }] # one per subnet
  members  = distinct(concat(var.members, var.on ? [] : var /* tail */ .more))
}
`, `resource "aws_instance" "web" {
  cidr_blocks = var.office ? [
    "10.0.0.0/8",     # corporate network
    "192.168.0.0/16", # VPN users
  ] : []

  ebs = map( # nested
    object({
      size = number
    }),
  )

  either = var.on ? {
    a = 1
  } : {} # the whole value

  extra = merge( # written broken
    var.x,
    {
      a = 1
    },
  )

  grouped = ([
    "a", # first
    "b",
  ])

  members = distinct(concat(
    var.members,
    var.on ? [] : var.more, # tail
  ))

  open = var.on ? { # the object
    a = 1
  } : {}

  picked = [
    [
      "a", # first
      "b",
    ],
  ][0]

  rules = var.enabled ? [
    { # HTTPS from anywhere
      port = 443
    },
    { # SSH
      port = 22
    },
  ] : []

  subnets = [ # one per subnet
    for s in var.subnets : {
      id = s
    }
  ]

  user_data = templatefile( # bootstrap script
    "${path.module}/init.sh",
    {
      port = 8080
    },
  )

  zones = toset( # all zones
    [
      "us-east-1a",
      "us-east-1b",
      "us-east-1c",
      "us-east-1d",
      "us-east-1e",
      "us-east-1f",
    ],
  )

  tags = merge( # web server tags
    local.common_tags,
    {
      Name = "web"
    },
  )
}

variable "person" {
  type = object( # the person to greet
    {
      age  = number
      name = string
    },
  )
}
`)
	// The same for an object item, here in a .tfvars file.
	files := map[string]string{"terraform.tfvars": `sites = {
  web = merge(local.defaults, { port = 80 }) # public site
}
policy = jsonencode({ Version = "2012-10-17" }) # minimal policy
`}
	want := `policy = jsonencode( # minimal policy
  {
    Version = "2012-10-17"
  },
)

sites = {
  web = merge( # public site
    local.defaults,
    {
      port = 80
    },
  )
}
`
	if got := formatSource(t, config.Default(), "terraform.tfvars", files); got != want {
		t.Errorf("terraform.tfvars:\n%s\nwant:\n%s", got, want)
	}
}

func TestRound4Naming(t *testing.T) {
	// A name read through each.value, a dynamic block's iterator or a
	// for-expression variable keeps its dashes exactly when an ignore_naming
	// marker in the directory keeps a key of that name (D14): here the
	// marked variable's keys (R4b-001).
	check(t, nil, `# terralint ignore_naming
variable "ingress-rules" {
  type = list(object({
    from-port = number
  }))
}

resource "aws_security_group" "api" {
  dynamic "ingress" {
    for_each = var.ingress-rules
    content {
      from_port = ingress.value.from-port
    }
  }
}

resource "terraform_data" "each" {
  for_each = { for i, r in var.ingress-rules : i => r }
  input    = each.value.from-port
}

output "ports" {
  value = [for r in var.ingress-rules : r.from-port]
}
`, `output "ports" {
  value = [for r in var.ingress-rules : r.from-port]
}

resource "aws_security_group" "api" {
  dynamic "ingress" {
    for_each = var.ingress-rules

    content {
      from_port = ingress.value.from-port
    }
  }
}

resource "terraform_data" "each" {
  for_each = { for i, r in var.ingress-rules : i => r }

  input = each.value.from-port
}

# terralint ignore_naming
variable "ingress-rules" {
  type = list(object({
    from-port = number
  }))
}
`)
	// A marked locals entry and a marked .tfvars assignment keep their keys
	// for such reads too.
	files := map[string]string{
		"main.tf": `locals {
  # terralint ignore_naming
  subnet-map = { us-east-1a = { cidr-block = "10.0.1.0/24" } }
}

resource "aws_subnet" "s" {
  for_each   = local.subnet-map
  cidr_block = each.value.cidr-block
  name       = each.value.zone-name
}

output "ports" {
  value = [for r in var.rules : r.to-port]
}
`,
		"terraform.tfvars": `# terralint ignore_naming
rules = [{ to-port = 443 }]
`,
	}
	want := `locals {
  # terralint ignore_naming
  subnet-map = {
    us-east-1a = {
      cidr-block = "10.0.1.0/24"
    }
  }
}

output "ports" {
  value = [for r in var.rules : r.to-port]
}

resource "aws_subnet" "s" {
  for_each = local.subnet-map

  cidr_block = each.value.cidr-block
  name       = each.value.zone_name
}
`
	if got := formatSource(t, config.Default(), "main.tf", files); got != want {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, want)
	}

	// Inside an item under ignore_naming, such reads of a renamed
	// variable's keys are renamed too, as they are when "dynamic" is left
	// out of Naming.Labels (R4b-002).
	src := `variable "sg-rules" {
  type = list(object({
    from-port = number
  }))
}

# terralint ignore_naming
resource "aws_security_group" "web-sg" {
  description = join(",", [for my-r in var.sg-rules : my-r.from-port])

  dynamic "my-ingress" {
    for_each = var.sg-rules

    content {
      from_port = my-ingress.value.from-port
    }
  }
}
`
	check(t, nil, src, `# terralint ignore_naming
resource "aws_security_group" "web-sg" {
  description = join(",", [for my-r in var.sg_rules : my-r.from_port])

  dynamic "my-ingress" {
    for_each = var.sg_rules

    content {
      from_port = my-ingress.value.from_port
    }
  }
}

variable "sg_rules" {
  type = list(object({
    from_port = number
  }))
}
`)
	cfg := config.Default()
	delete(cfg.Naming.Labels, "dynamic")
	check(t, cfg, strings.Replace(src, "# terralint ignore_naming\n", "", 1), `resource "aws_security_group" "web_sg" {
  description = join(",", [for my_r in var.sg_rules : my_r.from_port])

  dynamic "my-ingress" {
    for_each = var.sg_rules

    content {
      from_port = my-ingress.value.from_port
    }
  }
}

variable "sg_rules" {
  type = list(object({
    from_port = number
  }))
}
`)
}

func TestRound4Sections(t *testing.T) {
	// Nested blocks of one type keep their source order across a section
	// that holds some of them, whatever its anchor sorts to (D15, R4b-003):
	// the section follows the last block of that type before it, or else
	// goes right before the first one after it.
	check(t, nil, `resource "null_resource" "setup" {
  provisioner "local-exec" {
    command = "step-1"
  }

  connection {
    host = "x"
  }

  # terralint remote steps {
  provisioner "remote-exec" {
    inline = ["step-2"]
  }
  # }

  provisioner "local-exec" {
    command = "step-3"
  }
}

resource "aws_cloudfront_distribution" "cdn" {
  depends_on = [aws_s3_bucket.b]

  # terralint static assets {
  ordered_cache_behavior {
    path_pattern = "/static/*"
  }
  # }

  enabled = true

  ordered_cache_behavior {
    path_pattern = "/*"
  }
}
`, `resource "aws_cloudfront_distribution" "cdn" {
  enabled = true

  # terralint static assets {
  ordered_cache_behavior {
    path_pattern = "/static/*"
  }
  # }

  ordered_cache_behavior {
    path_pattern = "/*"
  }

  depends_on = [aws_s3_bucket.b]
}

resource "null_resource" "setup" {
  connection {
    host = "x"
  }

  provisioner "local-exec" {
    command = "step-1"
  }

  # terralint remote steps {
  provisioner "remote-exec" {
    inline = ["step-2"]
  }
  # }

  provisioner "local-exec" {
    command = "step-3"
  }
}
`)
}
