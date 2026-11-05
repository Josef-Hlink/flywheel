package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// minRide is how long you have to pedal for a ride to be kept, pauses not
// counted.
const minRide = 10 * time.Second

// maxGap caps how long one sample counts for: a longer silence means the
// trainer stopped reporting, not that you held that power.
const maxGap = 5 * time.Second

// migrations[v] takes the database from schema version v to v+1. Times are
// unix milliseconds.
var migrations = []string{`
CREATE TABLE rides (
	id          INTEGER PRIMARY KEY,
	trainer     TEXT NOT NULL,
	started_at  INTEGER NOT NULL, -- first pedal stroke
	ended_at    INTEGER,          -- last pedal stroke; NULL while riding
	moving_s    REAL,
	energy_kj   REAL,
	avg_power   REAL,             -- W over moving time
	max_power   INTEGER,
	avg_cadence REAL,             -- rpm while pedalling
	distance_km REAL
);
CREATE TABLE samples (
	ride_id INTEGER NOT NULL REFERENCES rides (id) ON DELETE CASCADE,
	t       INTEGER NOT NULL,
	power   INTEGER, -- W
	cadence REAL,    -- rpm
	speed   REAL,    -- km/h
	mode    TEXT,    -- erg or resistance; NULL before the first set
	target  INTEGER  -- W in erg, % in resistance
);
CREATE INDEX samples_ride_t ON samples (ride_id, t);
`, `
-- The road model's rider weight. NULL on rides saved before it, which
-- resummarize recomputes.
ALTER TABLE rides ADD COLUMN weight_kg REAL;
`}

// openDB opens the rides database, creating it on first use.
func openDB() (*sql.DB, error) {
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}
	return openDBAt(filepath.Join(dir, "rides.db"))
}

func openDBAt(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if version > len(migrations) {
		db.Close()
		return nil, fmt.Errorf("%s is from a newer flywheel (schema %d)", path, version)
	}
	for ; version < len(migrations); version++ {
		// PRAGMA takes no parameters; version is ours.
		m := migrations[version] + fmt.Sprintf("PRAGMA user_version = %d;", version+1)
		if err := inTx(db, m); err != nil {
			db.Close()
			return nil, fmt.Errorf("upgrade %s to schema %d: %w", path, version+1, err)
		}
	}
	return db, nil
}

func inTx(db *sql.DB, script string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(script); err != nil {
		return err
	}
	return tx.Commit()
}

// sample is one Indoor Bike Data notification and the target at the time.
type sample struct {
	T       time.Time
	Power   *int16
	Cadence *float64
	Speed   *float64
	Mode    string
	Target  int
}

// pedalling goes by power alone: it drops to 0 the moment you stop, while
// the trainer's cadence estimate lags a few seconds behind both your first
// and last strokes, and its speed coasts for twenty.
func (s sample) pedalling() bool {
	return s.Power != nil && *s.Power > 0
}

type rideSummary struct {
	ID         int64         `json:"id,omitempty"` // 0 until saved
	Start      time.Time     `json:"start"`
	Duration   time.Duration `json:"duration"` // first to last pedal stroke
	Moving     time.Duration `json:"moving"`
	EnergyKJ   float64       `json:"energy_kj"`
	AvgPower   float64       `json:"avg_power"`
	MaxPower   int           `json:"max_power"`
	AvgCadence float64       `json:"avg_cadence"`
	DistanceKM float64       `json:"distance_km"` // on the road model
	WeightKG   float64       `json:"weight_kg"`   // the rider's, for the road model
}

// summarize holds each value until the next sample, so a field the trainer
// left out of one notification keeps its last value. Distance comes from
// the road model for a rider of riderKG, starting from a standstill.
func summarize(ss []sample, riderKG float64) rideSummary {
	if len(ss) == 0 {
		return rideSummary{}
	}
	sum := rideSummary{Start: ss[0].T, Duration: ss[len(ss)-1].T.Sub(ss[0].T), WeightKG: riderKG}
	var power, cadence, v float64
	var joules, metres, revs, cadenceSecs float64
	for i, s := range ss {
		if i > 0 {
			dt := min(s.T.Sub(ss[i-1].T), maxGap)
			secs := dt.Seconds()
			if power > 0 {
				sum.Moving += dt
				if cadence > 0 {
					revs += cadence * secs
					cadenceSecs += secs
				}
			}
			joules += power * secs
			next := roadStep(v, power, riderKG, dt)
			metres += (v + next) / 2 * secs
			v = next
		}
		if s.Power != nil {
			power = float64(*s.Power)
			sum.MaxPower = max(sum.MaxPower, int(*s.Power))
		}
		if s.Cadence != nil {
			cadence = *s.Cadence
		}
	}
	sum.EnergyKJ = joules / 1000
	sum.DistanceKM = metres / 1000
	if sum.Moving > 0 {
		sum.AvgPower = joules / sum.Moving.Seconds()
	}
	if cadenceSecs > 0 {
		sum.AvgCadence = revs / cadenceSecs
	}
	return sum
}

// recorder turns the bike data of one connection into at most one ride: from
// the first pedal stroke to the last, pauses included. The ride reaches the
// database once you've pedalled for minRide, and its new samples every flush
// after that, so a crash loses seconds, not the ride.
type recorder struct {
	db      *sql.DB
	trainer string
	riderKG float64

	mu       sync.Mutex // guards the fields below; never held across a db call
	samples  []sample   // the ride so far, from its first pedal stroke
	last     int        // index of the last pedal stroke
	finished bool

	flushMu sync.Mutex // serialises flush and finish; guards the fields below
	id      int64      // 0 until the ride is in the database
	written int        // samples[:written] are in the database
	saved   *rideSummary
}

func newRecorder(db *sql.DB, trainer string, riderKG float64) *recorder {
	return &recorder{db: db, trainer: trainer, riderKG: riderKG}
}

// add takes a sample. It never touches the database, so it's safe from BLE
// callbacks.
func (r *recorder) add(s sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished || (len(r.samples) == 0 && !s.pedalling()) {
		return
	}
	r.samples = append(r.samples, s)
	if s.pedalling() {
		r.last = len(r.samples) - 1
	}
}

// live summarises the ride so far, or returns nil before the first stroke.
func (r *recorder) live() *rideSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) == 0 {
		return nil
	}
	sum := summarize(r.samples[:r.last+1], r.riderKG)
	return &sum
}

// flush writes the samples up to the last pedal stroke. The ones after it
// wait: they're a pause if you pedal again, and dropped if you don't.
func (r *recorder) flush() error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	return r.flushLocked()
}

func (r *recorder) flushLocked() error {
	r.mu.Lock()
	var todo []sample
	if r.id != 0 || (len(r.samples) > 0 && summarize(r.samples[:r.last+1], r.riderKG).Moving >= minRide) {
		// add only appends, so these elements won't change under us.
		todo = r.samples[r.written : r.last+1]
	}
	r.mu.Unlock()
	if len(todo) == 0 {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := r.id
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO rides (trainer, started_at, weight_kg) VALUES (?, ?, ?)`,
			r.trainer, todo[0].T.UnixMilli(), r.riderKG)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO samples (ride_id, t, power, cadence, speed, mode, target) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for _, s := range todo {
		var mode, target any
		if s.Mode != "" {
			mode, target = s.Mode, s.Target
		}
		if _, err := stmt.Exec(id, s.T.UnixMilli(), s.Power, s.Cadence, s.Speed, mode, target); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if r.id == 0 {
		log.Printf("recording ride %d", id)
	}
	r.id = id
	r.written += len(todo)
	return nil
}

// finish ends the ride and saves it if it was long enough. The summary is
// nil if you never pedalled, and has no ID if the ride was too short to
// keep. Calling it again retries a failed save.
func (r *recorder) finish() (*rideSummary, error) {
	r.mu.Lock()
	r.finished = true
	var sum *rideSummary
	if len(r.samples) > 0 {
		s := summarize(r.samples[:r.last+1], r.riderKG)
		sum = &s
	}
	r.mu.Unlock()

	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	if r.saved != nil {
		return r.saved, nil
	}
	if err := r.flushLocked(); err != nil {
		return sum, fmt.Errorf("save ride: %w", err)
	}
	if r.id == 0 {
		return sum, nil
	}
	sum.ID = r.id
	if err := saveSummary(r.db, *sum); err != nil {
		return sum, fmt.Errorf("save ride: %w", err)
	}
	r.saved = sum
	log.Printf("saved ride %d: %s", sum.ID, describeRide(*sum))
	return sum, nil
}

func saveSummary(db *sql.DB, s rideSummary) error {
	_, err := db.Exec(`UPDATE rides SET ended_at = ?, moving_s = ?, energy_kj = ?, avg_power = ?,
		max_power = ?, avg_cadence = ?, distance_km = ?, weight_kg = ? WHERE id = ?`,
		s.Start.Add(s.Duration).UnixMilli(), s.Moving.Seconds(), s.EnergyKJ, s.AvgPower,
		s.MaxPower, s.AvgCadence, s.DistanceKM, s.WeightKG, s.ID)
	return err
}

// resummarize recomputes rides from their samples: those saved before the
// road model, for a rider of riderKG, and with open, those a crash left
// open. Only the background process may pass open, as an open ride may be
// in progress.
func resummarize(db *sql.DB, riderKG float64, open bool) error {
	rows, err := db.Query(`SELECT id, coalesce(weight_kg, ?) FROM rides
		WHERE (ended_at IS NOT NULL AND weight_kg IS NULL) OR (? AND ended_at IS NULL)`,
		riderKG, open)
	if err != nil {
		return err
	}
	type todo struct {
		id int64
		kg float64
	}
	var rides []todo
	for rows.Next() {
		var r todo
		if err := rows.Scan(&r.id, &r.kg); err != nil {
			rows.Close()
			return err
		}
		rides = append(rides, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rides {
		id := r.id
		ss, err := loadSamples(db, id)
		if err != nil {
			return err
		}
		if len(ss) == 0 {
			if _, err := db.Exec(`DELETE FROM rides WHERE id = ?`, id); err != nil {
				return err
			}
			continue
		}
		sum := summarize(ss, r.kg)
		sum.ID = id
		if err := saveSummary(db, sum); err != nil {
			return err
		}
		log.Printf("summarised ride %d again: %s", id, describeRide(sum))
	}
	return nil
}

func loadSamples(db *sql.DB, rideID int64) ([]sample, error) {
	rows, err := db.Query(`SELECT t, power, cadence, speed, coalesce(mode, ''), coalesce(target, 0)
		FROM samples WHERE ride_id = ? ORDER BY t`, rideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ss []sample
	for rows.Next() {
		var s sample
		var ms int64
		if err := rows.Scan(&ms, &s.Power, &s.Cadence, &s.Speed, &s.Mode, &s.Target); err != nil {
			return nil, err
		}
		s.T = time.UnixMilli(ms)
		ss = append(ss, s)
	}
	return ss, rows.Err()
}

func listRides(db *sql.DB, n int) ([]rideSummary, error) {
	rows, err := db.Query(`SELECT id, started_at, ended_at, moving_s, energy_kj, avg_power,
		max_power, avg_cadence, distance_km
		FROM rides WHERE ended_at IS NOT NULL ORDER BY started_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rideSummary
	for rows.Next() {
		var s rideSummary
		var start, end int64
		var moving float64
		err := rows.Scan(&s.ID, &start, &end, &moving, &s.EnergyKJ, &s.AvgPower,
			&s.MaxPower, &s.AvgCadence, &s.DistanceKM)
		if err != nil {
			return nil, err
		}
		s.Start = time.UnixMilli(start)
		s.Duration = time.Duration(end-start) * time.Millisecond
		s.Moving = time.Duration(moving * float64(time.Second))
		out = append(out, s)
	}
	slices.Reverse(out) // newest last, next to the prompt
	return out, rows.Err()
}

func ridesCmd(args []string) error {
	fs := flag.NewFlagSet("rides", flag.ExitOnError)
	n := fs.Int("n", 20, "how many of the latest rides to show")
	fs.Parse(args)

	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	r, err := loadRider()
	if err != nil {
		return err
	}
	if err := resummarize(db, r.weight(), false); err != nil {
		return err
	}
	rides, err := listRides(db, *n)
	if err != nil {
		return err
	}
	if len(rides) == 0 {
		fmt.Println("no rides yet")
		return nil
	}
	fmt.Printf("%4s  %-16s  %7s  %7s  %5s  %5s  %5s  %3s  %5s\n", "", "", "moving", "elapsed", "avg W", "max W", "kJ", "rpm", "km")
	for _, r := range rides {
		fmt.Printf("%4d  %-16s  %7s  %7s  %5.0f  %5d  %5.0f  %3.0f  %5.1f\n",
			r.ID, r.Start.Format("Mon 2 Jan 15:04"), clock(r.Moving), clock(r.Duration),
			r.AvgPower, r.MaxPower, r.EnergyKJ, r.AvgCadence, r.DistanceKM)
	}
	return nil
}

// describeRide is a one-line summary, led by moving time.
func describeRide(s rideSummary) string {
	line := clock(s.Moving)
	if paused := s.Duration - s.Moving; paused >= time.Second {
		line += fmt.Sprintf(" (%s elapsed)", clock(s.Duration))
	}
	return line + fmt.Sprintf(", avg %.0f W, max %d W, %.0f kJ, %.0f rpm, %.1f km",
		s.AvgPower, s.MaxPower, s.EnergyKJ, s.AvgCadence, s.DistanceKM)
}

// clock formats a duration like a bike computer: 4:05, or 1:04:05.
func clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
