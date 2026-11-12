package deployment

import (
	"strconv"
	"strings"
	"testing"
)

func TestElevatedServicesEffectFitsAnEffect(t *testing.T) {
	if effect := ElevatedServicesEffect([]string{"dind", "runner"}); !strings.Contains(effect, "dind, runner differs") {
		t.Fatalf("named services are missing: %q", effect)
	}
	names := []string{}
	for i := range 20 {
		names = append(names, "service-with-a-long-name-"+strconv.Itoa(i))
	}
	effect := ElevatedServicesEffect(names)
	if len(effect) > effectLength || !strings.Contains(effect, "service-with-a-long-name-0, ") || !strings.Contains(effect, " more differs") {
		t.Fatalf("a project's worth of services does not fit an effect: %d %q", len(effect), effect)
	}
}
