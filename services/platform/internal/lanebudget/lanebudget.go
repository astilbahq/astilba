// Package lanebudget annotates a CI run whose test package goes past the time budget that
// scripts/lane-budget.mjs holds its lane to, so the pull request that takes a package there shows
// it in its own checks. The Lane budget workflow sees it only after it lands, and only as a
// median of several runs.
package lanebudget

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// RaceBudget is budgetSeconds in scripts/lane-budget.mjs, whose test holds the two together.
const RaceBudget = 600 * time.Second

// NativeShare is nativeShare in scripts/lane-budget.mjs, whose test holds the two together.
const NativeShare = 0.6

// timedKey marks a process whose package time is already being judged. A test that runs its own
// binary again, as a helper in a namespace or a process that crashes on purpose, passes its
// environment on, so the helper's own time and output are left alone: some tests read a helper's
// output, and its time is part of the test's.
const timedKey = "ASTILBA_LANE_BUDGET_TIMED"

// now and printLine are replaced by this package's tests.
var (
	now       = time.Now
	printLine = func(line string) { fmt.Println(line) }
)

// Start begins timing the package of the TestMain that calls it. The function it returns is
// called once that package's tests have run, and prints a GitHub Actions warning if they took
// longer than the lane's budget.
func Start() (done func()) {
	began := now()
	if os.Getenv(timedKey) != "" {
		return func() {}
	}
	_ = os.Setenv(timedKey, "1")
	name := callerPackage()
	return func() {
		var timeout time.Duration
		if f := flag.Lookup("test.timeout"); f != nil {
			timeout, _ = f.Value.(flag.Getter).Get().(time.Duration)
		}
		if line := warning(name, now().Sub(began), timeout, os.Getenv); line != "" {
			printLine(line)
		}
	}
}

// warning is the annotation for a package whose tests took took under a go test -timeout of
// timeout, or nothing when it is within its budget or this is not a GitHub Actions run. A native lane holds
// each package to a share of its go test command's -timeout, and a Go race lane holds every
// package to one budget. A native run is one that runs the native tests, which every native
// lane's command enables and no Go race lane's does.
func warning(name string, took, timeout time.Duration, getenv func(string) string) string {
	if getenv("GITHUB_ACTIONS") != "true" {
		return ""
	}
	lane, budget := "Go race lane", RaceBudget
	if getenv("ASTILBA_NATIVE_AGENT") == "1" {
		if timeout <= 0 {
			return ""
		}
		lane, budget = "Native lane", time.Duration(float64(timeout)*NativeShare)
	}
	if took <= budget {
		return ""
	}
	stops := "go test sets no timeout"
	if timeout > 0 {
		stops = fmt.Sprintf("go test stops at %.0fs", timeout.Seconds())
	}
	return fmt.Sprintf("::warning title=%s past its budget::%s took %.0fs against a %.0fs budget, and %s. See docs/continuous-integration.md.", lane, name, took.Seconds(), budget.Seconds(), stops)
}

// callerPackage is the import path of the package whose TestMain called Start, as go test prints
// it. An external test package's name ends in _test, and go test prints the package it tests.
func callerPackage() string {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return "this package"
	}
	function := runtime.FuncForPC(pc).Name()
	slash := strings.LastIndex(function, "/")
	if dot := strings.Index(function[slash+1:], "."); dot >= 0 {
		function = function[:slash+1+dot]
	}
	return strings.TrimSuffix(function, "_test")
}
