package agent

import (
	"testing"
	"unicode/utf8"
)

// Management's API takes preflight notes of at most 256 characters, and refuses a heartbeat or
// enrolment with a longer one.
func TestPreflightNotesFitTheAPI(t *testing.T) {
	for _, note := range []string{classicImageStoreNote, buildPolicyUnsupported, builderUnsupportedNote, dockerFirewallOffNote, instanceMetadataNote} {
		if n := utf8.RuneCountInString(note); n > 256 {
			t.Errorf("a preflight note has %d characters: %q", n, note)
		}
	}
}
