// Package sixel encodes RGBA pixels as a Sixel image.
package sixel

import (
	"sort"
	"strconv"
)

// MaxColors is the number of colour registers the encoder uses. Windows
// Terminal provides 256.
const MaxColors = 256

const buckets = 1 << 15 // colours are grouped by the top 5 bits of each channel

// Encoder holds the working buffers for encoding, so that encoding a frame
// allocates nothing once the buffers have grown. The zero value is ready to
// use. An Encoder is not safe for concurrent use.
type Encoder struct {
	count      []uint32
	sumR, sumG []uint32
	sumB       []uint32
	lut        []uint8
	touched    []uint16
	index      []uint8
	palette    [MaxColors][3]uint8
	colors     int

	rows   [MaxColors][]byte
	inBand [MaxColors]bool
	used   []uint8
}

// Encode appends a Sixel image of the w by h pixel region to dst and returns
// the extended slice. pix holds RGBA bytes, row y starting at pix[y*stride].
// Alpha is ignored.
func (e *Encoder) Encode(dst, pix []byte, stride, w, h int) []byte {
	if w <= 0 || h <= 0 {
		return dst
	}
	e.quantize(pix, stride, w, h)

	// P2=1 leaves unset pixels alone. The raster attributes give the size.
	dst = append(dst, "\x1bP0;1;0q\"1;1;"...)
	dst = strconv.AppendInt(dst, int64(w), 10)
	dst = append(dst, ';')
	dst = strconv.AppendInt(dst, int64(h), 10)
	for i := 0; i < e.colors; i++ {
		c := e.palette[i]
		dst = append(dst, '#')
		dst = strconv.AppendInt(dst, int64(i), 10)
		dst = append(dst, ";2;"...)
		dst = strconv.AppendInt(dst, percent(c[0]), 10)
		dst = append(dst, ';')
		dst = strconv.AppendInt(dst, percent(c[1]), 10)
		dst = append(dst, ';')
		dst = strconv.AppendInt(dst, percent(c[2]), 10)
	}
	// A graphics newline separates bands. None follows the last band: at the
	// bottom of the screen it could scroll the display.
	for y := 0; y < h; y += 6 {
		if y > 0 {
			dst = append(dst, '-')
		}
		dst = e.band(dst, y, w, h)
	}
	return append(dst, "\x1b\\"...)
}

// percent converts a channel value to Sixel's 0 to 100 scale.
func percent(v uint8) int64 { return (int64(v)*100 + 127) / 255 }

// quantize fills e.index with one palette index per pixel. It keeps the most
// frequent colours and maps the others to the nearest kept colour, which
// suits text on a flat background.
func (e *Encoder) quantize(pix []byte, stride, w, h int) {
	if e.count == nil {
		e.count = make([]uint32, buckets)
		e.sumR = make([]uint32, buckets)
		e.sumG = make([]uint32, buckets)
		e.sumB = make([]uint32, buckets)
		e.lut = make([]uint8, buckets)
	}
	if cap(e.index) < w*h {
		e.index = make([]uint8, w*h)
	}
	e.index = e.index[:w*h]
	e.touched = e.touched[:0]

	for y := 0; y < h; y++ {
		row := pix[y*stride : y*stride+w*4]
		for x := 0; x < len(row); x += 4 {
			r, g, b := row[x], row[x+1], row[x+2]
			k := uint16(r>>3)<<10 | uint16(g>>3)<<5 | uint16(b>>3)
			if e.count[k] == 0 {
				e.touched = append(e.touched, k)
			}
			e.count[k]++
			e.sumR[k] += uint32(r)
			e.sumG[k] += uint32(g)
			e.sumB[k] += uint32(b)
		}
	}

	if len(e.touched) > MaxColors {
		sort.Slice(e.touched, func(i, j int) bool {
			ci, cj := e.count[e.touched[i]], e.count[e.touched[j]]
			if ci != cj {
				return ci > cj
			}
			return e.touched[i] < e.touched[j]
		})
	}
	e.colors = min(len(e.touched), MaxColors)
	for i, k := range e.touched[:e.colors] {
		e.palette[i] = e.mean(k)
		e.lut[k] = uint8(i)
	}
	for _, k := range e.touched[e.colors:] {
		c := e.mean(k)
		best, bestDist := 0, 1<<30
		for i := 0; i < e.colors; i++ {
			p := e.palette[i]
			dr, dg, db := int(c[0])-int(p[0]), int(c[1])-int(p[1]), int(c[2])-int(p[2])
			if d := dr*dr + dg*dg + db*db; d < bestDist {
				best, bestDist = i, d
			}
		}
		e.lut[k] = uint8(best)
	}

	for y := 0; y < h; y++ {
		row := pix[y*stride : y*stride+w*4]
		out := e.index[y*w : y*w+w]
		for x := range out {
			r, g, b := row[x*4], row[x*4+1], row[x*4+2]
			out[x] = e.lut[uint16(r>>3)<<10|uint16(g>>3)<<5|uint16(b>>3)]
		}
	}
	for _, k := range e.touched {
		e.count[k], e.sumR[k], e.sumG[k], e.sumB[k] = 0, 0, 0, 0
	}
}

func (e *Encoder) mean(k uint16) [3]uint8 {
	n := e.count[k]
	return [3]uint8{uint8(e.sumR[k] / n), uint8(e.sumG[k] / n), uint8(e.sumB[k] / n)}
}

// band appends the six pixel rows starting at y. Each colour present in the
// band is written as one pass over the band's width.
func (e *Encoder) band(dst []byte, y, w, h int) []byte {
	e.used = e.used[:0]
	for i := 0; i < 6 && y+i < h; i++ {
		bit := byte(1) << i
		for x, c := range e.index[(y+i)*w : (y+i)*w+w] {
			if !e.inBand[c] {
				e.inBand[c] = true
				e.used = append(e.used, c)
				if cap(e.rows[c]) < w {
					e.rows[c] = make([]byte, w)
				}
				e.rows[c] = e.rows[c][:w]
				clear(e.rows[c])
			}
			e.rows[c][x] |= bit
		}
	}
	for n, c := range e.used {
		e.inBand[c] = false
		if n > 0 {
			dst = append(dst, '$')
		}
		dst = append(dst, '#')
		dst = strconv.AppendInt(dst, int64(c), 10)
		row := e.rows[c]
		end := len(row)
		for end > 0 && row[end-1] == 0 {
			end--
		}
		for x := 0; x < end; {
			v := row[x]
			run := 1
			for x+run < end && row[x+run] == v {
				run++
			}
			ch := 63 + v
			if run > 3 {
				dst = append(dst, '!')
				dst = strconv.AppendInt(dst, int64(run), 10)
				dst = append(dst, ch)
			} else {
				for i := 0; i < run; i++ {
					dst = append(dst, ch)
				}
			}
			x += run
		}
	}
	return dst
}
