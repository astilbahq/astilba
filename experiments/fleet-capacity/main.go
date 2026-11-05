// Command fleet-capacity measures the load connected agents put on the management application.
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
	default:
		err = fmt.Errorf("usage: fleet-capacity measure|agents|summary")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleet-capacity:", err)
		os.Exit(1)
	}
}
