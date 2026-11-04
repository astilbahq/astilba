// Package terminal defines the frames both streams of a troubleshooting session use.
// Terminal content passes through unlogged and unrecorded.
package terminal

import (
	"encoding/binary"
	"errors"
)

// MaxFrame bounds a binary message, including its one-byte kind.
const MaxFrame = 32 << 10

const (
	Input   byte = 1 // browser to agent: typed input
	Output  byte = 2 // agent to browser: terminal output
	Resize  byte = 3 // browser to agent: columns and rows
	Start   byte = 4 // management to agent: both streams attached, start the shell
	Exit    byte = 5 // agent to browser: the shell ended
	Refused byte = 6 // agent to browser: a reason the session cannot run
	Warning byte = 7 // management to browser: seconds until a limit ends the session
)

const maxDimension = 1000

// Reasons an agent refuses a session.
const (
	RefusedOptIn      = "opt_in"
	RefusedRuntime    = "runtime"
	RefusedIneligible = "ineligible"
	RefusedNoShell    = "no_shell"
	RefusedLimit      = "limit"
	// RefusedUnavailable is a Docker failure on the host, which says nothing about the target.
	RefusedUnavailable = "unavailable"
	// RefusedInputOverflow ends a session whose shell stopped reading more input than can wait.
	RefusedInputOverflow = "input_overflow"
	refusedUnknown       = "unknown"
)

// Refusal maps an agent's reason to a known value, because the text reaches the session
// record and the dashboard.
func Refusal(reason []byte) string {
	switch text := string(reason); text {
	case RefusedOptIn, RefusedRuntime, RefusedIneligible, RefusedNoShell, RefusedLimit, RefusedUnavailable, RefusedInputOverflow:
		return text
	default:
		return refusedUnknown
	}
}

// ResizeFrame encodes terminal dimensions.
func ResizeFrame(columns, rows uint16) []byte {
	out := []byte{Resize, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(out[1:], columns)
	binary.BigEndian.PutUint16(out[3:], rows)
	return out
}

// ParseResize decodes and bounds terminal dimensions.
func ParseResize(frame []byte) (columns, rows uint16, err error) {
	if len(frame) != 5 || frame[0] != Resize {
		return 0, 0, errors.New("invalid resize frame")
	}
	columns, rows = binary.BigEndian.Uint16(frame[1:]), binary.BigEndian.Uint16(frame[3:])
	if columns == 0 || rows == 0 || columns > maxDimension || rows > maxDimension {
		return 0, 0, errors.New("terminal dimensions out of range")
	}
	return columns, rows, nil
}

// WarningFrame tells the browser how many seconds remain before a limit.
func WarningFrame(seconds uint16) []byte {
	out := []byte{Warning, 0, 0}
	binary.BigEndian.PutUint16(out[1:], seconds)
	return out
}
