package main

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)

// ride builds samples a second apart from (power, cadence) pairs, at 36 km/h
// while pedalling.
func ride(start time.Time, pc ...[2]int) []sample {
	var ss []sample
	for i, v := range pc {
		p, c := int16(v[0]), float64(v[1])
		speed := 0.0
		if p > 0 || c > 0 {
			speed = 36
		}
		ss = append(ss, sample{T: start.Add(time.Duration(i) * time.Second), Power: &p, Cadence: &c, Speed: &speed})
	}
	return ss
}

func steady(n, power, cadence int) [][2]int {
	out := make([][2]int, n)
	for i := range out {
		out[i] = [2]int{power, cadence}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSummarize(t *testing.T) {
	// 10 s at 200 W and 90 rpm, a 5 s stop, then 5 s at 300 W and 60 rpm.
	pc := append(steady(11, 200, 90), steady(5, 0, 0)...)
	pc = append(pc, steady(6, 300, 60)...)
	s := summarize(ride(t0, pc...), 75)

	if s.Duration != 21*time.Second {
		t.Errorf("duration %s, want 21s", s.Duration)
	}
	// Values hold until the next sample: the 10 s at 200 W include the
	// interval up to the first stopped sample, and so on.
	if s.Moving != 16*time.Second {
		t.Errorf("moving %s, want 16s", s.Moving)
	}
	if want := (11*200 + 5*300) / 1000.0; !near(s.EnergyKJ, want) {
		t.Errorf("energy %v kJ, want %v", s.EnergyKJ, want)
	}
	if want := (11*200 + 5*300) / 16.0; !near(s.AvgPower, want) {
		t.Errorf("avg power %v, want %v", s.AvgPower, want)
	}
	if s.MaxPower != 300 {
		t.Errorf("max power %d, want 300", s.MaxPower)
	}
	if want := (11*90 + 5*60) / 16.0; !near(s.AvgCadence, want) {
		t.Errorf("avg cadence %v, want %v", s.AvgCadence, want)
	}
	if s.WeightKG != 75 || s.DistanceKM < 0.05 || s.DistanceKM > 0.2 {
		t.Errorf("%v km at %v kg: want 50-200 m from 16 s at 200-300 W", s.DistanceKM, s.WeightKG)
	}
}

func TestSummarizeCapsGaps(t *testing.T) {
	ss := ride(t0, steady(2, 100, 80)...)
	ss[1].T = t0.Add(time.Minute) // the trainer went quiet
	s := summarize(ss, 75)
	if s.Moving != maxGap || !near(s.EnergyKJ, 100*maxGap.Seconds()/1000) {
		t.Errorf("moving %s, energy %v kJ: a gap should count as %s", s.Moving, s.EnergyKJ, maxGap)
	}
}

func testDB(t *testing.T) *sql.DB {
	db, err := openDBAt(filepath.Join(t.TempDir(), "rides.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecorderDropsShortRides(t *testing.T) {
	db := testDB(t)
	r := newRecorder(db, "KICKR", 75)
	// Two spins two minutes apart: long, but only 7 s of pedalling.
	pc := steady(5, 150, 80)
	pc = append(pc, steady(120, 0, 0)...)
	pc = append(pc, steady(3, 150, 80)...)
	for _, s := range ride(t0, pc...) {
		r.add(s)
	}
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	sum, err := r.finish()
	if err != nil {
		t.Fatal(err)
	}
	if sum == nil || sum.ID != 0 {
		t.Errorf("finish = %+v, want a summary without an ID", sum)
	}
	if n := countRows(t, db, "rides"); n != 0 {
		t.Errorf("%d rides saved, want 0", n)
	}
}

func TestRecorderNoRide(t *testing.T) {
	r := newRecorder(testDB(t), "KICKR", 75)
	for _, s := range ride(t0, steady(30, 0, 0)...) {
		r.add(s)
	}
	if sum, err := r.finish(); sum != nil || err != nil {
		t.Errorf("finish = %+v, %v; want nothing", sum, err)
	}
}

func TestRecorderTrimsIdle(t *testing.T) {
	db := testDB(t)
	r := newRecorder(db, "KICKR", 75)
	// Idle, a 16 s ride with a 3 s pause in it, then idle again.
	pc := steady(5, 0, 0)
	pc = append(pc, steady(8, 200, 90)...)
	pc = append(pc, steady(3, 0, 0)...)
	pc = append(pc, steady(6, 250, 95)...)
	pc = append(pc, steady(20, 0, 0)...)
	ss := ride(t0, pc...)
	ss[6].Mode, ss[6].Target = modeERG, 200

	for i, s := range ss {
		r.add(s)
		if i%4 == 0 {
			if err := r.flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	live := r.live()
	sum, err := r.finish()
	if err != nil {
		t.Fatal(err)
	}
	if sum.ID == 0 {
		t.Fatal("ride not saved")
	}
	if sum.Start != ss[5].T || sum.Duration != 16*time.Second || sum.Moving != 13*time.Second {
		t.Errorf("ride %s +%s (%s moving), want %s +16s (13s moving)", sum.Start, sum.Duration, sum.Moving, ss[5].T)
	}
	if l := *live; l.ID != 0 {
		t.Errorf("live summary has ID %d", l.ID)
	} else if l.ID = sum.ID; l != *sum {
		t.Errorf("live %+v differs from saved %+v", l, *sum)
	}

	got, err := loadSamples(db, sum.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := ss[5:22] // first to last pedal stroke
	if len(got) != len(want) {
		t.Fatalf("%d samples saved, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].T.Equal(want[i].T) || *got[i].Power != *want[i].Power || got[i].Mode != want[i].Mode {
			t.Errorf("sample %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	rides, err := listRides(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rides) != 1 || rides[0].ID != sum.ID || !near(rides[0].AvgPower, sum.AvgPower) ||
		rides[0].Duration != sum.Duration || !rides[0].Start.Equal(sum.Start) {
		t.Errorf("listRides = %+v, want [%+v]", rides, *sum)
	}

	// Samples after finish don't start another ride.
	for _, s := range ride(t0.Add(time.Hour), steady(20, 200, 90)...) {
		r.add(s)
	}
	if again, err := r.finish(); err != nil || again.ID != sum.ID {
		t.Errorf("second finish = %+v, %v", again, err)
	}
	if n := countRows(t, db, "rides"); n != 1 {
		t.Errorf("%d rides, want 1", n)
	}
}

func TestRecoverRides(t *testing.T) {
	db := testDB(t)
	r := newRecorder(db, "KICKR", 68)
	ss := ride(t0, steady(15, 180, 85)...)
	for _, s := range ss {
		r.add(s)
	}
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	// The process dies here, before finish.
	if rides, _ := listRides(db, 10); len(rides) != 0 {
		t.Errorf("open ride listed: %+v", rides)
	}

	// Without open, an open ride might be in progress, so it's left alone.
	if err := resummarize(db, 75, false); err != nil {
		t.Fatal(err)
	}
	if rides, _ := listRides(db, 10); len(rides) != 0 {
		t.Errorf("open ride finished: %+v", rides)
	}

	// The ride keeps the weight it was recorded at.
	if err := resummarize(db, 75, true); err != nil {
		t.Fatal(err)
	}
	rides, err := listRides(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := summarize(ss, 68)
	if len(rides) != 1 || rides[0].Duration != want.Duration || !near(rides[0].EnergyKJ, want.EnergyKJ) ||
		!near(rides[0].DistanceKM, want.DistanceKM) {
		t.Errorf("recovered %+v, want %+v", rides, want)
	}
}

// A ride saved by schema 1 had the trainer's distance and no weight. The
// upgrade adds the column, and resummarize redoes the ride on the model.
func TestUpgradeFromSchema1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rides.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(migrations[0] + "PRAGMA user_version = 1;"); err != nil {
		t.Fatal(err)
	}
	ss := ride(t0, steady(20, 250, 90)...)
	last := ss[len(ss)-1].T.UnixMilli()
	if _, err := old.Exec(`INSERT INTO rides (id, trainer, started_at, ended_at, distance_km) VALUES (1, 'KICKR', ?, ?, 9.9)`,
		t0.UnixMilli(), last); err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if _, err := old.Exec(`INSERT INTO samples (ride_id, t, power, cadence, speed) VALUES (1, ?, ?, ?, ?)`,
			s.T.UnixMilli(), s.Power, s.Cadence, s.Speed); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	db, err := openDBAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("schema %d, %v; want %d", version, err, len(migrations))
	}
	if err := resummarize(db, 70, false); err != nil {
		t.Fatal(err)
	}
	var kg, km float64
	if err := db.QueryRow(`SELECT weight_kg, distance_km FROM rides WHERE id = 1`).Scan(&kg, &km); err != nil {
		t.Fatal(err)
	}
	if want := summarize(ss, 70); kg != 70 || !near(km, want.DistanceKM) {
		t.Errorf("ride 1 at %v kg, %v km; want 70 kg, %v km", kg, km, want.DistanceKM)
	}
}
