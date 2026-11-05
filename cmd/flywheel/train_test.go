package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReplTarget(t *testing.T) {
	tests := []struct {
		in, mode  string
		target    int
		wantMode  string
		wantValue int
		wantErr   bool
	}{
		{"250w", "", 0, modeERG, 250, false},
		{"30%", modeERG, 250, modeResistance, 30, false},
		{"220", "", 0, modeERG, 220, false},
		{"40", modeResistance, 30, modeResistance, 40, false},
		{"+10", modeERG, 250, modeERG, 260, false},
		{"-5", modeResistance, 30, modeResistance, 25, false},
		{"+10", "", 0, "", 0, true},
		{"+10w", modeERG, 250, "", 0, true},
		{"fast", modeERG, 250, "", 0, true},
	}
	for _, tt := range tests {
		mode, value, err := replTarget(tt.in, tt.mode, tt.target)
		if (err != nil) != tt.wantErr || mode != tt.wantMode || value != tt.wantValue {
			t.Errorf("replTarget(%q, %q, %d) = %q, %d, %v", tt.in, tt.mode, tt.target, mode, value, err)
		}
	}
}

// fakeDaemon answers status and set on the socket like the background
// process, with a trainer reporting 180 W at 85 rpm.
func fakeDaemon(t *testing.T) {
	// t.TempDir is too long for a unix socket path on macOS.
	dir, err := os.MkdirTemp("", "fw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	ln, err := net.Listen("unix", socketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	st := status{Trainer: "KICKR", Since: time.Now()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req request
			json.NewDecoder(conn).Decode(&req)
			mu.Lock()
			var resp response
			switch req.Cmd {
			case "status":
			case "set":
				if req.Value < 0 {
					resp.Error = fmt.Sprintf("%d is out of range", req.Value)
				} else {
					st.Mode, st.Target = req.Mode, req.Value
				}
			}
			p, c, s := int16(180), 85.0, 30.0
			st.Power, st.Cadence, st.Speed, st.Updated = &p, &c, &s, time.Now()
			st.Ride = &rideSummary{Duration: 75 * time.Second, Moving: time.Minute}
			cp := st
			resp.Status = &cp
			mu.Unlock()
			json.NewEncoder(conn).Encode(resp)
			conn.Close()
		}
	}()
}

type script []string

func (s *script) ReadLine() (string, error) {
	if len(*s) == 0 {
		return "", io.EOF
	}
	line := (*s)[0]
	*s = (*s)[1:]
	return line, nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func TestRepl(t *testing.T) {
	fakeDaemon(t)
	in := script{"", "200", "+10", " 30% ", "-50", "-5", "s", "warp", "q", "never read"}
	var out syncBuffer
	if err := repl(&in, &out); err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 {
		t.Errorf("q didn't stop the prompt: %q left", in)
	}
	got := out.b.String()
	for _, want := range []string{
		"set ERG 200 W\n",
		"set ERG 210 W\n",
		"set resistance 30%\n",
		"-20 is out of range\n",
		"set resistance 25%\n",
		"ride 1:00 (1:15 elapsed)",
		`"warp": not a command`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestFeed(t *testing.T) {
	fakeDaemon(t)
	var out syncBuffer
	done := make(chan struct{})
	go func() {
		time.Sleep(600 * time.Millisecond)
		close(done)
	}()
	feed(&out, done)
	got := out.b.String()
	if want := "   1:00  none set          180 W    85 rpm   30.0 km/h\n"; !strings.Contains(got, want) {
		t.Errorf("feed printed %q, want lines like %q", got, want)
	}
}
