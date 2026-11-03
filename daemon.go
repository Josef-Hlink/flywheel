package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"tinygo.org/x/bluetooth"
)

// daemon holds the BLE link to the trainer and serves the unix socket. The
// trainer only follows targets while connected, and reconnecting takes
// seconds, so this outlives the commands that talk to it.
type daemon struct {
	t      *trainer
	idle   time.Duration
	power  valueRange
	resist *valueRange // nil if the trainer doesn't report one
	quit   chan string

	// ctl serialises control point use. BLE callbacks never take it: they
	// run on the CoreBluetooth delegate, which also delivers the control
	// point responses that a ctl holder waits for.
	ctl      sync.Mutex
	started  bool
	released bool // control handed back via reset

	mu      sync.Mutex // guards the fields below; held only briefly
	mode    string
	target  int
	bike    bikeData
	updated time.Time
	active  time.Time // last pedalling or set
	since   time.Time
}

// serveDaemon is the hidden subcommand connect spawns. It reports success or
// failure on fd 3, then keeps running until disconnect, idle or signal.
func serveDaemon(args []string) error {
	fs := flag.NewFlagSet("_daemon", flag.ExitOnError)
	name := fs.String("n", "", "trainer name")
	scanFor := fs.Duration("t", 10*time.Second, "how long to scan")
	idle := fs.Duration("idle", 5*time.Minute, "disconnect after this long without pedalling")
	fs.Parse(args)

	log.SetFlags(log.LstdFlags)
	ready := os.NewFile(3, "ready")
	d := &daemon{idle: *idle, quit: make(chan string, 1)}
	ln, err := d.start(*name, *scanFor)
	if err != nil {
		fmt.Fprintf(ready, "error %v\n", err)
		ready.Close()
		return err
	}
	fmt.Fprintf(ready, "ok %s\n", d.t.name)
	ready.Close()
	return d.run(ln)
}

func (d *daemon) start(name string, scanFor time.Duration) (_ net.Listener, err error) {
	addr := ""
	if name == "" {
		saved, err := loadTrainer()
		if err != nil {
			log.Printf("ignoring saved trainer: %v", err)
		}
		name, addr = saved.Name, saved.Address
	}

	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return nil, fmt.Errorf("enable bluetooth: %w", err)
	}
	adapter.SetConnectionStateHandler(func(_ bluetooth.Device, s bluetooth.ConnectionState) {
		if !s.Connected {
			d.stop(fmt.Sprintf("trainer disconnected (%v)", s.Err))
		}
	})

	c, err := findTrainer(adapter, name, addr, scanFor)
	if err != nil {
		return nil, err
	}
	log.Printf("connecting to %s", c)
	t, err := connectTrainer(adapter, c)
	if err != nil {
		return nil, err
	}
	d.t = t
	defer func() {
		if err != nil {
			t.close()
		}
	}()

	if b, err := t.read(bluetooth.CharacteristicUUIDSupportedPowerRange); err == nil {
		d.power, err = parseRange(b)
		if err != nil {
			return nil, err
		}
	} else {
		d.power = valueRange{Min: 0, Max: 2000, Step: 1}
		log.Printf("assuming power range 0-2000 W: %v", err)
	}
	if b, err := t.read(bluetooth.CharacteristicUUIDSupportedResistanceLevelRange); err == nil {
		r, err := parseRange(b)
		if err != nil {
			return nil, err
		}
		d.resist = &r
	}

	if _, err := t.control(opRequestControl); err != nil {
		return nil, err
	}

	if err := t.subscribe(bluetooth.CharacteristicUUIDIndoorBikeData, d.onBikeData); err != nil {
		return nil, err
	}
	if t.has(bluetooth.CharacteristicUUIDFitnessMachineStatus) {
		err := t.subscribe(bluetooth.CharacteristicUUIDFitnessMachineStatus, func(b []byte) {
			log.Printf("status: %s [% x]", parseStatus(b), b)
			if len(b) > 0 && b[0] == 0xFF {
				d.stop("another app took control of the trainer")
			}
		})
		if err != nil {
			log.Print(err)
		}
	}

	if err := saveTrainer(savedTrainer{Name: c.name, Address: c.addr.String()}); err != nil {
		log.Printf("remember trainer: %v", err)
	}

	sock := socketPath()
	os.Remove(sock) // connect already checked nobody answers on it
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	now := time.Now()
	d.since, d.active = now, now
	log.Printf("connected to %s, power %d-%d W, idle timeout %s", c.name, d.power.Min, d.power.Max, d.idle)
	return ln, nil
}

// stop asks run to shut down. Safe from BLE callbacks: it never blocks.
func (d *daemon) stop(reason string) {
	select {
	case d.quit <- reason:
	default:
	}
}

func (d *daemon) onBikeData(b []byte) {
	bd, err := parseBikeData(b)
	if err != nil {
		log.Print(err)
		return
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bike, d.updated = bd, now
	// Power drops to 0 the moment you stop pedalling; speed coasts down for
	// seconds and cadence lags, so power and cadence decide.
	if (bd.Power != nil && *bd.Power > 0) || (bd.Cadence != nil && *bd.Cadence > 0) {
		d.active = now
	}
}

func (d *daemon) run(ln net.Listener) error {
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(conn)
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	var reason string
	for reason == "" {
		select {
		case reason = <-d.quit:
		case s := <-sigs:
			reason = "got " + s.String()
		case <-tick.C:
			d.mu.Lock()
			quiet := time.Since(d.active)
			d.mu.Unlock()
			if quiet > d.idle {
				reason = fmt.Sprintf("no pedalling for %s", d.idle)
			}
		}
	}

	log.Printf("shutting down: %s", reason)
	ln.Close()
	os.Remove(socketPath())
	d.ctl.Lock()
	if !d.released {
		if ok, _ := d.t.dev.Connected(); ok {
			if _, err := d.t.control(opReset); err != nil {
				log.Printf("reset: %v", err)
			}
		}
	}
	d.t.close()
	return nil
}

func (d *daemon) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	st, err := d.handle(req)
	resp := response{Status: st}
	if err != nil {
		resp.Error = err.Error()
	}
	json.NewEncoder(conn).Encode(resp)
	if req.Cmd == "disconnect" && err == nil {
		d.stop("disconnect requested")
	}
}

func (d *daemon) handle(req request) (*status, error) {
	switch req.Cmd {
	case "status":
		return d.status(), nil

	case "set":
		var frame []byte
		switch req.Mode {
		case modeERG:
			if req.Value < int(d.power.Min) || req.Value > int(d.power.Max) {
				return nil, fmt.Errorf("%d W is outside the trainer's %d-%d W", req.Value, d.power.Min, d.power.Max)
			}
			frame = encodePower(int16(req.Value))
		case modeResistance:
			if d.resist == nil {
				return nil, errors.New("this trainer doesn't report a resistance range")
			}
			if req.Value < 0 || req.Value > 100 {
				return nil, fmt.Errorf("%d%% is outside 0-100%%", req.Value)
			}
			frame = encodeResistance(d.resist.fromPercent(req.Value))
		default:
			return nil, fmt.Errorf("unknown mode %q", req.Mode)
		}

		d.ctl.Lock()
		defer d.ctl.Unlock()
		if d.released {
			return nil, errors.New("disconnecting")
		}
		if !d.started {
			if _, err := d.t.control(opStartResume); err != nil {
				return nil, err
			}
			d.started = true
		}
		if _, err := d.t.control(frame...); err != nil {
			return nil, err
		}
		log.Printf("set %s %d", req.Mode, req.Value)
		d.mu.Lock()
		d.mode, d.target, d.active = req.Mode, req.Value, time.Now()
		d.mu.Unlock()
		return d.status(), nil

	case "disconnect":
		st := d.status()
		d.ctl.Lock()
		defer d.ctl.Unlock()
		if !d.released {
			if _, err := d.t.control(opReset); err != nil {
				log.Printf("reset: %v", err)
			}
			d.released = true
		}
		return st, nil
	}
	return nil, fmt.Errorf("unknown command %q", req.Cmd)
}

func (d *daemon) status() *status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &status{
		Trainer: d.t.name,
		Since:   d.since,
		Mode:    d.mode,
		Target:  d.target,
		Power:   d.bike.Power,
		Cadence: d.bike.Cadence,
		Speed:   d.bike.Speed,
		Updated: d.updated,
		Idle:    d.idle,
	}
}
