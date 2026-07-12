package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Brand mark: a rounded-square in SeDoc blue with a white document glyph
// (folded top-right corner + text lines). Rendered with 4x supersampling for
// clean edges, downscaled to the target size.

var (
	brand = color.NRGBA{0x25, 0x63, 0xEB, 0xFF} // #2563EB
	paper = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	line  = color.NRGBA{0x93, 0xB4, 0xF5, 0xFF} // light-blue text lines
)

func main() {
	sizes := []int{16, 32, 64, 80, 128}
	dests := os.Args[1:]
	if len(dests) == 0 {
		panic("usage: genicons <destDir>...")
	}
	for _, s := range sizes {
		img := render(s)
		for _, d := range dests {
			f, err := os.Create(filepath.Join(d, "icon-"+itoa(s)+".png"))
			if err != nil {
				panic(err)
			}
			if err := png.Encode(f, img); err != nil {
				panic(err)
			}
			_ = f.Close()
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func render(size int) *image.NRGBA {
	const ss = 4
	w := size * ss
	img := image.NewNRGBA(image.Rect(0, 0, w, w))
	fw := float64(w)
	radius := fw * 0.22

	// Document glyph geometry (centered, ~58% of the tile).
	dw := fw * 0.42
	dh := fw * 0.54
	dx := (fw - dw) / 2
	dy := (fw - dh) / 2
	fold := dw * 0.34 // folded corner size

	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			var c color.NRGBA
			if insideRoundRect(fx, fy, 0, 0, fw, fw, radius) {
				c = brand
				// Document body (rounded rect) minus the folded corner.
				if insideRoundRect(fx, fy, dx, dy, dw, dh, dw*0.06) {
					// Folded corner: cut the top-right triangle.
					if !(fx-dx > dw-fold && (fx-dx)-(dw-fold) > (dh-(fy-dy))-(dh-fold)) {
						c = paper
						// Three text lines.
						for i := 0; i < 3; i++ {
							ly := dy + dh*(0.42+0.16*float64(i))
							lx0 := dx + dw*0.18
							lx1 := dx + dw*0.82
							if i == 2 {
								lx1 = dx + dw*0.6
							}
							if fy >= ly && fy < ly+dh*0.06 && fx >= lx0 && fx <= lx1 {
								c = line
							}
						}
					}
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return downscale(img, size, ss)
}

func insideRoundRect(px, py, x, y, w, h, r float64) bool {
	if px < x || py < y || px > x+w || py > y+h {
		return false
	}
	// Corner regions.
	cx, cy := px, py
	inCorner := false
	var ccx, ccy float64
	switch {
	case px < x+r && py < y+r:
		ccx, ccy, inCorner = x+r, y+r, true
	case px > x+w-r && py < y+r:
		ccx, ccy, inCorner = x+w-r, y+r, true
	case px < x+r && py > y+h-r:
		ccx, ccy, inCorner = x+r, y+h-r, true
	case px > x+w-r && py > y+h-r:
		ccx, ccy, inCorner = x+w-r, y+h-r, true
	}
	if inCorner {
		return math.Hypot(cx-ccx, cy-ccy) <= r
	}
	return true
}

func downscale(src *image.NRGBA, size, ss int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a int
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := src.NRGBAAt(x*ss+dx, y*ss+dy)
					r += int(c.R)
					g += int(c.G)
					b += int(c.B)
					a += int(c.A)
				}
			}
			n := ss * ss
			dst.SetNRGBA(x, y, color.NRGBA{byte(r / n), byte(g / n), byte(b / n), byte(a / n)})
		}
	}
	return dst
}
