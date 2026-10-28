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
    # after a
    var.a + var.b
  )

  call = f(
    a, # after a
    b,
    # end of the arguments
  )

  cond = (
    # after the condition
    var.a ? 1 : 2
  )

  for = [for x in var.y :
    # after x
    x
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
