// Command flywheel controls a Bluetooth smart trainer (FTMS) from the terminal.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

// version is set at release time with -ldflags.
var version = "dev"

// versionString falls back on the module version that go install records.
func versionString() string {
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
	}
	return version
}

const usage = `usage: flywheel <command> [flags]

commands:
  scan          list nearby trainers that speak FTMS
  connect       connect in the background and take control of the trainer
  status [-w]   show the connection, mode and live power/cadence (-w: keep watching)
  set 250W      ERG mode: hold 250 W whatever your cadence or gear
  set 30%       resistance mode: fixed resistance, shift to change effort
  disconnect    release the trainer and stop the background process
  train         ride at a prompt: type 250w, 30% or +10 while your data scrolls by
                (connects first if need be, and then disconnects when you leave)
  rides [-n 20] list your latest rides
  weight [kg]   show or set your weight, for road speed (default 75 kg)
  version       print flywheel's version

Every ride with over 10 seconds of pedalling is saved to
~/.local/share/flywheel/rides.db (under $XDG_DATA_HOME if that's set): one
ride per connection, from your first pedal stroke to your last.

Speed and distance come from your power on a flat road, as in riding apps;
the trainer's own speed only follows your cadence.
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
	case "train":
		err = trainCmd(os.Args[2:])
	case "rides":
		err = ridesCmd(os.Args[2:])
	case "weight":
		err = weightCmd(os.Args[2:])
	case "probe": // hidden diagnostic, not in usage
		err = probe(os.Args[2:])
	case "_daemon": // spawned by connect
		err = serveDaemon(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("flywheel", versionString())
		return
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
