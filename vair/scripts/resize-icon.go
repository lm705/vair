//go:build ignore

// resize-icon regenerates the Linux hicolor icon set from build/appicon.png.
//
//	go run scripts/resize-icon.go
//
// Freedesktop icon lookup keys off the DIRECTORY name, not the image, so a
// 1024×1024 file dropped into .../512x512/apps is found and then rescaled by
// every consumer at every draw. hicolor has no 1024×1024 directory at all, so
// shipping the source icon as-is means panels and the dash fall back to the
// generic-application glyph.
//
// The source is 1024², and every target divides it exactly, so each output is a
// plain box average over an integer n×n block — no filter design, no resampling
// library, no quality argument. Anything that isn't an exact divisor would need
// a real filter and belongs in a tool, not here.
package main

import (
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

const srcSize = 1024

// Powers of two only — 48×48 is a common hicolor size but 1024 is not divisible
// by it, and half-covered edge blocks are exactly the case that needs a real
// filter. Consumers that want 48 scale the 64 down themselves.
var sizes = []int{512, 256, 128, 64, 32}

func main() {
	f, err := os.Open(filepath.Join("build", "appicon.png"))
	if err != nil {
		log.Fatal(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	if b := src.Bounds(); b.Dx() != srcSize || b.Dy() != srcSize {
		log.Fatalf("appicon.png is %dx%d; this tool assumes %d² with integer downscale factors",
			b.Dx(), b.Dy(), srcSize)
	}

	out := filepath.Join("build", "linux", "icons")
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, size := range sizes {
		if srcSize%size != 0 {
			log.Fatalf("%d does not divide %d exactly", size, srcSize)
		}
		dst := boxDownscale(src, size)
		p := filepath.Join(out, "com.vair.app-"+itoa(size)+".png")
		w, err := os.Create(p)
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(w, dst); err != nil {
			log.Fatal(err)
		}
		if err := w.Close(); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s", p)
	}
}

// boxDownscale averages each n×n source block into one destination pixel.
//
// The average is taken on PREMULTIPLIED values (what image.RGBA holds), which is
// the only correct way to average partly transparent pixels: averaging
// straight-alpha colour would let a fully transparent pixel's arbitrary RGB
// bleed into the result and fringe the icon's edges.
func boxDownscale(src image.Image, size int) *image.RGBA {
	n := srcSize / size
	area := uint32(n * n)
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var sr, sg, sb, sa uint32
			for dy := 0; dy < n; dy++ {
				for dx := 0; dx < n; dx++ {
					r, g, bl, a := src.At(b.Min.X+x*n+dx, b.Min.Y+y*n+dy).RGBA()
					sr, sg, sb, sa = sr+r, sg+g, sb+bl, sa+a
				}
			}
			i := dst.PixOffset(x, y)
			// RGBA() returns 16-bit values; >>8 brings each back to 8-bit.
			dst.Pix[i+0] = uint8(sr / area >> 8)
			dst.Pix[i+1] = uint8(sg / area >> 8)
			dst.Pix[i+2] = uint8(sb / area >> 8)
			dst.Pix[i+3] = uint8(sa / area >> 8)
		}
	}
	return dst
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
