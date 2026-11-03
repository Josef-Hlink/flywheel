// Command flywheel controls a Bluetooth smart trainer (FTMS) from the terminal.
package main

import (
	"fmt"
	"os"
)

const usage = `usage: flywheel <command> [flags]

commands:
  scan    list nearby trainers that speak FTMS
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "scan":
		err = scan(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "flywheel: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel: %v\n", err)
		os.Exit(1)
	}
}
