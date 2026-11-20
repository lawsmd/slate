package herdr

import "testing"

func TestPlace(t *testing.T) {
	for _, c := range []struct {
		name       string
		cols, rows int
		area, r    rect
		x, y       int
		ok         bool
	}{
		// Measured: a 26-column sidebar and a tab bar row around a 198 by
		// 57 pane area, in a 224 by 58 terminal.
		{"right pane", 224, 58, rect{0, 0, 198, 57}, rect{99, 0, 99, 57}, 125, 1, true},
		{"left pane", 224, 58, rect{0, 0, 198, 57}, rect{0, 0, 99, 57}, 26, 1, true},
		{"area counted from the terminal", 224, 58, rect{26, 1, 198, 57}, rect{125, 1, 99, 57}, 125, 1, true},
		{"no tab bar", 224, 58, rect{0, 0, 198, 58}, rect{99, 0, 99, 58}, 125, 0, true},
		{"size unknown", 0, 0, rect{0, 0, 198, 57}, rect{99, 0, 99, 57}, 0, 0, false},
		{"area larger than the terminal", 180, 58, rect{0, 0, 198, 57}, rect{99, 0, 99, 57}, 0, 0, false},
	} {
		x, y, ok := place(c.cols, c.rows, c.area, c.r)
		if x != c.x || y != c.y || ok != c.ok {
			t.Errorf("%s: place = %d, %d, %v; want %d, %d, %v", c.name, x, y, ok, c.x, c.y, c.ok)
		}
	}
}
