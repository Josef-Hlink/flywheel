package main

// Encoding and decoding for the Bluetooth Fitness Machine Service (FTMS).
// All multi-byte fields are little-endian.

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// Control point (0x2AD9) opcodes.
const (
	opRequestControl = 0x00
	opReset          = 0x01
	opSetResistance  = 0x04
	opSetPower       = 0x05
	opStartResume    = 0x07
	opStopPause      = 0x08
	opResponse       = 0x80
	stopParamStop    = 0x01
	stopParamPause   = 0x02
)

var opNames = map[byte]string{
	opRequestControl: "request control",
	opReset:          "reset",
	opSetResistance:  "set target resistance",
	opSetPower:       "set target power",
	opStartResume:    "start/resume",
	opStopPause:      "stop/pause",
}

func opName(op byte) string {
	if n, ok := opNames[op]; ok {
		return n
	}
	return fmt.Sprintf("opcode 0x%02x", op)
}

// Control point result codes.
const (
	resultSuccess      = 0x01
	resultNotSupported = 0x02
	resultInvalidParam = 0x03
	resultFailed       = 0x04
	resultNotPermitted = 0x05
)

var resultNames = map[byte]string{
	resultSuccess:      "success",
	resultNotSupported: "not supported",
	resultInvalidParam: "invalid parameter",
	resultFailed:       "operation failed",
	resultNotPermitted: "control not permitted",
}

// controlError is a non-success result from the control point.
type controlError struct {
	op, result byte
}

func (e controlError) Error() string {
	msg, ok := resultNames[e.result]
	if !ok {
		msg = fmt.Sprintf("result 0x%02x", e.result)
	}
	if e.result == resultNotPermitted {
		msg += " (is another app, like MyWhoosh or the Wahoo app, controlling the trainer?)"
	}
	return fmt.Sprintf("%s: %s", opName(e.op), msg)
}

// parseResponse checks a control point indication against the request
// opcode it should answer.
func parseResponse(op byte, b []byte) error {
	if len(b) < 3 || b[0] != opResponse {
		return fmt.Errorf("%s: malformed response % x", opName(op), b)
	}
	if b[1] != op {
		return fmt.Errorf("%s: response is for %s", opName(op), opName(b[1]))
	}
	if b[2] != resultSuccess {
		return controlError{op, b[2]}
	}
	return nil
}

func encodePower(watts int16) []byte {
	return binary.LittleEndian.AppendUint16([]byte{opSetPower}, uint16(watts))
}

// encodeResistance takes the level in the trainer's raw units (0.1 steps,
// see resistanceRange).
func encodeResistance(raw int16) []byte {
	return binary.LittleEndian.AppendUint16([]byte{opSetResistance}, uint16(raw))
}

// bikeData is a decoded Indoor Bike Data (0x2AD2) notification. Fields the
// trainer didn't send are nil.
type bikeData struct {
	Speed      *float64 // km/h
	Cadence    *float64 // rpm
	Distance   *uint32  // m
	Resistance *int16
	Power      *int16 // W
	HeartRate  *uint8
	Elapsed    *uint16 // s
}

func parseBikeData(b []byte) (bikeData, error) {
	var d bikeData
	if len(b) < 2 {
		return d, fmt.Errorf("indoor bike data too short: % x", b)
	}
	flags := binary.LittleEndian.Uint16(b)
	r := reader{b: b[2:]}
	has := func(bit uint) bool { return flags&(1<<bit) != 0 }

	// Bit 0 ("more data") is inverted: speed is present when it's clear.
	if !has(0) {
		v := float64(r.u16()) / 100
		d.Speed = &v
	}
	if has(1) {
		r.skip(2) // average speed
	}
	if has(2) {
		v := float64(r.u16()) / 2
		d.Cadence = &v
	}
	if has(3) {
		r.skip(2) // average cadence
	}
	if has(4) {
		v := r.u24()
		d.Distance = &v
	}
	if has(5) {
		v := int16(r.u16())
		d.Resistance = &v
	}
	if has(6) {
		v := int16(r.u16())
		d.Power = &v
	}
	if has(7) {
		r.skip(2) // average power
	}
	if has(8) {
		r.skip(5) // total, per hour, per minute energy
	}
	if has(9) {
		v := r.u8()
		d.HeartRate = &v
	}
	if has(10) {
		r.skip(1) // metabolic equivalent
	}
	if has(11) {
		v := r.u16()
		d.Elapsed = &v
	}
	if r.short {
		return d, fmt.Errorf("indoor bike data shorter than its flags 0x%04x say: % x", flags, b)
	}
	return d, nil
}

func (d bikeData) String() string {
	var parts []string
	if d.Power != nil {
		parts = append(parts, fmt.Sprintf("%d W", *d.Power))
	}
	if d.Cadence != nil {
		parts = append(parts, fmt.Sprintf("%.1f rpm", *d.Cadence))
	}
	if d.Speed != nil {
		parts = append(parts, fmt.Sprintf("%.2f km/h", *d.Speed))
	}
	if d.Resistance != nil {
		parts = append(parts, fmt.Sprintf("resistance %d", *d.Resistance))
	}
	if d.HeartRate != nil {
		parts = append(parts, fmt.Sprintf("%d bpm", *d.HeartRate))
	}
	if d.Distance != nil {
		parts = append(parts, fmt.Sprintf("%d m", *d.Distance))
	}
	if d.Elapsed != nil {
		parts = append(parts, fmt.Sprintf("%d s", *d.Elapsed))
	}
	return strings.Join(parts, "  ")
}

// parseStatus describes a Fitness Machine Status (0x2ADA) notification.
func parseStatus(b []byte) string {
	if len(b) == 0 {
		return "empty status"
	}
	p := b[1:]
	switch b[0] {
	case 0x01:
		return "reset"
	case 0x02:
		if len(p) >= 1 && p[0] == stopParamPause {
			return "paused"
		}
		return "stopped"
	case 0x03:
		return "stopped by safety key"
	case 0x04:
		return "started/resumed"
	case 0x07:
		// FTMS 1.0 says uint8 at 0.1; later revisions and most apps use
		// sint16 at 0.1. Go by length.
		switch len(p) {
		case 1:
			return fmt.Sprintf("target resistance changed: %.1f (uint8)", float64(p[0])/10)
		case 2:
			return fmt.Sprintf("target resistance changed: %.1f", float64(int16(binary.LittleEndian.Uint16(p)))/10)
		}
	case 0x08:
		if len(p) >= 2 {
			return fmt.Sprintf("target power changed: %d W", int16(binary.LittleEndian.Uint16(p)))
		}
	case 0x12:
		return "simulation parameters changed"
	case 0x14:
		return "spin down status"
	case 0xFF:
		return "control permission lost"
	}
	return fmt.Sprintf("status 0x%02x % x", b[0], p)
}

var trainingStatusNames = []string{
	"other", "idle", "warming up", "low intensity interval",
	"high intensity interval", "recovery interval", "isometric",
	"heart rate control", "fitness test", "speed below control region",
	"speed above control region", "cool down", "watt control",
	"manual mode", "pre-workout", "post-workout",
}

// parseTrainingStatus describes a Training Status (0x2AD3) value.
func parseTrainingStatus(b []byte) string {
	if len(b) < 2 {
		return fmt.Sprintf("training status % x", b)
	}
	s := fmt.Sprintf("status 0x%02x", b[1])
	if int(b[1]) < len(trainingStatusNames) {
		s = trainingStatusNames[b[1]]
	}
	if b[0]&1 != 0 && len(b) > 2 {
		s += fmt.Sprintf(" %q", b[2:])
	}
	return s
}

var machineFeatureNames = []string{
	"average speed", "cadence", "total distance", "inclination",
	"elevation gain", "pace", "step count", "resistance level",
	"stride count", "expended energy", "heart rate", "metabolic equivalent",
	"elapsed time", "remaining time", "power measurement",
	"force on belt and power output", "user data retention",
}

var targetFeatureNames = []string{
	"speed", "inclination", "resistance", "power", "heart rate",
	"expended energy", "step number", "stride number", "distance",
	"training time", "time in 2 HR zones", "time in 3 HR zones",
	"time in 5 HR zones", "indoor bike simulation", "wheel circumference",
	"spin down", "cadence",
}

const (
	targetResistance = 1 << 2
	targetPower      = 1 << 3
)

// features is the decoded Fitness Machine Feature (0x2ACC) value.
type features struct {
	Machine, Target uint32
}

func parseFeatures(b []byte) (features, error) {
	if len(b) < 8 {
		return features{}, fmt.Errorf("fitness machine feature too short: % x", b)
	}
	return features{
		Machine: binary.LittleEndian.Uint32(b),
		Target:  binary.LittleEndian.Uint32(b[4:]),
	}, nil
}

func flagNames(flags uint32, names []string) string {
	var out []string
	for i := range 32 {
		if flags&(1<<i) == 0 {
			continue
		}
		if i < len(names) {
			out = append(out, names[i])
		} else {
			out = append(out, fmt.Sprintf("bit %d", i))
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// valueRange is a Supported Power Range (0x2AD8) or Supported Resistance
// Level Range (0x2AD6). Resistance values are in raw 0.1 steps.
type valueRange struct {
	Min, Max int16
	Step     uint16
}

func parseRange(b []byte) (valueRange, error) {
	if len(b) < 6 {
		return valueRange{}, fmt.Errorf("range too short: % x", b)
	}
	return valueRange{
		Min:  int16(binary.LittleEndian.Uint16(b)),
		Max:  int16(binary.LittleEndian.Uint16(b[2:])),
		Step: binary.LittleEndian.Uint16(b[4:]),
	}, nil
}

// fromPercent maps 0–100% onto the range, rounded to its step. On the Kickr
// Core (raw 0–100, step 1) it's the identity.
func (r valueRange) fromPercent(pct int) int16 {
	step := max(int(r.Step), 1)
	span := float64(r.Max) - float64(r.Min)
	steps := math.Round(float64(pct) / 100 * span / float64(step))
	return r.Min + int16(int(steps)*step)
}

// reader consumes little-endian fields and records running off the end
// instead of panicking.
type reader struct {
	b     []byte
	short bool
}

func (r *reader) take(n int) []byte {
	if len(r.b) < n {
		r.short = true
		r.b = nil
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) skip(n int)  { r.take(n) }
func (r *reader) u8() uint8   { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.LittleEndian.Uint16(r.take(2)) }
func (r *reader) u24() uint32 {
	b := r.take(3)
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}
