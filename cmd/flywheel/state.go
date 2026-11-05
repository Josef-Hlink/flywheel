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
	return xdgDir("XDG_STATE_HOME", ".local/state")
}

// dataDir holds the rides database.
func dataDir() (string, error) {
	return xdgDir("XDG_DATA_HOME", ".local/share")
}

// xdgDir is flywheel's directory under $env, or under ~/fallback if that's
// unset, created if need be.
func xdgDir(env, fallback string) (string, error) {
	dir := os.Getenv(env)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, fallback)
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
	return t, loadJSON(stateDir, "trainer.json", &t)
}

func saveTrainer(t savedTrainer) error {
	return saveJSON(stateDir, "trainer.json", t)
}

// defaultWeightKG stands in until you set yours.
const defaultWeightKG = 75

// rider is what the road model needs to know about you.
type rider struct {
	WeightKG float64 `json:"weight_kg,omitempty"`
}

// weight is the rider's weight in kg, or the default if it's unset.
func (r rider) weight() float64 {
	if r.WeightKG > 0 {
		return r.WeightKG
	}
	return defaultWeightKG
}

func loadRider() (rider, error) {
	var r rider
	return r, loadJSON(dataDir, "rider.json", &r)
}

func saveRider(r rider) error {
	return saveJSON(dataDir, "rider.json", r)
}

// loadJSON reads name in dir into v, leaving v alone if there's no file.
func loadJSON(dir func() (string, error), name string, v any) error {
	d, err := dir()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(d, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func saveJSON(dir func() (string, error), name string, v any) error {
	d, err := dir()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, name), append(b, '\n'), 0o644)
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
	Speed   *float64      `json:"speed,omitempty"` // km/h on the road model, not the trainer's
	Updated time.Time     `json:"updated"`         // last Indoor Bike Data
	Idle    time.Duration `json:"idle"`            // auto-disconnect after this long without pedalling
	// Ride is the ride so far, nil before the first pedal stroke. In the
	// reply to disconnect it's the finished ride, with an ID if it was saved.
	Ride *rideSummary `json:"ride,omitempty"`
}
