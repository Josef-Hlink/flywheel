// Command flywheel controls a Bluetooth smart trainer (FTMS) from the terminal.
package main

import (
	"fmt"
	"os"
)

const usage = `usage: flywheel <command> [flags]

commands:
  scan          list nearby trainers that speak FTMS
  connect       connect in the background and take control of the trainer
  status [-w]   show the connection, mode and live power/cadence (-w: keep watching)
  set 250W      ERG mode: hold 250 W whatever your cadence or gear
  set 30%       resistance mode: fixed resistance, shift to change effort
  disconnect    release the trainer and stop the background process
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
	case "connect":
		err = connectCmd(os.Args[2:])
	case "status":
		err = statusCmd(os.Args[2:])
	case "set":
		err = setCmd(os.Args[2:])
	case "disconnect":
		err = disconnectCmd(os.Args[2:])
	case "probe": // hidden diagnostic, not in usage
		err = probe(os.Args[2:])
	case "_daemon": // spawned by connect
		err = serveDaemon(os.Args[2:])
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
