package main

import (
	"errors"
	"testing"
)

func TestParseBikeData(t *testing.T) {
	// Flags 0x0044: speed (bit 0 clear), cadence, power.
	d, err := parseBikeData([]byte{0x44, 0x00, 0xb8, 0x0b, 0xab, 0x00, 0x96, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if d.Speed == nil || *d.Speed != 30 {
		t.Errorf("speed = %v, want 30", d.Speed)
	}
	if d.Cadence == nil || *d.Cadence != 85.5 {
		t.Errorf("cadence = %v, want 85.5", d.Cadence)
	}
	if d.Power == nil || *d.Power != 150 {
		t.Errorf("power = %v, want 150", d.Power)
	}
	if d.Distance != nil || d.HeartRate != nil {
		t.Errorf("unexpected fields: %+v", d)
	}

	// Flags 0x0177: no speed, avg speed, cadence, distance, resistance, power,
	// energy. Every field before power has to be skipped correctly.
	d, err = parseBikeData([]byte{
		0x77, 0x01,
		0x10, 0x27, // avg speed
		0x50, 0x00, // cadence 40
		0x01, 0x02, 0x03, // distance 0x030201
		0x1e, 0x00, // resistance 30
		0xfa, 0x00, // power 250
		0, 0, 0, 0, 0, // energy
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Speed != nil {
		t.Errorf("speed = %v, want absent", *d.Speed)
	}
	if *d.Cadence != 40 || *d.Distance != 0x030201 || *d.Resistance != 30 || *d.Power != 250 {
		t.Errorf("got %s", d)
	}

	if _, err := parseBikeData([]byte{0x44, 0x00, 0xb8}); err == nil {
		t.Error("truncated frame: want error")
	}
}

func TestParseResponse(t *testing.T) {
	if err := parseResponse(opSetPower, []byte{0x80, 0x05, 0x01}); err != nil {
		t.Errorf("success: %v", err)
	}
	var ce controlError
	err := parseResponse(opRequestControl, []byte{0x80, 0x00, 0x05})
	if !errors.As(err, &ce) || ce.result != resultNotPermitted {
		t.Errorf("not permitted: got %v", err)
	}
	if err := parseResponse(opSetPower, []byte{0x80, 0x00, 0x01}); err == nil {
		t.Error("mismatched opcode: want error")
	}
}

func TestEncode(t *testing.T) {
	if got := encodePower(150); string(got) != "\x05\x96\x00" {
		t.Errorf("encodePower(150) = % x", got)
	}
	if got := encodeResistance(300); string(got) != "\x04\x2c\x01" {
		t.Errorf("encodeResistance(300) = % x", got)
	}
}

func TestFromPercent(t *testing.T) {
	kickr := valueRange{Min: 0, Max: 100, Step: 1}
	for pct, want := range map[int]int16{0: 0, 30: 30, 100: 100} {
		if got := kickr.fromPercent(pct); got != want {
			t.Errorf("kickr %d%% = %d, want %d", pct, got, want)
		}
	}
	// A 0.0-100.0 trainer in 0.1 steps, quantised to whole levels.
	fine := valueRange{Min: 0, Max: 1000, Step: 10}
	if got := fine.fromPercent(33); got != 330 {
		t.Errorf("fine 33%% = %d, want 330", got)
	}
}
