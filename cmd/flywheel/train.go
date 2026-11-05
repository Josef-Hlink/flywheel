package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const trainHelp = `  250w     ERG: hold 250 W
  30%      resistance mode at 30%
  220      new target in the current mode (ERG if none yet)
  +10 -5   nudge the current target
  s        the ride so far
  q        leave (or ctrl-d)
`

// trainCmd is an interactive session: the trainer's data scrolls by, one
// line per sample, while a prompt takes new targets. It connects if need
// be, and disconnects when you leave if it did.
func trainCmd(args []string) error {
	fs := flag.NewFlagSet("train", flag.ExitOnError)
	var o connectOpts
	o.flags(fs)
	fs.Parse(args)

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("train needs a terminal")
	}

	ours := false
	if _, err := call(request{Cmd: "status"}); errors.Is(err, errNotConnected) {
		if err := connect(o); err != nil {
			return err
		}
		ours = true
	} else if err != nil {
		return err
	}
	fmt.Print(trainHelp)

	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout}, "> ")
	replErr := repl(t, t)
	term.Restore(fd, old)
	fmt.Print("\r\033[K") // the prompt

	if !ours {
		fmt.Println("still connected; flywheel disconnect releases the trainer")
		return replErr
	}
	err = disconnect()
	if errors.Is(err, errNotConnected) {
		err = nil // it went while we were at the prompt, and said so
	}
	return errors.Join(replErr, err)
}

type lineReader interface {
	ReadLine() (string, error)
}

// repl runs commands from in until q or EOF, with the trainer's data going
// to out alongside. out must be safe for concurrent writes.
func repl(in lineReader, out io.Writer) error {
	done := make(chan struct{})
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		feed(out, done)
	}()
	defer func() {
		close(done)
		<-fed // no stray line after the terminal is restored
	}()

	for {
		line, err := in.ReadLine()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		quit, err := do(out, strings.ToLower(strings.TrimSpace(line)))
		if err != nil {
			fmt.Fprintln(out, err)
		}
		if quit {
			return nil
		}
	}
}

// feed prints a line for every sample from the trainer, until done or the
// background process goes away. Of a run of samples without pedalling only
// the first is printed, so the log stays put while the flywheel coasts down
// and you're off the bike. The clock is moving time, like a bike computer's
// auto-pause.
func feed(out io.Writer, done <-chan struct{}) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var last time.Time
	wasIdle := false
	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}
		st, err := call(request{Cmd: "status"})
		if err != nil {
			logPath := "daemon.log"
			if dir, err := stateDir(); err == nil {
				logPath = filepath.Join(dir, logPath)
			}
			fmt.Fprintf(out, "lost the trainer (see %s); ctrl-d to leave\n", logPath)
			return
		}
		if st.Updated.IsZero() || st.Updated.Equal(last) {
			continue
		}
		last = st.Updated
		idle := !(sample{Power: st.Power, Cadence: st.Cadence}).pedalling()
		if idle && wasIdle {
			continue
		}
		wasIdle = idle
		moving := "-:--"
		if st.Ride != nil {
			moving = clock(st.Ride.Moving)
		}
		fmt.Fprintf(out, "%7s  %-15s  %s\n", moving, describeMode(st.Mode, st.Target), liveData(st))
	}
}

// do runs one command and reports whether to leave.
func do(out io.Writer, cmd string) (quit bool, err error) {
	switch cmd {
	case "":
		return false, nil
	case "q", "quit", "exit":
		return true, nil
	case "h", "help", "?":
		fmt.Fprint(out, trainHelp)
		return false, nil
	}

	st, err := call(request{Cmd: "status"})
	if err != nil {
		return false, err
	}
	if cmd == "s" || cmd == "status" {
		if st.Ride == nil {
			fmt.Fprintln(out, "no ride yet: it starts when you pedal")
		} else {
			fmt.Fprintf(out, "ride %s\n", describeRide(*st.Ride))
		}
		return false, nil
	}

	mode, value, err := replTarget(cmd, st.Mode, st.Target)
	if err != nil {
		return false, err
	}
	if st, err = call(request{Cmd: "set", Mode: mode, Value: value}); err != nil {
		return false, err
	}
	fmt.Fprintf(out, "set %s\n", describeMode(st.Mode, st.Target))
	return false, nil
}

// replTarget reads 250w and 30% like set does, plus a bare number (a new
// target in the current mode, ERG if none) and +N/-N (relative to the
// current target).
func replTarget(s, mode string, target int) (string, int, error) {
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		n, err := strconv.Atoi(s)
		if err != nil {
			return "", 0, fmt.Errorf("%q: nudge by a whole number, like +10 or -5", s)
		}
		if mode == "" {
			return "", 0, errors.New("nothing to nudge yet: set 200w or 30% first")
		}
		return mode, target + n, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		if mode == "" {
			mode = modeERG
		}
		return mode, n, nil
	}
	mode, value, err := parseTarget(s)
	if err != nil {
		return "", 0, fmt.Errorf("%q: not a command; h lists them", s)
	}
	return mode, value, nil
}
