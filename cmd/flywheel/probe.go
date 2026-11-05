package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// probe is a hidden diagnostic: connect, dump what the trainer supports and
// what it reports. Only -w or -send take control and change resistance.
func probe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	name := fs.String("n", "", "trainer name, or part of it (default: strongest named FTMS trainer)")
	scanFor := fs.Duration("t", 5*time.Second, "how long to scan")
	watts := fs.Int("w", 0, "take control, start, and set this ERG target power")
	send := fs.String("send", "", "take control and write these comma-separated hex frames to the control point")
	listen := fs.Duration("l", 10*time.Second, "how long to listen to the trainer")
	fs.Parse(args)

	var frames [][]byte
	if *send != "" {
		for f := range strings.SplitSeq(*send, ",") {
			b, err := hex.DecodeString(strings.ReplaceAll(f, " ", ""))
			if err != nil || len(b) == 0 {
				return fmt.Errorf("-send: bad frame %q", f)
			}
			frames = append(frames, b)
		}
	}
	if *watts < 0 || *watts > 2000 {
		return fmt.Errorf("-w: %d W is out of range", *watts)
	}

	start := time.Now()
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Printf("%6.1fs  %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, a...))
	}

	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("enable bluetooth: %w", err)
	}
	adapter.SetConnectionStateHandler(func(_ bluetooth.Device, s bluetooth.ConnectionState) {
		if !s.Connected {
			logf("disconnected (%v)", s.Err)
		}
	})

	logf("scanning %s", *scanFor)
	c, err := findTrainer(adapter, *name, "", *scanFor)
	if err != nil {
		return err
	}
	logf("picked %s", c)

	t, err := connectTrainer(adapter, c)
	if err != nil {
		return err
	}
	defer t.close()
	logf("connected")

	for u := range t.chars {
		logf("characteristic %s", uuidName(u))
	}

	if b, err := t.read(bluetooth.CharacteristicUUIDFitnessMachineFeature); err != nil {
		logf("features: %v", err)
	} else if f, err := parseFeatures(b); err != nil {
		logf("features: %v", err)
	} else {
		logf("machine features: %s  [% x]", flagNames(f.Machine, machineFeatureNames), b[:4])
		logf("target features:  %s  [% x]", flagNames(f.Target, targetFeatureNames), b[4:])
	}
	if b, err := t.read(bluetooth.CharacteristicUUIDSupportedPowerRange); err != nil {
		logf("power range: %v", err)
	} else if r, err := parseRange(b); err != nil {
		logf("power range: %v", err)
	} else {
		logf("power range: %d to %d W, step %d  [% x]", r.Min, r.Max, r.Step, b)
	}
	if b, err := t.read(bluetooth.CharacteristicUUIDSupportedResistanceLevelRange); err != nil {
		logf("resistance range: %v", err)
	} else if r, err := parseRange(b); err != nil {
		logf("resistance range: %v", err)
	} else {
		logf("resistance range: raw %d to %d, step %d  [% x]", r.Min, r.Max, r.Step, b)
	}
	if b, err := t.read(bluetooth.CharacteristicUUIDTrainingStatus); err == nil {
		logf("training status: %s  [% x]", parseTrainingStatus(b), b)
	}

	subs := []struct {
		u     bluetooth.UUID
		label string
		parse func([]byte) string
	}{
		{bluetooth.CharacteristicUUIDFitnessMachineStatus, "status", parseStatus},
		{bluetooth.CharacteristicUUIDTrainingStatus, "training", parseTrainingStatus},
		{bluetooth.CharacteristicUUIDIndoorBikeData, "bike", func(b []byte) string {
			d, err := parseBikeData(b)
			if err != nil {
				return err.Error()
			}
			return d.String()
		}},
	}
	for _, s := range subs {
		if !t.has(s.u) {
			continue
		}
		err := t.subscribe(s.u, func(b []byte) {
			logf("%-8s %s  [% x]", s.label, s.parse(b), b)
		})
		if err != nil {
			logf("%v", err)
		}
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()

	if *watts > 0 || len(frames) > 0 {
		cmds := [][]byte{{opRequestControl}}
		if *watts > 0 {
			cmds = append(cmds, []byte{opStartResume}, encodePower(int16(*watts)))
		}
		cmds = append(cmds, frames...)

		controlled := false
		for _, cmd := range cmds {
			resp, err := t.control(cmd...)
			if err != nil {
				logf("control  % x -> [% x] %v", cmd, resp, err)
				break
			}
			logf("control  % x -> [% x] %s ok", cmd, resp, opName(cmd[0]))
			controlled = true
		}
		if controlled {
			// Leave the trainer as we found it rather than stuck in ERG.
			defer func() {
				resp, err := t.control(opReset)
				if err != nil {
					logf("control  01 -> [% x] %v", resp, err)
				} else {
					logf("control  01 -> [% x] reset ok", resp)
				}
			}()
		}
	}

	logf("listening %s (ctrl-c to stop)", *listen)
	select {
	case <-time.After(*listen):
	case <-ctx.Done():
	}
	// A second ctrl-c during cleanup kills us outright.
	stopSignals()
	return nil
}
