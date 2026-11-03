package main

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"tinygo.org/x/bluetooth"
)

// scan lists trainers advertising the Fitness Machine Service (FTMS).
func scan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	timeout := fs.Duration("t", 5*time.Second, "how long to scan")
	fs.Parse(args)

	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("enable bluetooth: %w", err)
	}

	// Callbacks are serialised and stop once StopScan returns, so the map
	// needs no lock.
	seen := map[string]bool{}
	time.AfterFunc(*timeout, func() { adapter.StopScan() })
	err := adapter.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
		if !r.HasServiceUUID(bluetooth.ServiceUUIDFitnessMachine) {
			return
		}
		addr := r.Address.String()
		if seen[addr] {
			return
		}
		seen[addr] = true
		fmt.Printf("%-24s %s  %d dBm\n", r.LocalName(), addr, r.RSSI)
	})
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	if len(seen) == 0 {
		return errors.New("no trainers found; is it powered, and not connected to another app?")
	}
	return nil
}
