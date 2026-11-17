package main

import "testing"

func TestParseClickKeypad(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
		down  clickButton
		ok    bool
	}{
		{"released", []byte{0x23, 0x08, 0xff, 0xff, 0xff, 0xff, 0x0f}, 0, true},
		{"A", []byte{0x23, 0x08, 0xef, 0xff, 0xff, 0xff, 0x0f}, clickA, true},
		{"plus", []byte{0x23, 0x08, 0xff, 0xdf, 0xff, 0xff, 0x0f}, clickPlus, true},
		{"Y and Z", []byte{0x23, 0x08, 0xbf, 0xfe, 0xff, 0xff, 0x0f}, clickY | clickZ, true},
		{"trailing fields", []byte{0x23, 0x08, 0xdf, 0xff, 0xff, 0xff, 0x0f, 0x1a, 0x00}, clickB, true},
		// The left pod's buttons aren't ours to act on.
		{"d-pad", []byte{0x23, 0x08, 0xfe, 0xff, 0xff, 0xff, 0x0f}, 0, true},
		{"battery", []byte{0x19, 0x10, 0x64}, 0, false},
		{"locked", []byte{0xff, 0x05, 0x00, 0xfa, 0x05}, 0, false},
		{"cut short", []byte{0x23, 0x08, 0xff}, 0, false},
		{"empty", nil, 0, false},
	}
	for _, tt := range tests {
		down, ok := parseClickKeypad(tt.frame)
		if down != tt.down || ok != tt.ok {
			t.Errorf("%s: got %#x, %v; want %#x, %v", tt.name, uint32(down), ok, uint32(tt.down), tt.ok)
		}
	}
}

func TestClickTarget(t *testing.T) {
	const ceiling = 500
	tests := []struct {
		name   string
		btn    clickButton
		mode   string
		target int
		last   map[string]int
		mode2  string
		value  int
		ok     bool
	}{
		{"nudge up", clickY, modeERG, 200, nil, modeERG, 210, true},
		{"nudge down", clickB, modeERG, 200, nil, modeERG, 190, true},
		{"step up", clickA, modeERG, 200, nil, modeERG, 250, true},
		{"step down", clickZ, modeERG, 200, nil, modeERG, 150, true},
		{"resistance steps are smaller", clickY, modeResistance, 30, nil, modeResistance, 31, true},
		{"resistance big step", clickZ, modeResistance, 30, nil, modeResistance, 25, true},

		{"stops at the cap", clickA, modeERG, 480, nil, modeERG, 500, true},
		{"at the cap", clickY, modeERG, 500, nil, modeERG, 500, false},
		{"typed in above the cap", clickY, modeERG, 650, nil, modeERG, 650, false},
		{"down from above the cap", clickB, modeERG, 650, nil, modeERG, 640, true},
		{"stops at zero", clickZ, modeERG, 30, nil, modeERG, 0, true},
		{"at zero", clickB, modeERG, 0, nil, modeERG, 0, false},
		{"resistance stops at 100%", clickA, modeResistance, 98, nil, modeResistance, 100, true},

		{"first press starts ERG", clickB, "", 0, nil, modeERG, clickStartWatts, true},
		{"switch to resistance", clickPlus, modeERG, 200, nil, modeResistance, clickStartPercent, true},
		{"switch back to where ERG was", clickPlus, modeResistance, 30, map[string]int{modeERG: 220}, modeERG, 220, true},
		{"switch to where resistance was", clickPlus, modeERG, 200, map[string]int{modeResistance: 35}, modeResistance, 35, true},
		{"switch back, capped", clickPlus, modeResistance, 30, map[string]int{modeERG: 650}, modeERG, 500, true},
	}
	for _, tt := range tests {
		mode, value, ok := clickTarget(tt.btn, tt.mode, tt.target, tt.last, ceiling)
		if mode != tt.mode2 || value != tt.value || ok != tt.ok {
			t.Errorf("%s: got %s %d %v, want %s %d %v", tt.name, mode, value, ok, tt.mode2, tt.value, tt.ok)
		}
	}
}
