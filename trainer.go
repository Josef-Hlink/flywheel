package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// candidate is an FTMS trainer seen while scanning. On macOS the address is
// a per-host CoreBluetooth UUID, not a MAC, so the name matters too.
type candidate struct {
	addr bluetooth.Address
	name string
	rssi int16
}

func (c candidate) String() string {
	return fmt.Sprintf("%s  %s  %d dBm", c.name, c.addr.String(), c.rssi)
}

// findTrainer scans for FTMS trainers and picks one: the trainer at addr if
// given and seen, else with an empty name the strongest-signal named
// trainer, otherwise the strongest whose name contains name
// (case-insensitive). Seeing addr or an exact name match ends the scan early.
func findTrainer(adapter *bluetooth.Adapter, name, addr string, timeout time.Duration) (candidate, error) {
	var once sync.Once
	// StopScan panics if two calls race, so the timer and an early match
	// have to share one.
	stop := func() { once.Do(func() { adapter.StopScan() }) }
	timer := time.AfterFunc(timeout, stop)
	defer timer.Stop()

	// Callbacks are serialised and stop once Scan returns, so the map needs
	// no lock.
	found := map[string]*candidate{}
	err := adapter.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
		key := r.Address.String()
		c, ok := found[key]
		if !ok {
			// A scan response can carry the name without the service UUID,
			// so only the first sighting has to prove it's FTMS.
			if !r.HasServiceUUID(bluetooth.ServiceUUIDFitnessMachine) {
				return
			}
			c = &candidate{addr: r.Address}
			found[key] = c
		}
		if n := r.LocalName(); n != "" {
			c.name = n
		}
		c.rssi = r.RSSI
		if (addr != "" && key == addr) || (name != "" && strings.EqualFold(c.name, name)) {
			stop()
		}
	})
	if err != nil {
		return candidate{}, fmt.Errorf("scan: %w", err)
	}

	if c, ok := found[addr]; ok && c.name != "" {
		return *c, nil
	}
	var best *candidate
	for _, c := range found {
		if c.name == "" {
			continue
		}
		if name != "" && !strings.Contains(strings.ToLower(c.name), strings.ToLower(name)) {
			continue
		}
		if best == nil || c.rssi > best.rssi {
			best = c
		}
	}
	if best == nil {
		if name != "" {
			return candidate{}, fmt.Errorf("no trainer named %q found; is it on and awake? (pedal to wake it)", name)
		}
		return candidate{}, errors.New("no trainers found; is it powered, and not connected to another app?")
	}
	return *best, nil
}

// trainer is a connected FTMS trainer.
type trainer struct {
	dev    bluetooth.Device
	name   string
	chars  map[bluetooth.UUID]bluetooth.DeviceCharacteristic
	cp     bluetooth.DeviceCharacteristic
	cpResp chan []byte
}

// connectTrainer connects, discovers the FTMS characteristics and enables
// control point indications, which FTMS requires before any write.
func connectTrainer(adapter *bluetooth.Adapter, c candidate) (_ *trainer, err error) {
	dev, err := adapter.Connect(c.addr, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", c.name, err)
	}
	defer func() {
		if err != nil {
			dev.Disconnect()
		}
	}()

	svcs, err := dev.DiscoverServices([]bluetooth.UUID{bluetooth.ServiceUUIDFitnessMachine})
	if err != nil {
		return nil, fmt.Errorf("discover FTMS service: %w", err)
	}
	chars, err := svcs[0].DiscoverCharacteristics(nil)
	if err != nil {
		return nil, fmt.Errorf("discover FTMS characteristics: %w", err)
	}

	t := &trainer{
		dev:    dev,
		name:   c.name,
		chars:  map[bluetooth.UUID]bluetooth.DeviceCharacteristic{},
		cpResp: make(chan []byte, 4),
	}
	for _, ch := range chars {
		t.chars[ch.UUID()] = ch
	}

	cp, ok := t.chars[bluetooth.CharacteristicUUIDFitnessMachineControlPoint]
	if !ok {
		return nil, fmt.Errorf("%s has no FTMS control point", c.name)
	}
	t.cp = cp
	err = cp.EnableNotifications(func(b []byte) {
		// Never block the CoreBluetooth delegate.
		select {
		case t.cpResp <- b:
		default:
		}
	})
	if err != nil {
		return nil, fmt.Errorf("enable control point indications: %w", err)
	}
	return t, nil
}

func (t *trainer) has(u bluetooth.UUID) bool {
	_, ok := t.chars[u]
	return ok
}

func (t *trainer) read(u bluetooth.UUID) ([]byte, error) {
	ch, ok := t.chars[u]
	if !ok {
		return nil, fmt.Errorf("characteristic %s not present", uuidName(u))
	}
	buf := make([]byte, 64)
	n, err := ch.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", uuidName(u), err)
	}
	return buf[:n], nil
}

// subscribe enables notifications on u. fn runs on the CoreBluetooth
// delegate, so it must not block.
func (t *trainer) subscribe(u bluetooth.UUID, fn func([]byte)) error {
	ch, ok := t.chars[u]
	if !ok {
		return fmt.Errorf("characteristic %s not present", uuidName(u))
	}
	if err := ch.EnableNotifications(fn); err != nil {
		return fmt.Errorf("subscribe to %s: %w", uuidName(u), err)
	}
	return nil
}

// control writes a control point request and waits for its response. It
// returns the raw response alongside any error.
func (t *trainer) control(frame ...byte) ([]byte, error) {
	op := frame[0]
	for len(t.cpResp) > 0 {
		<-t.cpResp // stale response from an earlier, timed-out request
	}
	if _, err := t.cp.Write(frame); err != nil {
		return nil, fmt.Errorf("%s: %w", opName(op), err)
	}
	select {
	case b := <-t.cpResp:
		return b, parseResponse(op, b)
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("%s: no response from trainer", opName(op))
	}
}

func (t *trainer) close() {
	t.dev.Disconnect()
}

var uuidNames = map[bluetooth.UUID]string{
	bluetooth.CharacteristicUUIDFitnessMachineFeature:         "fitness machine feature",
	bluetooth.CharacteristicUUIDIndoorBikeData:                "indoor bike data",
	bluetooth.CharacteristicUUIDTrainingStatus:                "training status",
	bluetooth.CharacteristicUUIDSupportedResistanceLevelRange: "supported resistance level range",
	bluetooth.CharacteristicUUIDSupportedPowerRange:           "supported power range",
	bluetooth.CharacteristicUUIDFitnessMachineControlPoint:    "control point",
	bluetooth.CharacteristicUUIDFitnessMachineStatus:          "fitness machine status",
}

func uuidName(u bluetooth.UUID) string {
	s := u.String()
	if u.Is16Bit() {
		s = fmt.Sprintf("0x%04X", u.Get16Bit())
	}
	if n, ok := uuidNames[u]; ok {
		s += " " + n
	}
	return s
}
