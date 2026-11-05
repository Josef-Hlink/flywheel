package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var errNotConnected = errors.New("not connected (run flywheel connect)")

// call sends one request to the background process.
func call(req request) (*status, error) {
	conn, err := net.DialTimeout("unix", socketPath(), time.Second)
	if err != nil {
		return nil, errNotConnected
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("no reply from background process: %w", err)
	}
	if resp.Error != "" {
		return resp.Status, errors.New(resp.Error)
	}
	return resp.Status, nil
}

// connectOpts are the flags of connect, which train shares.
type connectOpts struct {
	name    string
	scanFor time.Duration
	idle    time.Duration
}

func (o *connectOpts) flags(fs *flag.FlagSet) {
	fs.StringVar(&o.name, "n", "", "trainer name, or part of it (default: the last one, else the strongest)")
	fs.DurationVar(&o.scanFor, "t", 10*time.Second, "how long to scan")
	fs.DurationVar(&o.idle, "idle", 5*time.Minute, "disconnect after this long without pedalling")
}

func connectCmd(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	var o connectOpts
	o.flags(fs)
	fs.Parse(args)

	if st, err := call(request{Cmd: "status"}); err == nil {
		fmt.Printf("already connected to %s\n", st.Trainer)
		return nil
	}
	return connect(o)
}

// connect starts the background process and waits for it to connect.
func connect(o connectOpts) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	logPath := filepath.Join(dir, "daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()

	cmd := exec.Command(exe, "_daemon", "-n", o.name, "-t", o.scanFor.String(), "-idle", o.idle.String())
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.ExtraFiles = []*os.File{w} // fd 3: the ready line
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		w.Close()
		return err
	}
	w.Close()
	cmd.Process.Release()

	// Scanning, connecting and discovery take up to ~30s when it goes badly.
	r.SetReadDeadline(time.Now().Add(o.scanFor + 45*time.Second))
	line, err := bufio.NewReader(r).ReadString('\n')
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "ok "):
		fmt.Printf("connected to %s; disconnects after %s without pedalling\n", strings.TrimPrefix(line, "ok "), o.idle)
		return nil
	case strings.HasPrefix(line, "error "):
		return errors.New(strings.TrimPrefix(line, "error "))
	case errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("background process didn't report back; see %s", logPath)
	default:
		return fmt.Errorf("background process exited; see %s", logPath)
	}
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	watch := fs.Bool("w", false, "keep watching on one live line, with peak power")
	fs.Parse(args)
	if *watch {
		return watchStatus()
	}

	st, err := call(request{Cmd: "status"})
	if err != nil {
		return err
	}
	fmt.Printf("%s, connected %s\n", st.Trainer, time.Since(st.Since).Round(time.Second))
	fmt.Printf("mode     %s\n", describeMode(st.Mode, st.Target))
	if st.Ride != nil {
		fmt.Printf("ride     %s\n", describeRide(*st.Ride))
	}
	if age := time.Since(st.Updated); st.Updated.IsZero() || age > 5*time.Second {
		fmt.Printf("no data from the trainer for %s\n", age.Round(time.Second))
		return nil
	}
	if st.Power != nil {
		fmt.Printf("power    %d W\n", *st.Power)
	}
	if st.Cadence != nil {
		fmt.Printf("cadence  %.0f rpm\n", *st.Cadence)
	}
	if st.Speed != nil {
		fmt.Printf("speed    %.1f km/h\n", *st.Speed)
	}
	return nil
}

// watchStatus redraws one status line until ctrl-c. The trainer reports
// about once a second; polling at 4 Hz catches every sample for the peak.
func watchStatus() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	var peak int16
	for {
		st, err := call(request{Cmd: "status"})
		if err != nil {
			fmt.Println()
			return err
		}
		if st.Power != nil && fresh(st) {
			peak = max(peak, *st.Power)
		}
		fmt.Printf("\r\033[K%s   %s   peak %d W", describeMode(st.Mode, st.Target), liveData(st), peak)

		select {
		case <-ctx.Done():
			fmt.Printf("\npeak %d W\n", peak)
			return nil
		case <-tick.C:
		}
	}
}

// fresh reports whether the trainer's data in st is recent enough to show.
func fresh(st *status) bool {
	return !st.Updated.IsZero() && time.Since(st.Updated) <= 5*time.Second
}

// liveData describes the trainer's latest power, cadence and speed.
func liveData(st *status) string {
	if !fresh(st) {
		return fmt.Sprintf("no data for %s", time.Since(st.Updated).Round(time.Second))
	}
	var parts []string
	if st.Power != nil {
		parts = append(parts, fmt.Sprintf("%4d W", *st.Power))
	}
	if st.Cadence != nil {
		parts = append(parts, fmt.Sprintf("%3.0f rpm", *st.Cadence))
	}
	if st.Speed != nil {
		parts = append(parts, fmt.Sprintf("%4.1f km/h", *st.Speed))
	}
	return strings.Join(parts, "   ")
}

func describeMode(mode string, target int) string {
	switch mode {
	case modeERG:
		return fmt.Sprintf("ERG %d W", target)
	case modeResistance:
		return fmt.Sprintf("resistance %d%%", target)
	}
	return "none set"
}

func setCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: flywheel set 250W | 30%")
	}
	mode, value, err := parseTarget(args[0])
	if err != nil {
		return err
	}
	st, err := call(request{Cmd: "set", Mode: mode, Value: value})
	if err != nil {
		return err
	}
	fmt.Println(describeMode(st.Mode, st.Target))
	return nil
}

// parseTarget reads "250W" as ERG target power and "30%" as resistance.
func parseTarget(s string) (mode string, value int, err error) {
	s = strings.TrimSpace(s)
	num := s
	switch {
	case strings.HasSuffix(s, "W"), strings.HasSuffix(s, "w"):
		mode, num = modeERG, s[:len(s)-1]
	case strings.HasSuffix(s, "%"):
		mode, num = modeResistance, s[:len(s)-1]
	default:
		return "", 0, fmt.Errorf("%q: give a unit, like 250W (ERG) or 30%% (resistance)", s)
	}
	value, err = strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return "", 0, fmt.Errorf("%q: not a whole number", s)
	}
	return mode, value, nil
}

func disconnectCmd(args []string) error {
	return disconnect()
}

// disconnect releases the trainer and reports the ride it saved.
func disconnect() error {
	st, err := call(request{Cmd: "disconnect"})
	if st == nil {
		return err
	}
	// Wait for the socket to go so an immediate connect doesn't see it.
	for range 30 {
		if _, err := os.Stat(socketPath()); err != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Printf("disconnected from %s\n", st.Trainer)
	switch {
	case err != nil:
		// The background process retries on its way out; see its log.
	case st.Ride == nil:
	case st.Ride.ID == 0:
		fmt.Printf("not saved: rides with under %s of pedalling aren't kept\n", minRide)
	default:
		fmt.Printf("saved ride %d: %s\n", st.Ride.ID, describeRide(*st.Ride))
	}
	return err
}
