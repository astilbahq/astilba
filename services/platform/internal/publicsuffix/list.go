// Package publicsuffix answers Public Suffix List questions from a pinned copy of the
// list, so that a new entry reaches a release when scripts/public-suffix-list.mjs
// moves the pin rather than when golang.org/x/net next regenerates its table. See
// docs/hosting.md#generated-application-names.
package publicsuffix

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

//go:embed public_suffix_list.dat
var listData string

type rules struct {
	// normal holds plain rules, wildcards the parent of each "*." rule, and exceptions
	// each "!" rule without its "!".
	normal, wildcards, exceptions map[string]bool
}

var loaded = sync.OnceValue(func() rules {
	r, e := parse(listData)
	if e != nil {
		// The list is embedded and parsed by this package's tests.
		panic(e)
	}
	return r
})

// parse follows https://github.com/publicsuffix/list/wiki/Format: a rule is a line's
// text up to its first whitespace, and a line starting with "//" is a comment.
func parse(text string) (rules, error) {
	r := rules{normal: map[string]bool{}, wildcards: map[string]bool{}, exceptions: map[string]bool{}}
	for line := range strings.Lines(text) {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "//") {
			continue
		}
		rule := fields[0]
		set := r.normal
		switch {
		case strings.HasPrefix(rule, "!"):
			rule, set = rule[1:], r.exceptions
		case strings.HasPrefix(rule, "*."):
			rule, set = rule[2:], r.wildcards
		}
		ascii := rule
		if !isASCII(rule) {
			var e error
			if ascii, e = idna.ToASCII(rule); e != nil {
				return rules{}, fmt.Errorf("public suffix rule %q: %w", fields[0], e)
			}
		}
		if ascii == "" || strings.Contains(ascii, "*") || strings.Contains(ascii, "..") || ascii != strings.ToLower(ascii) {
			return rules{}, fmt.Errorf("unsupported public suffix rule %q", fields[0])
		}
		set[ascii] = true
	}
	if len(r.normal) == 0 {
		return rules{}, errors.New("the public suffix list has no rules")
	}
	return r, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// PublicSuffix returns the public suffix of domain, a lowercase ASCII name with no
// empty labels, under the algorithm at https://publicsuffix.org/list/. listed reports
// whether a rule on the list decided it; when none matches, the default rule makes
// the last label the suffix. It returns "" for a name with an empty label.
func PublicSuffix(domain string) (suffix string, listed bool) {
	labels := strings.Split(domain, ".")
	for _, l := range labels {
		if l == "" {
			return "", false
		}
	}
	r := loaded()
	// An exception rule wins over every other rule, and its suffix drops its first label.
	for i := range labels {
		if r.exceptions[strings.Join(labels[i:], ".")] {
			return strings.Join(labels[i+1:], "."), true
		}
	}
	// Otherwise the matching rule with the most labels wins: start from the whole name.
	for i := range labels {
		if r.normal[strings.Join(labels[i:], ".")] || i+1 < len(labels) && r.wildcards[strings.Join(labels[i+1:], ".")] {
			return strings.Join(labels[i:], "."), true
		}
	}
	return labels[len(labels)-1], false
}

// RegistrableDomain returns the public suffix of domain and the label before it, the
// name browsers treat as one site. It fails for a public suffix itself.
func RegistrableDomain(domain string) (string, error) {
	suffix, _ := PublicSuffix(domain)
	if suffix == "" || suffix == domain {
		return "", fmt.Errorf("%q has no registrable domain", domain)
	}
	rest := strings.TrimSuffix(domain, "."+suffix)
	return rest[strings.LastIndexByte(rest, '.')+1:] + "." + suffix, nil
}

// Version names the pinned list for logs and errors.
func Version() string {
	return "publicsuffix/list@" + listCommit[:12] + " (" + listCommitted + ")"
}
