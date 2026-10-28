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
    module.my-mod.out-x,
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
`, `provider "google_beta" {}

resource "aws_x" "my_y" {
  for_each = { for my_k, my_v in var.my_m : my_k => my_v... }

  provider = google_beta

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
