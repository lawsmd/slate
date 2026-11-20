package slate

import "testing"

func TestEnvCellSize(t *testing.T) {
	for _, c := range []struct {
		env  string
		w, h int
		ok   bool
	}{
		{"", 0, 0, false},
		{"10x20", 10, 20, true},
		{"8x16", 8, 16, true},
		{"10", 0, 0, false},
		{"0x20", 0, 0, false},
		{"ax20", 0, 0, false},
		{"10x-2", 0, 0, false},
	} {
		t.Setenv("SLATE_CELL", c.env)
		if w, h, ok := EnvCellSize(); w != c.w || h != c.h || ok != c.ok {
			t.Errorf("SLATE_CELL=%q: got %d, %d, %v", c.env, w, h, ok)
		}
	}
}
