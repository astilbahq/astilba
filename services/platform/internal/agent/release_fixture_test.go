package agent

import (
	"fmt"
	"runtime"
)

// Fixture releases claim this source commit and minimum version, and the agents installing them
// run with the same minimum version.
const (
	fixtureCommit = "0123456789abcdef0123456789abcdef01234567"
	fixtureFloor  = "0.0.0"
)

var permitted = Running{MinimumVersion: fixtureFloor}

// fixtureAgent is a stand-in executable that describes itself as release version does. keyAuth
// marks a release that authenticates with a key.
func fixtureAgent(version string, keyAuth bool) []byte {
	return fixtureAgentWithFloor(version, fixtureFloor, keyAuth)
}

// fixtureAgentWithFloor is fixtureAgent built with minimum version floor.
func fixtureAgentWithFloor(version, floor string, keyAuth bool) []byte {
	extra := ""
	if keyAuth {
		extra = `,"KeyAuth":1`
	}
	return []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"Version\":\"%s\",\"MinimumVersion\":\"%s\",\"Commit\":\"%s\",\"Protocol\":1,\"Arch\":\"%s\"%s}'\n", version, floor, fixtureCommit, runtime.GOARCH, extra))
}
