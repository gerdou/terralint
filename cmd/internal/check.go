package internal

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gerdou/terralint/config"
	"github.com/gerdou/terralint/format"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// Check reports every file under path that cannot be parsed or breaks a rule.
func Check(path string) error {
	return forEachFile(path, func(filePath string) error {
		original, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		formatted, err := format.File(filePath, config.Default())
		if err != nil {
			return err
		}
		if err := compare(original, formatted); err != nil {
			return fmt.Errorf("%s: %w", filePath, err)
		}
		return nil
	})
}

func compare(original []byte, formatted []byte) error {
	originalHash, formattedHash := generateHash(original, formatted)

	if formattedHash == originalHash {
		return nil
	}

	return compareContent(original, formatted)
}

func generateHash(original []byte, formatted []byte) (string, string) {
	hasher := sha1.New()
	hasher.Write(original)

	originalHash := hex.EncodeToString(hasher.Sum(nil))

	hasher.Reset()

	hasher.Write(formatted)

	formattedHash := hex.EncodeToString(hasher.Sum(nil))
	return originalHash, formattedHash
}

func compareContent(original []byte, formatted []byte) error {
	dmp := diffmatchpatch.New()
	dmp.DiffTimeout = time.Hour
	src := string(original)
	dst := string(formatted)

	diffs := lineDiff(dmp, src, dst)

	var notEquals []diffmatchpatch.Diff
	for _, diff := range diffs {
		if diff.Type != diffmatchpatch.DiffEqual {
			notEquals = append(notEquals, diff)
		}
	}

	if notEquals == nil || len(notEquals) == 0 {
		return nil
	}

	var errorText strings.Builder
	errorText.WriteString("\n")
	errorText.WriteString(dmp.DiffPrettyText(diffs))
	return errors.New(errorText.String())
}

// lineDiff diffs a and b line by line. Each distinct line stands for one
// private-use rune while diffing: go-diff's own DiffLinesToRunes encodes
// line numbers as digit strings and so diffs digits, not lines.
func lineDiff(dmp *diffmatchpatch.DiffMatchPatch, a, b string) []diffmatchpatch.Diff {
	index := map[string]rune{}
	var lines []string
	toRunes := func(s string) []rune {
		var rs []rune
		for _, line := range strings.SplitAfter(s, "\n") {
			if line == "" {
				continue
			}
			r, ok := index[line]
			if !ok {
				r = lineRune(len(lines))
				index[line] = r
				lines = append(lines, line)
			}
			rs = append(rs, r)
		}
		return rs
	}
	ra, rb := toRunes(a), toRunes(b)
	byRune := make(map[rune]string, len(index))
	for line, r := range index {
		byRune[r] = line
	}
	diffs := dmp.DiffMainRunes(ra, rb, false)
	for i := range diffs {
		var text strings.Builder
		for _, r := range diffs[i].Text {
			text.WriteString(byRune[r])
		}
		diffs[i].Text = text.String()
	}
	return diffs
}

// lineRune maps a line number to a rune in the private-use areas, which
// hold 137,468 of them.
func lineRune(i int) rune {
	if i < 0x1900 {
		return rune(0xE000 + i)
	}
	return rune(0xF0000 + i - 0x1900)
}
