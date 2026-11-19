package sixel

import (
	"strconv"
	"strings"
	"testing"
)

// decode is a small Sixel reader covering what Encode writes. It returns RGB
// bytes, three per pixel.
func decode(t *testing.T, s string) (pix []byte, w, h int) {
	t.Helper()
	s, ok := strings.CutPrefix(s, "\x1bP0;1;0q")
	if !ok {
		t.Fatalf("missing header: %q", s[:min(len(s), 20)])
	}
	s, ok = strings.CutSuffix(s, "\x1b\\")
	if !ok {
		t.Fatal("missing terminator")
	}
	pos := 0
	num := func() int {
		start := pos
		for pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
			pos++
		}
		n, err := strconv.Atoi(s[start:pos])
		if err != nil {
			t.Fatalf("expected a number at %d", start)
		}
		return n
	}
	if s[pos] != '"' {
		t.Fatal("missing raster attributes")
	}
	pos++
	num()
	pos++
	num()
	pos++
	w = num()
	pos++
	h = num()
	pix = make([]byte, w*h*3)

	var palette [MaxColors][3]byte
	cur, x, y := 0, 0, 0
	put := func(bits byte, run int) {
		for ; run > 0; run-- {
			for i := 0; i < 6; i++ {
				if bits&(1<<i) != 0 && y+i < h && x < w {
					copy(pix[((y+i)*w+x)*3:], palette[cur][:])
				}
			}
			x++
		}
	}
	for pos < len(s) {
		c := s[pos]
		pos++
		switch {
		case c == '#':
			cur = num()
			if pos < len(s) && s[pos] == ';' {
				pos++
				num() // colour space, always 2
				for i := 0; i < 3; i++ {
					pos++
					palette[cur][i] = byte((num()*255 + 50) / 100)
				}
			}
		case c == '$':
			x = 0
		case c == '-':
			x, y = 0, y+6
		case c == '!':
			run := num()
			put(s[pos]-63, run)
			pos++
		case c >= 63 && c <= 126:
			put(c-63, 1)
		default:
			t.Fatalf("unexpected byte %q at %d", c, pos-1)
		}
	}
	return pix, w, h
}

func maxError(t *testing.T, rgba []byte, stride int, got []byte, w, h int) (worst, total int) {
	t.Helper()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				d := int(rgba[y*stride+x*4+c]) - int(got[(y*w+x)*3+c])
				if d < 0 {
					d = -d
				}
				worst = max(worst, d)
				total += d
			}
		}
	}
	return worst, total
}

func TestRoundTripFewColours(t *testing.T) {
	// 13 rows: the last band is one row tall. The stride is wider than the
	// image to check that padding is skipped.
	const w, h, stride = 37, 13, 40 * 4
	colours := [][3]byte{{26, 27, 38}, {192, 202, 245}, {122, 162, 247}, {255, 0, 0}, {0, 0, 0}, {255, 255, 255}}
	rgba := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := colours[(x/3+y*y)%len(colours)]
			copy(rgba[y*stride+x*4:], []byte{c[0], c[1], c[2], 255})
		}
	}
	var e Encoder
	out := e.Encode(nil, rgba, stride, w, h)
	got, gw, gh := decode(t, string(out))
	if gw != w || gh != h {
		t.Fatalf("size %dx%d, want %dx%d", gw, gh, w, h)
	}
	// Sixel stores each channel as a percentage, so 2 is the rounding limit.
	if worst, _ := maxError(t, rgba, stride, got, w, h); worst > 2 {
		t.Fatalf("worst channel error %d, want at most 2", worst)
	}
}

func TestManyColoursStayClose(t *testing.T) {
	const w, h = 128, 96
	rgba := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			copy(rgba[(y*w+x)*4:], []byte{byte(x * 2), byte(y * 2), byte(x + y), 255})
		}
	}
	var e Encoder
	out := e.Encode(nil, rgba, w*4, w, h)
	if e.colors != MaxColors {
		t.Fatalf("used %d colours, want %d", e.colors, MaxColors)
	}
	got, gw, gh := decode(t, string(out))
	if gw != w || gh != h {
		t.Fatalf("size %dx%d, want %dx%d", gw, gh, w, h)
	}
	_, total := maxError(t, rgba, w*4, got, w, h)
	// A smooth gradient is the worst input for a quantiser that keeps the
	// most frequent colours. The bound catches a broken mapping, not banding.
	if mean := float64(total) / float64(w*h*3); mean > 16 {
		t.Fatalf("mean channel error %.1f, want at most 16", mean)
	}
}

func TestEncoderReuse(t *testing.T) {
	var e Encoder
	a := []byte{255, 0, 0, 255, 0, 255, 0, 255}
	b := []byte{0, 0, 255, 255, 0, 0, 255, 255}
	first := string(e.Encode(nil, a, 8, 2, 1))
	e.Encode(nil, b, 8, 2, 1)
	if again := string(e.Encode(nil, a, 8, 2, 1)); again != first {
		t.Fatalf("state leaked between frames:\n%q\n%q", first, again)
	}
}

func BenchmarkEncodeText(b *testing.B) {
	// A frame the size of an 80 by 24 cell block, with sparse marks standing
	// in for text.
	const w, h = 720, 456
	rgba := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		c := [3]byte{26, 27, 38}
		if (i*7919)%11 == 0 {
			v := byte(i * 31)
			c = [3]byte{v, v, v}
		}
		copy(rgba[i*4:], []byte{c[0], c[1], c[2], 255})
	}
	var e Encoder
	var out []byte
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out = e.Encode(out[:0], rgba, w*4, w, h)
	}
	b.SetBytes(int64(len(out)))
}
