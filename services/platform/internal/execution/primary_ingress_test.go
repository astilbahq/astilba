package execution

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The warning names whom a proxy restart interrupts, within the length the contract allows one
// effect, and counts whom it has no room for.
func TestIngressRestartEffectFitsTheContract(t *testing.T) {
	long := strings.Repeat("ü", 80)
	many := []string{}
	for i := range 50 {
		many = append(many, fmt.Sprintf("%s / %02d", long, i))
	}
	for name, c := range map[string]struct {
		routed []string
		want   string
	}{
		"nothing":         {nil, "Nothing routes through it yet."},
		"a few":           {[]string{"Shop / Production", "Blog / pr-12 (preview)"}, "Traffic to Shop / Production, Blog / pr-12 (preview) drops"},
		"too many":        {append([]string{"Shop / Production"}, many...), "Traffic to Shop / Production, " + many[0] + " and 49 more drops"},
		"one fits":        {many, "Traffic to " + many[0] + " and 49 more drops"},
		"none fit":        {[]string{strings.Repeat("x", 190), strings.Repeat("y", 190), strings.Repeat("z", 190)}, "Traffic to 3 environments drops"},
		"one is too long": {[]string{strings.Repeat("x", 250)}, "Traffic to 1 environment drops"},
	} {
		t.Run(name, func(t *testing.T) {
			got := ingressRestartEffect("edge-1", c.routed)
			if utf8.RuneCountInString(got) > maxEffectLength || !strings.Contains(got, c.want) {
				t.Fatalf("%d characters: %s", utf8.RuneCountInString(got), got)
			}
		})
	}
}
