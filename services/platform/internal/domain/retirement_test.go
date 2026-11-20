package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The refusal names up to ten previews with what each waits for, and counts the rest.
func TestPreviewsRemainNamesEachPreview(t *testing.T) {
	previews := []PendingPreview{{Number: 3, State: "blocked", Note: "Cleanup waits for a verified agent transfer."}, {Number: 5, State: "deleting"}}
	for n := int32(6); n < 17; n++ {
		previews = append(previews, PendingPreview{Number: n, State: "queued"})
	}
	var refusal *Error
	if !errors.As(PreviewsRemain(previews), &refusal) || refusal.Code != "conflict" || !slices.Equal(refusal.Fields, []string{"retirement.previews"}) {
		t.Fatalf("refusal: %+v", refusal)
	}
	for _, want := range []string{" #3 is blocked (Cleanup waits for a verified agent transfer); #5 is deleting; #6 is queued;", "#13 is queued; and 3 more."} {
		if !strings.Contains(refusal.Message, want) {
			t.Errorf("missing %q in %s", want, refusal.Message)
		}
	}
	if strings.Contains(refusal.Message, "#14") {
		t.Errorf("named more than ten previews: %s", refusal.Message)
	}
	if one := PreviewsRemain(previews[1:2]).Error(); !strings.HasSuffix(one, fmt.Sprintf(" #%d is deleting.", 5)) {
		t.Errorf("single preview: %s", one)
	}
}
