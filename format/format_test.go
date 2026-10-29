package format_test

// Unit tests for what the corpus cannot prove: sections and markers, rule
// switches, naming exemptions across files, comment placement, file headers,
// and list and object layout. Every case is also formatted a second time,
// which must change nothing.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/format"
	"github.com/gerdou/terralint/parser"
)

// formatSource writes files (name -> content) to a new directory and
// formats the first one named, checking that the result is stable.
func formatSource(t *testing.T, cfg *config.Config, name string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, n)), 0o755); err != nil {
			t.Fatal(err)
		}
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
    hello ${var.my-name}
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
  d = local.b-x
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

func TestRound5Sections(t *testing.T) {
	// Several sections hold blocks of one type, none comes before them and
	// a free one follows: the first section goes right before that free
	// block, and the others follow it, whatever its anchor (origin, which
	// sorts after the type) sorts to (D15, R5b-001).
	check(t, nil, `resource "aws_cloudfront_distribution" "cdn" {
  enabled = true

  origin {
    origin_id = "api"
  }

  # terralint api routes {
  ordered_cache_behavior {
    path_pattern = "/api/*"
  }
  # }

  # terralint static assets {
  ordered_cache_behavior {
    path_pattern = "/static/*"
  }
  # }

  ordered_cache_behavior {
    path_pattern = "/*"
  }
}
`, `resource "aws_cloudfront_distribution" "cdn" {
  enabled = true

  # terralint api routes {
  ordered_cache_behavior {
    path_pattern = "/api/*"
  }
  # }

  # terralint static assets {
  ordered_cache_behavior {
    path_pattern = "/static/*"
  }
  # }

  ordered_cache_behavior {
    path_pattern = "/*"
  }

  origin {
    origin_id = "api"
  }
}
`)
}

func TestRound6Comments(t *testing.T) {
	// A comment after the opening bracket of a list or call that is an
	// argument or element is that list's or call's own (D13): it stays at
	// the end of the bracket's line, apart from the first element's own
	// comment (R6c-001). check also formats the output again.
	check(t, nil, `module "vpc" {
  source = "./vpc"
  azs    = toset([ # all zones in the region
    "us-east-1a",
    "us-east-1b",
  ])
  subnet_groups = [[ # public
    "10.0.1.0/24",
  ], [ # private
    "10.0.3.0/24",
  ]]
  cidr_blocks = concat([ # private ranges
    # corporate VPN
    "10.0.0.0/8",
  ], var.extra_cidrs)
  defaults = var.c ? coalescelist(var.list, [ # defaults
    "a",
  ]) : []
  billing = try(coalesce( # type: string
    local.a,
    local.b
  ), null)
}
`, `module "vpc" {
  source = "./vpc"

  azs = toset(
    [ # all zones in the region
      "us-east-1a",
      "us-east-1b",
    ],
  )

  billing = try(
    coalesce( # type: string
      local.a,
      local.b,
    ),
    null,
  )

  cidr_blocks = concat(
    [ # private ranges
      # corporate VPN
      "10.0.0.0/8",
    ],
    var.extra_cidrs,
  )

  defaults = var.c ? coalescelist(
    var.list,
    [ # defaults
      "a",
    ],
  ) : []

  subnet_groups = [
    [ # public
      "10.0.1.0/24",
    ],
    [ # private
      "10.0.3.0/24",
    ],
  ]
}
`)
}

// heldAt returns the field of src's AST that holds the comment line text,
// as a path from the value of the attribute it is in ("Value.Args[0]...");
// an InlineComment that InlineInside says the value holds too is skipped.
func heldAt(t *testing.T, src, text string) string {
	t.Helper()
	root, err := parser.Parse([]byte(src), "main.tf")
	if err != nil {
		t.Fatal(err)
	}
	var find func(v reflect.Value, at string) string
	find = func(v reflect.Value, at string) string {
		switch v.Kind() {
		case reflect.Interface, reflect.Ptr:
			if !v.IsNil() {
				return find(v.Elem(), at)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				switch {
				case !f.IsExported():
				case f.Type.Kind() == reflect.String && strings.HasSuffix(f.Name, "Comment"):
					if in := v.FieldByName("InlineInside"); f.Name == "InlineComment" && in.IsValid() && in.Bool() {
						continue
					}
					if slices.Contains(strings.Split(v.Field(i).String(), "\n"), text) {
						return at + "." + f.Name
					}
				default:
					if h := find(v.Field(i), at+"."+f.Name); h != "" {
						return h
					}
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if h := find(v.Index(i), fmt.Sprintf("%s[%d]", at, i)); h != "" {
					return h
				}
			}
		}
		return ""
	}
	at := find(reflect.ValueOf(root), "")
	if i := strings.Index(at, ".Value."); i >= 0 {
		return at[i+1:]
	}
	return at
}

func TestRound7Comments(t *testing.T) {
	// A comment after the opening bracket of a list or call that is a
	// conditional branch, an operand, in parentheses or under an index
	// inside a call argument or list element is held by the list's or
	// call's first element, and printed on its own line just inside the
	// bracket, merged with that element's own comment (D17, a recorded
	// limitation; R7a-001, R7c-001). It is kept, the output holds it where
	// the source does, and the second run changes nothing.
	src := `output "forwarding_rules" {
  value = concat(
    local.create_http ? [ # HTTP forwarding rule
      # primary
      google_compute_global_forwarding_rule.http[0].self_link,
    ] : [],
    var.ssl ? [ # HTTPS forwarding rule
      google_compute_global_forwarding_rule.https[0].self_link,
    ] : [],
  )
}

locals {
  zones = concat(var.multi_az ? [ # all zones in the region
    "a",
    "b",
  ] : ["a"], var.extra)
  ports = [
    80,
    var.tls ? [ # https only
      443,
    ] : [],
  ]
  first = element([ # candidates
    "a",
    "b",
  ][0], 0)
  parens = concat(([ # in parens
    "x",
  ]), var.b)
  subnets = concat(
    var.public ? tolist( # public subnets
      var.public_subnets
    ) : [],
    var.b,
  )
  same = f(
    var.a == [ # operand
      "a",
    ],
    var.b,
  )
}
`
	want := `locals {
  first = element(
    [
      # candidates
      "a",
      "b",
    ][0],
    0,
  )

  parens = concat(
    ([
      # in parens
      "x",
    ]),
    var.b,
  )

  ports = [
    80,
    var.tls ? [
      # https only
      443,
    ] : [],
  ]

  same = f(
    var.a == [
      # operand
      "a",
    ],
    var.b,
  )

  subnets = concat(
    var.public ? tolist(
      # public subnets
      var.public_subnets,
    ) : [],
    var.b,
  )

  zones = concat(
    var.multi_az ? [
      # all zones in the region
      "a",
      "b",
    ] : ["a"],
    var.extra,
  )
}

output "forwarding_rules" {
  value = concat(
    local.create_http ? [
      # HTTP forwarding rule
      # primary
      google_compute_global_forwarding_rule.http[0].self_link,
    ] : [],
    var.ssl ? [
      # HTTPS forwarding rule
      google_compute_global_forwarding_rule.https[0].self_link,
    ] : [],
  )
}
`
	got := formatSource(t, config.Default(), "main.tf", map[string]string{"main.tf": src})
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	for label, at := range map[string]string{
		"HTTP forwarding rule":    "Value.Args[0].TrueExpr.Items[0].ExprComments.LeadComment",
		"primary":                 "Value.Args[0].TrueExpr.Items[0].ExprComments.LeadComment",
		"HTTPS forwarding rule":   "Value.Args[1].TrueExpr.Items[0].ExprComments.LeadComment",
		"all zones in the region": "Value.Args[0].TrueExpr.Items[0].ExprComments.LeadComment",
		"https only":              "Value.Items[1].TrueExpr.Items[0].ExprComments.LeadComment",
		"candidates":              "Value.Args[0].Source.Items[0].ExprComments.LeadComment",
		"in parens":               "Value.Args[0].Expression.Items[0].ExprComments.LeadComment",
		"public subnets":          "Value.Args[0].TrueExpr.Args[0].ExprComments.LeadComment",
		"operand":                 "Value.Args[0].Right.Items[0].ExprComments.LeadComment",
	} {
		for what, text := range map[string]string{"source": src, "output": got} {
			if h := heldAt(t, text, label); h != at {
				t.Errorf("%q in the %s: held at %s, want %s", label, what, h, at)
			}
		}
	}
}

func TestNamingRule(t *testing.T) {
	// Names change, values do not: everything left of a "=" and the labels
	// that name things are renamed; in a value only the name part of a
	// reference to a name the directory (or a local child module) declares
	// and renames follows it.
	files := map[string]string{
		"main.tf": `variable "my-x" {
  type = object({ my-key = string })
}

locals {
  my-local = { my-key = var.my-x.my-key, "Quoted-Key" = 1 }
  names    = [for my-v in var.list : my-v]
  each-key = "${var.undeclared-y}"
}

resource "aws_s3_bucket" "my-b" {
  for_each = var.buckets
  bucket   = local.my-local.my-key
  name     = each.value.bucket-name
  tags     = { Cost-Center = "x" }
}

moved {
  from = aws_s3_bucket.old-b
  to   = aws_s3_bucket.my-b
}

output "my-out" {
  value = aws_s3_bucket.my-b["a"].id
}

module "reg" {
  source = "Azure/naming/azurerm"
  my-arg = 1
}

module "net" {
  source = "./modules/net"
  count  = 1
}

output "seed" {
  value = [module.reg.unique-seed, module.net[0].vpc-id, module.net[*].kept-out, data.terraform_remote_state.s.outputs.vpc-id]
}
`,
		"vars.tf":          "variable \"other-x\" {}\n\noutput \"o\" {\n  value = var.other-x\n}\n",
		"terraform.tfvars": "other-x = 1\nundeclared-z = 2\nmy-x = { my-key = \"v\" }\n",
		"modules/net/outputs.tf": `output "vpc-id" {
  value = 1
}

# terralint ignore_naming
output "kept-out" {
  value = 2
}
`,
	}
	want := map[string]string{
		"main.tf": `locals {
  each_key = var.undeclared-y

  my_local = {
    my_key       = var.my_x.my-key
    "Quoted-Key" = 1
  }

  names = [for my-v in var.list : my-v]
}

module "net" {
  source = "./modules/net"

  count = 1
}

module "reg" {
  source = "Azure/naming/azurerm"

  my_arg = 1
}

moved {
  from = aws_s3_bucket.old-b
  to   = aws_s3_bucket.my_b
}

output "my_out" {
  value = aws_s3_bucket.my_b["a"].id
}

output "seed" {
  value = [
    module.reg.unique-seed,
    module.net[0].vpc_id,
    module.net[*].kept-out,
    data.terraform_remote_state.s.outputs.vpc-id,
  ]
}

resource "aws_s3_bucket" "my_b" {
  for_each = var.buckets

  bucket = local.my_local.my-key
  name   = each.value.bucket-name

  tags = {
    Cost_Center = "x"
  }
}

variable "my_x" {
  type = object({
    my_key = string
  })
}
`,
		"vars.tf": "output \"o\" {\n  value = var.other_x\n}\n\nvariable \"other_x\" {}\n",
		"terraform.tfvars": `my_x = {
  my_key = "v"
}

other_x      = 1
undeclared_z = 2
`,
		"modules/net/outputs.tf": `# terralint ignore_naming
output "kept-out" {
  value = 2
}

output "vpc_id" {
  value = 1
}
`,
	}
	for name, w := range want {
		if got := formatSource(t, config.Default(), name, files); got != w {
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", name, got, w)
		}
	}
}

func TestNamingMarkers(t *testing.T) {
	// A marked item keeps its own names; references in it to renamed names
	// still follow. A marker on a variable or on a .tfvars assignment keeps
	// every name of both. Provider local names never change.
	files := map[string]string{
		"main.tf": `# terralint ignore_naming
variable "keep-x" {
  type = object({ a-b = string })
}

variable "set-y" {
  type = object({ c-d = string })
}

variable "my-z" {}

# terralint ignore_naming
resource "aws_s3_bucket" "keep-me" {
  bucket = var.my-z
  tags   = { team-name = var.keep-x.a-b }
}

output "o" {
  value = [aws_s3_bucket.keep-me.id, var.keep-x.a-b, var.set-y.c-d]
}

terraform {
  required_providers {
    google-beta = { source = "hashicorp/google-beta" }
  }
}

provider "google-beta" {}

module "m" {
  source    = "./m"
  providers = { google-beta = google-beta }
}
`,
		"terraform.tfvars": "keep-x = { a-b = \"1\" }\n\n# terralint ignore_naming\nset-y = { c-d = \"2\" }\n",
	}
	got := formatSource(t, config.Default(), "main.tf", files)
	for _, w := range []string{
		`google-beta = {`, `provider "google-beta" {}`, `google-beta = google-beta`,
		`value = [aws_s3_bucket.keep-me.id, var.keep-x.a-b, var.set-y.c-d]`,
		`resource "aws_s3_bucket" "keep-me" {`, `bucket = var.my_z`, `team-name = var.keep-x.a-b`,
		`variable "keep-x" {`, `a-b = string`, `variable "my_z" {}`, `variable "set-y" {`, `c-d = string`,
	} {
		if !strings.Contains(got, w) {
			t.Errorf("main.tf: want %q in:\n%s", w, got)
		}
	}
	if got, w := formatSource(t, config.Default(), "terraform.tfvars", files), "keep-x = {\n  a-b = \"1\"\n}\n\n# terralint ignore_naming\nset-y = {\n  c-d = \"2\"\n}\n"; got != w {
		t.Errorf("terraform.tfvars:\ngot:\n%s\nwant:\n%s", got, w)
	}

	// The pairing holds for undashed variable names too (R10b-001, D25).
	plain := map[string]string{
		"variables.tf":     "variable \"db\" {\n  type = object({\n    instance-class = string\n  })\n}\n\n# terralint ignore_naming\nvariable \"tags\" {\n  default = {\n    cost-center = \"1\"\n  }\n}\n",
		"terraform.tfvars": "# terralint ignore_naming\ndb = {\n  instance-class = \"db.t3.micro\"\n}\n\ntags = {\n  cost-center = \"1234\"\n}\n",
	}
	for name, want := range plain {
		if got := formatSource(t, config.Default(), name, plain); got != want {
			t.Errorf("undashed variable names, %s:\ngot:\n%s\nwant:\n%s", name, got, want)
		}
	}

	// With the rule off nothing is renamed; with a type left out of Labels,
	// its labels, the names after it and the references to it keep theirs.
	off := config.Default()
	off.Rules[config.RuleNaming] = false
	check(t, off, "variable \"my-x\" {}\n\noutput \"o\" {\n  value = { my-k = var.my-x }\n}\n",
		"output \"o\" {\n  value = {\n    my-k = var.my-x\n  }\n}\n\nvariable \"my-x\" {}\n")
	noVar := config.Default()
	delete(noVar.Naming.Labels, "variable")
	if got, w := formatSource(t, noVar, "main.tf", map[string]string{"main.tf": "variable \"my-x\" {}\n\nmodule \"m\" {\n  source = \"./m\"\n  my-arg = var.my-x\n}\n", "a.tfvars": "my-x = 1\n"}),
		"module \"m\" {\n  source = \"./m\"\n\n  my-arg = var.my-x\n}\n\nvariable \"my-x\" {}\n"; got != w {
		t.Errorf("without variable labels:\ngot:\n%s\nwant:\n%s", got, w)
	}
	if got := formatSource(t, noVar, "a.tfvars", map[string]string{"main.tf": "variable \"my-x\" {}\n", "a.tfvars": "my-x = 1\n"}); got != "my-x = 1\n" {
		t.Errorf("without variable labels, a.tfvars: %q", got)
	}
}

func TestCheckScopedData(t *testing.T) {
	// A data source declared in a check block is scoped to it: references
	// to it in the check follow its rename and its marker (D24).
	src := `check "site-up" {
  data "http" "my-site" {
    url = "https://example.com"
  }

  assert {
    condition     = data.http.my-site.status_code == 200
    error_message = "down"
  }
}

check "kept" {
  # terralint ignore_naming
  data "http" "keep-site" {
    url = "https://example.com"
  }

  assert {
    condition     = data.http.keep-site.status_code == 200 && data.http.my-other.id != ""
    error_message = "down"
  }
}

data "http" "my-other" {}
`
	check(t, nil, src, `check "kept" {
  assert {
    condition     = data.http.keep-site.status_code == 200 && data.http.my_other.id != ""
    error_message = "down"
  }

  # terralint ignore_naming
  data "http" "keep-site" {
    url = "https://example.com"
  }
}

check "site-up" {
  assert {
    condition     = data.http.my_site.status_code == 200
    error_message = "down"
  }

  data "http" "my_site" {
    url = "https://example.com"
  }
}

data "http" "my_other" {}
`)
	noData := config.Default()
	delete(noData.Naming.Labels, "data")
	if got := formatSource(t, noData, "main.tf", map[string]string{"main.tf": src}); !strings.Contains(got, `data "http" "my-site"`) || !strings.Contains(got, "data.http.my-site.status_code") {
		t.Errorf("without data labels, the scoped data source and its reference keep their names:\n%s", got)
	}
}

func TestModuleCallArguments(t *testing.T) {
	// An argument a local child module declares follows the child's
	// variable, whatever markers the call carries; its keys are kept when
	// the child's variable is marked. Other arguments are ordinary names: a
	// marker on the call, a section around it or on the argument keeps them
	// and their keys (D23). Output reads follow the child also when the call
	// is in a file without a dash.
	files := map[string]string{
		"main.tf": `# terralint ignore_naming
module "reg" {
  source      = "Azure/naming/azurerm"
  unique-seed = "abc"
  name-prefix = { my-key = 1 }
}

# terralint ignore_naming {
module "reg-two" {
  source      = "cloudposse/label/null"
  label-order = ["a"]
}
# }

# terralint ignore_naming
module "net" {
  source     = "./modules/net"
  cidr-block = "10.0.0.0/16"
  vpc-name   = "x"
  tag-map    = { my-key = 1 }
  opts       = { my-key = 1 }
  extra-arg  = { my-key = 1 }
}

module "net-two" {
  source     = "./modules/net"
  cidr-block = "10.0.0.0/16"
  vpc-name   = "x"
  tag-map    = { my-key = 1 }
  opts       = { my-key = 1 }
  # terralint ignore_naming
  extra-arg  = { my-key = 1 }
}

output "ids" {
  value = [module.net.vpc-id, module.lone.vpc-id]
}
`,
		"calls.tf": "module \"lone\" {\n  source = \"./modules/net\"\n}\n",
		"modules/net/variables.tf": `# terralint ignore_naming
variable "cidr-block" {}

variable "vpc-name" {}

# terralint ignore_naming
variable "tag-map" {}

variable "opts" {}

output "vpc-id" {
  value = var.vpc-name
}
`,
	}
	want := `# terralint ignore_naming
module "net" {
  source = "./modules/net"

  cidr-block = "10.0.0.0/16"

  extra-arg = {
    my-key = 1
  }

  opts = {
    my-key = 1
  }

  tag-map = {
    my-key = 1
  }

  vpc_name = "x"
}

module "net_two" {
  source = "./modules/net"

  cidr-block = "10.0.0.0/16"

  # terralint ignore_naming
  extra-arg = {
    my-key = 1
  }

  opts = {
    my_key = 1
  }

  tag-map = {
    my-key = 1
  }

  vpc_name = "x"
}

# terralint ignore_naming
module "reg" {
  source = "Azure/naming/azurerm"

  name-prefix = {
    my-key = 1
  }

  unique-seed = "abc"
}

# terralint ignore_naming {
module "reg-two" {
  source = "cloudposse/label/null"

  label-order = ["a"]
}
# }

output "ids" {
  value = [module.net.vpc_id, module.lone.vpc_id]
}
`
	if got := formatSource(t, config.Default(), "main.tf", files); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSiblingChanges(t *testing.T) {
	// What other files declare is read again when they change, also in the
	// same process.
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.tf", "output \"o\" {\n  value = var.my-x\n}\n")
	for _, c := range []struct{ vars, want string }{
		{"variable \"my-x\" {}\n", "var.my_x"},
		{"# terralint ignore_naming\nvariable \"my-x\" {}\n", "var.my-x"},
		{"variable \"my-x\" {}\n", "var.my_x"},
	} {
		write("vars.tf", c.vars)
		out, err := format.File(filepath.Join(dir, "main.tf"), config.Default())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), c.want) {
			t.Errorf("with vars.tf %q: want %s in:\n%s", c.vars, c.want, out)
		}
	}
}

func TestDirectoryCost(t *testing.T) {
	// Formatting every file of a directory costs about what formatting each
	// alone does, not the directory once per file (R9a-001): 60 files of 150
	// resources with dashed names took 14s when each file parsed all others.
	dir := t.TempDir()
	var files []string
	for f := 1; f <= 60; f++ {
		var b strings.Builder
		for r := 1; r <= 150; r++ {
			fmt.Fprintf(&b, "resource \"aws_instance\" \"web-%d-%d\" {\n  ami       = var.ami-id\n  subnet_id = aws_subnet.main-%d.id\n  vpc       = module.vpc.private_subnets[0]\n  tags      = { Name = \"web-%d-%d\" }\n}\n\n", f, r, f, f, r)
		}
		fmt.Fprintf(&b, "resource \"aws_subnet\" \"main-%d\" {}\n", f)
		files = append(files, filepath.Join(dir, fmt.Sprintf("f%d.tf", f)))
		if err := os.WriteFile(files[len(files)-1], []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vars := "variable \"ami-id\" {}\n\nmodule \"vpc\" {\n  source = \"terraform-aws-modules/vpc/aws\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "vars.tf"), []byte(vars), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for _, path := range files {
		out, err := format.File(path, config.Default())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "ami       = var.ami_id") {
			t.Fatalf("%s: references do not follow the renamed variable:\n%.300s", path, out)
		}
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("formatting a directory of 60 files took %s, want under 8s", took)
	}
}
