package main

import "testing"

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]struct {
		mode  string
		value int
	}{
		"250W": {modeERG, 250},
		"250w": {modeERG, 250},
		"30%":  {modeResistance, 30},
		"0%":   {modeResistance, 0},
	} {
		mode, value, err := parseTarget(in)
		if err != nil || mode != want.mode || value != want.value {
			t.Errorf("parseTarget(%q) = %q, %d, %v", in, mode, value, err)
		}
	}
	for _, in := range []string{"250", "W", "12.5W", "abc%"} {
		if _, _, err := parseTarget(in); err == nil {
			t.Errorf("parseTarget(%q): want error", in)
		}
	}
}
