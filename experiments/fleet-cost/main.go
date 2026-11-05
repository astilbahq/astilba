// Command fleet-cost measures what connected agents cost the management application.
package main

import (
	"fmt"
	"os"
)

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "agents":
		err = runAgents(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "measure":
		err = runMeasure(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "summary":
		err = runSummary(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "cost":
		err = runCost(os.Args[2:])
	default:
		err = fmt.Errorf("usage: fleet-cost measure|agents|summary|cost")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleet-cost:", err)
		os.Exit(1)
	}
}
