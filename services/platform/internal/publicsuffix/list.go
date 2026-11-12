// Package publicsuffix answers Public Suffix List questions from a pinned copy of the
// list, so that a new entry reaches a release when scripts/public-suffix-list.mjs
// moves the pin rather than when golang.org/x/net next regenerates its table. See
// docs/hosting.md#generated-application-names.
package publicsuffix

import (
	_ "embed"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"

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
		ascii, e := idna.ToASCII(rule)
		if e != nil {
			return rules{}, fmt.Errorf("public suffix rule %q: %w", fields[0], e)
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

// publicSuffix returns the public suffix of domain, a lowercase ASCII name, under the
// algorithm at https://publicsuffix.org/list/. listed reports whether a rule on the
// list decided it; when none matches, the default rule makes the last label the
// suffix. An IP address, or a name with an empty label, is its own suffix.
func (r rules) publicSuffix(domain string) (suffix string, listed bool) {
	labels := strings.Split(domain, ".")
	if _, e := netip.ParseAddr(domain); e == nil || slices.Contains(labels, "") {
		return domain, false
	}
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

// isolates reports whether every name below domain has a public suffix at or below
// domain. That holds when domain is itself a public suffix, listed exactly or by a
// wildcard on its parent, or when a "*.domain" rule makes each name directly below it
// one, and no exception rule undoes it. The child label "*" stands for every label:
// parse refuses a rule key containing "*", so no rule can single the child out.
func (r rules) isolates(domain string) bool {
	suffix, listed := r.publicSuffix("*." + domain)
	return listed && (suffix == domain || strings.HasSuffix(suffix, "."+domain))
}

// Isolates reports whether browsers put every name below domain, a lowercase ASCII
// name, in a site of its own below domain rather than in one site shared with the
// rest of domain.
func Isolates(domain string) bool { return loaded().isolates(domain) }

// RegistrableDomain returns the public suffix of domain, a lowercase ASCII name, and
// the label before it: the name browsers treat as one site. It fails for a public
// suffix itself and for an IP address.
func RegistrableDomain(domain string) (string, error) {
	suffix, _ := loaded().publicSuffix(domain)
	if suffix == domain {
		return "", fmt.Errorf("%q has no registrable domain", domain)
	}
	rest := strings.TrimSuffix(domain, "."+suffix)
	return rest[strings.LastIndexByte(rest, '.')+1:] + "." + suffix, nil
}

// Version names the pinned list for logs and errors.
func Version() string {
	return "publicsuffix/list@" + listCommit[:12] + " (pushed " + listPushed + ")"
}
