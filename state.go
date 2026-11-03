package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// socketPath is where the background process listens. On macOS TMPDIR is
// already per-user.
func socketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "flywheel.sock")
}

// stateDir holds the remembered trainer and the background process's log.
func stateDir() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "flywheel")
	return dir, os.MkdirAll(dir, 0o755)
}

// savedTrainer is the trainer the last successful connect used. The address
// alone isn't enough: on macOS it's a per-host CoreBluetooth UUID.
type savedTrainer struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

func loadTrainer() (savedTrainer, error) {
	var t savedTrainer
	dir, err := stateDir()
	if err != nil {
		return t, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "trainer.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

func saveTrainer(t savedTrainer) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "trainer.json"), append(b, '\n'), 0o644)
}

// Socket protocol: one JSON request and one JSON response per connection.

type request struct {
	Cmd   string `json:"cmd"`            // status, set, disconnect
	Mode  string `json:"mode,omitempty"` // set: erg or resistance
	Value int    `json:"value,omitempty"`
}

type response struct {
	Error  string  `json:"error,omitempty"`
	Status *status `json:"status,omitempty"`
}

const (
	modeERG        = "erg"
	modeResistance = "resistance"
)

type status struct {
	Trainer string        `json:"trainer"`
	Since   time.Time     `json:"since"`
	Mode    string        `json:"mode,omitempty"`
	Target  int           `json:"target"` // W in ERG, % in resistance mode
	Power   *int16        `json:"power,omitempty"`
	Cadence *float64      `json:"cadence,omitempty"`
	Speed   *float64      `json:"speed,omitempty"`
	Updated time.Time     `json:"updated"` // last Indoor Bike Data
	Idle    time.Duration `json:"idle"`    // auto-disconnect after this long without pedalling
}
