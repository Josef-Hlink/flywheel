package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// Zwift Click v2 support: the right-hand pod (A, B, Y, Z and +) as buttons
// for the target. The protocol is Zwift's own and undocumented; this follows
// what BikeControl (github.com/jonasbark/swiftcontrol) worked out. The
// left-hand pod stays locked until the Zwift app has unlocked it, so it
// isn't used.

const (
	zwiftCompanyID = 0x094A
	clickRightSide = 0x0A // first byte of the advertised manufacturer data

	clickKeypad = 0x23 // message type of a button report
)

var (
	clickService = bluetooth.New16BitUUID(0xFC82)
	clickAsync   = mustUUID("00000002-19ca-4651-86e5-fa29dcdd09d1") // notify: buttons, battery
	clickSyncRX  = mustUUID("00000003-19ca-4651-86e5-fa29dcdd09d1") // write: commands
	clickSyncTX  = mustUUID("00000004-19ca-4651-86e5-fa29dcdd09d1") // indicate: replies

	// clickStart makes the pod report its buttons; it echoes it back.
	clickStart = append([]byte("RideOn"), 0x02, 0x03)
)

func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

// clickButton is a bit in the pod's button map.
type clickButton uint32

const (
	clickA    clickButton = 0x0010
	clickB    clickButton = 0x0020
	clickY    clickButton = 0x0040
	clickZ    clickButton = 0x0080
	clickPlus clickButton = 0x1000
)

var clickButtons = []clickButton{clickA, clickB, clickY, clickZ, clickPlus}

func (b clickButton) String() string {
	switch b {
	case clickA:
		return "A"
	case clickB:
		return "B"
	case clickY:
		return "Y"
	case clickZ:
		return "Z"
	case clickPlus:
		return "+"
	}
	return fmt.Sprintf("button %#x", uint32(b))
}

// parseClickKeypad reads a button report into the set of buttons held down.
// The report is a protobuf message whose first field is a varint with a bit
// cleared for every button that's down.
func parseClickKeypad(b []byte) (down clickButton, ok bool) {
	if len(b) < 3 || b[0] != clickKeypad || b[1] != 0x08 {
		return 0, false
	}
	var released uint32
	for i, x := range b[2:] {
		if i == 5 {
			return 0, false
		}
		released |= uint32(x&0x7F) << (7 * i)
		if x&0x80 == 0 {
			var all clickButton
			for _, btn := range clickButtons {
				all |= btn
			}
			return clickButton(^released) & all, true
		}
	}
	return 0, false
}

const (
	clickStartWatts   = 100 // where the buttons start ERG from nothing
	clickStartPercent = 20  // and resistance
)

// clickTarget is the target a button press asks for. The four face buttons
// sit in a diamond: Y on top and B below nudge it up and down, A on the right
// and Z on the left do so in bigger steps. The + button, off to the side,
// switches between ERG and resistance, back to where that mode was left. With no target yet, any
// button starts ERG gently. ok is false if the press changes nothing.
func clickTarget(btn clickButton, mode string, target int, last map[string]int, maxWatts int) (newMode string, value int, ok bool) {
	lastOr := func(m string, def int) int {
		if v, ok := last[m]; ok {
			return v
		}
		return def
	}
	small, big, top := 10, 50, maxWatts
	switch mode {
	case "":
		return modeERG, min(lastOr(modeERG, clickStartWatts), maxWatts), true
	case modeResistance:
		small, big, top = 1, 5, 100
	}
	step := 0
	switch btn {
	case clickPlus:
		if mode == modeERG {
			return modeResistance, lastOr(modeResistance, clickStartPercent), true
		}
		return modeERG, min(lastOr(modeERG, clickStartWatts), maxWatts), true
	case clickY:
		step = small
	case clickB:
		step = -small
	case clickA:
		step = big
	case clickZ:
		step = -big
	}
	value = max(0, target+step)
	if step > 0 {
		// Up to the cap and no further; a target typed in above it stays put.
		value = max(target, min(value, top))
	}
	return mode, value, value != target
}

// click keeps looking for the pod and holds the link to it while it's awake.
type click struct {
	adapter *bluetooth.Adapter
	presses chan clickButton
	gone    chan struct{} // the pod disconnected

	mu    sync.Mutex
	addr  string // of the pod being connected or connected, else ""
	dev   *bluetooth.Device
	state string // searching or connected
	quit  bool
}

func newClick(adapter *bluetooth.Adapter) *click {
	return &click{
		adapter: adapter,
		presses: make(chan clickButton, 8),
		gone:    make(chan struct{}, 1),
		state:   "searching",
	}
}

func (c *click) status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// disconnected is for the adapter's connection handler: it reports whether
// addr was the pod, and if so wakes run to look for it again.
func (c *click) disconnected(addr string) bool {
	c.mu.Lock()
	ours := c.addr != "" && c.addr == addr
	c.mu.Unlock()
	if ours {
		select {
		case c.gone <- struct{}{}:
		default:
		}
	}
	return ours
}

// run scans for the pod, connects and waits for it to drop off again, for
// as long as the process lives. The pod sleeps when left alone and only
// advertises once a button wakes it, so the scan has no deadline.
func (c *click) run() {
	for {
		addr, err := c.find()
		if err != nil {
			log.Printf("click: scan: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		c.mu.Lock()
		if c.quit {
			c.mu.Unlock()
			return
		}
		c.addr = addr.String()
		c.mu.Unlock()
		for len(c.gone) > 0 {
			<-c.gone
		}

		dev, err := c.connect(addr)
		if err != nil {
			log.Printf("click: %v", err)
			c.mu.Lock()
			c.addr = ""
			c.mu.Unlock()
			time.Sleep(2 * time.Second)
			continue
		}
		c.mu.Lock()
		c.dev, c.state = dev, "connected"
		quit := c.quit
		c.mu.Unlock()
		if quit {
			dev.Disconnect()
			return
		}
		log.Printf("click: connected to the pod at %s", addr.String())

		<-c.gone
		c.mu.Lock()
		c.addr, c.dev, c.state = "", nil, "searching"
		quit = c.quit
		c.mu.Unlock()
		if quit {
			return
		}
		log.Print("click: pod disconnected; press a button on it to wake it")
	}
}

func (c *click) find() (bluetooth.Address, error) {
	var addr bluetooth.Address
	found := false
	err := c.adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if found {
			return
		}
		for _, m := range r.ManufacturerData() {
			if m.CompanyID == zwiftCompanyID && len(m.Data) > 0 && m.Data[0] == clickRightSide {
				addr, found = r.Address, true
				a.StopScan()
				return
			}
		}
	})
	return addr, err
}

func (c *click) connect(addr bluetooth.Address) (_ *bluetooth.Device, err error) {
	dev, err := c.adapter.Connect(addr, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() {
		if err != nil {
			dev.Disconnect()
		}
	}()
	svcs, err := dev.DiscoverServices([]bluetooth.UUID{clickService})
	if err != nil {
		return nil, fmt.Errorf("discover service: %w", err)
	}
	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{clickAsync, clickSyncRX, clickSyncTX})
	if err != nil {
		return nil, fmt.Errorf("discover characteristics: %w", err)
	}

	var down clickButton // only the notification callback touches it
	err = chars[0].EnableNotifications(func(b []byte) {
		now, ok := parseClickKeypad(b)
		if !ok {
			return
		}
		pressed := now &^ down
		down = now
		for _, btn := range clickButtons {
			if pressed&btn != 0 {
				select { // never block the BLE callback
				case c.presses <- btn:
				default:
				}
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe to buttons: %w", err)
	}
	if err := chars[2].EnableNotifications(func([]byte) {}); err != nil {
		return nil, fmt.Errorf("subscribe to replies: %w", err)
	}
	if _, err := chars[1].WriteWithoutResponse(clickStart); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	return &dev, nil
}

// close drops the link for good. BlueZ would otherwise keep it up after the
// process has gone.
func (c *click) close() {
	c.mu.Lock()
	c.quit = true
	dev := c.dev
	c.mu.Unlock()
	if dev != nil {
		dev.Disconnect()
	}
}
