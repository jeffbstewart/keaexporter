// Package oui maps MAC addresses to their IEEE-registered vendor.
// table.txt is GENERATED: update the capture with cmd/ouifetch, then
// regenerate with cmd/ouigen (presubmit fails if the two drift).
package oui

import (
	_ "embed"
	"strings"
)

//go:embed table.txt
var tableTxt string

var table = parse(tableTxt)

// parse reads "PREFIX<tab>vendor" lines; "#" starts a comment.
func parse(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix, name, ok := strings.Cut(line, "\t")
		if ok {
			m[prefix] = name
		}
	}
	return m
}

// normalize strips separators and uppercases; "" if hw is not a
// plausible 48-bit MAC.
func normalize(hw string) string {
	n := strings.ToUpper(strings.Map(func(r rune) rune {
		switch r {
		case ':', '-', '.':
			return -1
		}
		return r
	}, hw))
	if len(n) != 12 || strings.Trim(n, "0123456789ABCDEF") != "" {
		return ""
	}
	return n
}

// Vendor returns the registered organization for hw (any common MAC
// notation), or "". Longest registered prefix wins: /36, /28, /24.
func Vendor(hw string) string {
	return lookup(table, hw)
}

func lookup(m map[string]string, hw string) string {
	n := normalize(hw)
	if n == "" {
		return ""
	}
	for _, l := range []int{9, 7, 6} {
		if v, ok := m[n[:l]]; ok {
			return v
		}
	}
	return ""
}

// Randomized reports whether hw has the locally-administered bit set
// -- the signature of modern phone/laptop MAC randomization (and of
// any hand-assigned address). Such addresses carry no vendor.
func Randomized(hw string) bool {
	n := normalize(hw)
	return n != "" && strings.ContainsRune("26AE", rune(n[1]))
}
