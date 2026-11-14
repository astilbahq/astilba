package agent

import (
	"testing"
	"unicode/utf8"
)

// Management's API takes preflight notes of at most 256 characters, and refuses a heartbeat or
// enrolment with a longer one.
func TestPreflightNotesFitTheAPI(t *testing.T) {
	for _, note := range []string{classicImageStoreNote, oldContainerdDockerNote, buildPolicyUnsupported, builderUnsupportedNote, dockerFirewallOffNote, instanceMetadataNote} {
		if n := utf8.RuneCountInString(note); n > 256 {
			t.Errorf("a preflight note has %d characters: %q", n, note)
		}
	}
}

func TestPreflightNotesHowDockerKeepsImages(t *testing.T) {
	containerd := [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}
	classic := [][2]string{{"Backing Filesystem", "extfs"}}
	for _, tc := range []struct {
		status  [][2]string
		version string
		known   bool
		note    string
	}{
		{containerd, "1.54", true, ""},
		{containerd, "1.48", true, ""},
		{containerd, "1.47", true, oldContainerdDockerNote},
		{containerd, "", false, ""},
		{classic, "1.54", true, classicImageStoreNote},
		{classic, "1.47", true, classicImageStoreNote},
	} {
		if note := imageStoreNote(tc.status, tc.version, tc.known); note != tc.note {
			t.Errorf("%v %s %v: %q", tc.status, tc.version, tc.known, note)
		}
	}
}
