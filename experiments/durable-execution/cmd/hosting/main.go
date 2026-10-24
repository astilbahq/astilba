// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	fixture "github.com/astilbahq/astilba/experiments/durable-execution"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "init" && os.Args[1] != "serve") {
		fmt.Fprintln(os.Stderr, "usage: hosting init|serve")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := fixture.RunHostingFixture(ctx, os.Args[1] == "init"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
