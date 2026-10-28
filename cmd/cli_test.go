package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With TERRALINT_TEST_CLI set, the test binary runs the CLI instead of the
// tests, so each case gets a fresh process with its own flags and exit code.
func TestMain(m *testing.M) {
	if os.Getenv("TERRALINT_TEST_CLI") != "" {
		Execute()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "TERRALINT_TEST_CLI=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validTF = "resource \"a\" \"b\" {\n  ids = aws_instance.x[*].id # splat\n}\n"
const invalidTF = "variable \"a\" {\n  default =\n}\n"

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid")
	write(t, filepath.Join(valid, "ok.tf"), validTF)
	write(t, filepath.Join(valid, "d1", "d2", "d3", "deep.tfvars"), "a = 1\n")
	write(t, filepath.Join(dir, "outside.tf"), validTF)
	// Directory mode skips links, dangling or not, and never writes through them.
	if err := os.Symlink(filepath.Join(dir, "outside.tf"), filepath.Join(valid, "link.tf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing.tfvars"), filepath.Join(valid, "d1", "dangling.tfvars")); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(filepath.Join(dir, "outside.tf"), old, old); err != nil {
		t.Fatal(err)
	}
	mixed := filepath.Join(dir, "mixed")
	write(t, filepath.Join(mixed, "ok.tf"), validTF)
	write(t, filepath.Join(mixed, "bad.tf"), invalidTF)
	write(t, filepath.Join(mixed, "s1", "s2", "s3", "bad2.tf"), invalidTF)
	write(t, filepath.Join(dir, "notes.txt"), "x\n")
	// A directory named through a link is checked like the directory itself.
	mixedLink := filepath.Join(dir, "mixed-link")
	if err := os.Symlink(mixed, mixedLink); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		args       []string
		code       int
		inStderr   []string // each must appear exactly once
		quiet      bool     // nothing on stdout or stderr
		usageShown bool
	}{
		{args: []string{"check", "-f", filepath.Join(valid, "ok.tf")}, quiet: true},
		{args: []string{"check", "-d", valid}, quiet: true},
		{args: []string{"apply", "-d", valid}, quiet: true},
		{args: []string{"apply", "-f", filepath.Join(valid, "ok.tf")}, quiet: true},
		{args: []string{"check", "-f", filepath.Join(mixed, "bad.tf")}, code: 1, inStderr: []string{"bad.tf:2,12-3,1: Invalid expression"}},
		{args: []string{"check", "-d", mixed}, code: 1, inStderr: []string{"bad.tf:", "bad2.tf:"}},
		{args: []string{"apply", "-d", mixed}, code: 1, inStderr: []string{"bad.tf:", "bad2.tf:"}},
		{args: []string{"check", "-f", filepath.Join(valid, "ok.tf"), filepath.Join(mixed, "bad.tf")}, code: 1, inStderr: []string{"unexpected arguments"}, usageShown: true},
		{args: []string{"check", "-f", filepath.Join(dir, "notes.txt")}, code: 1, inStderr: []string{"not a .tf or .tfvars file"}},
		{args: []string{"check", "-f", valid}, code: 1, inStderr: []string{"expected a file path"}, usageShown: true},
		{args: []string{"check", "-d", filepath.Join(valid, "ok.tf")}, code: 1, inStderr: []string{"expected a directory path"}, usageShown: true},
		{args: []string{"check"}, code: 1, inStderr: []string{"exactly one of the command flags"}, usageShown: true},
		{args: []string{"-f", filepath.Join(mixed, "bad.tf")}, code: 1, inStderr: []string{"need a command"}, usageShown: true},
		{args: []string{"check", "-d", mixedLink}, code: 1, inStderr: []string{"mixed-link/bad.tf:", "bad2.tf:"}},
		{args: []string{"apply", "-d", mixedLink}, code: 1, inStderr: []string{"mixed-link/bad.tf:", "bad2.tf:"}},
		{args: []string{"check", "-f", filepath.Join(mixed, "bad.tf"), "-f", filepath.Join(valid, "ok.tf")}, code: 1, inStderr: []string{"more than once"}, usageShown: true},
		{args: []string{"apply", "-d", mixed, "-d", valid}, code: 1, inStderr: []string{"more than once"}, usageShown: true},
	}
	for _, tt := range tests {
		code, stdout, stderr := runCLI(t, tt.args...)
		name := strings.Join(tt.args, " ")
		if code != tt.code {
			t.Errorf("%s: exit %d, want %d; stderr %q", name, code, tt.code, stderr)
		}
		if tt.quiet && (stdout != "" || stderr != "") {
			t.Errorf("%s: stdout %q, stderr %q, want no output", name, stdout, stderr)
		}
		for _, s := range tt.inStderr {
			if n := strings.Count(stderr, s); n != 1 {
				t.Errorf("%s: %q appears %d times in stderr, want once: %q", name, s, n, stderr)
			}
		}
		if strings.Contains(stderr, "Usage:") != tt.usageShown {
			t.Errorf("%s: usage shown %v, want %v", name, !tt.usageShown, tt.usageShown)
		}
		if strings.Contains(stderr, "panic") {
			t.Errorf("%s: panicked: %s", name, stderr)
		}
	}

	if got, _ := os.ReadFile(filepath.Join(valid, "ok.tf")); string(got) != validTF {
		t.Errorf("apply changed ok.tf: %q", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "outside.tf")); err != nil || !info.ModTime().Equal(old) {
		t.Errorf("apply -d wrote through a symbolic link to outside.tf")
	}
}

// Directory mode skips files and directories whose name starts with ".",
// below the given directory, as Terraform does (.terraform/ holds downloaded
// modules); a dot-named directory given itself is walked.
func TestCLIHidden(t *testing.T) {
	dir := t.TempDir()
	unsorted := "variable \"b\" {}\n\nvariable \"a\" {}\n"
	write(t, filepath.Join(dir, "main.tf"), validTF)
	hidden := []string{
		filepath.Join(dir, ".terraform", "modules", "vpc", "variables.tf"),
		filepath.Join(dir, "sub", ".x.tf"),
		filepath.Join(dir, ".bad.tf"),
	}
	for _, p := range hidden[:2] {
		write(t, p, unsorted)
	}
	write(t, hidden[2], invalidTF)
	for _, cmd := range []string{"check", "apply"} {
		if code, stdout, stderr := runCLI(t, cmd, "-d", dir); code != 0 || stdout != "" || stderr != "" {
			t.Errorf("%s -d: exit %d, stdout %q, stderr %q; want 0 and silent", cmd, code, stdout, stderr)
		}
	}
	for _, p := range hidden[:2] {
		if got, _ := os.ReadFile(p); string(got) != unsorted {
			t.Errorf("apply -d changed %s: %q", p, got)
		}
	}
	if code, _, stderr := runCLI(t, "check", "-d", filepath.Join(dir, ".terraform")); code != 1 || !strings.Contains(stderr, "variables.tf") {
		t.Errorf("check -d .terraform: exit %d, stderr %q; want 1 naming variables.tf", code, stderr)
	}
}
