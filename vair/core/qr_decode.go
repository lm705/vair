package core

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// decodeQRImage finds and decodes a single QR code in img and returns its text
// payload (a config URL, a subscription link, or a base64 subscription blob).
//
// Stylized QRs — rounded finder patterns, dotted modules, a logo in the middle
// (e.g. Chrome's "Create QR code" with the dino) — trip up a single naive decode
// pass, so we try several:
//   - box-averaged downscales first: shrinking dotted/anti-aliased modules so
//     each cell becomes a solid block the decoder can sample cleanly (this is
//     what rescues the dotted Chrome/GitHub style), AND it keeps a 4K screen
//     grab fast;
//   - then the original;
//   - and on an image larger than 2000px a side, which is not tried whole,
//     the original in overlapping tiles: the downscales alone missed codes
//     whose modules a whole-number factor blurs — a 207px code on a 4K screen,
//     a 138px one at 2560x1440 — while 1920x1080 found every size;
//   - each with both binarizers (hybrid + global histogram) and the inverted
//     luminance (light-on-dark codes);
//   - all with TRY_HARDER.
//
// First hit wins.
func decodeQRImage(img image.Image) (string, error) {
	reader := qrcode.NewQRCodeReader()
	hints := map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_TRY_HARDER: true,
	}
	var lastErr error

	variants := make([]image.Image, 0, 4)
	for _, f := range []int{4, 3, 2} {
		if d := boxDownscale(img, f); d != nil {
			variants = append(variants, d)
		}
	}
	// Include the full-res original only when it isn't huge: TRY_HARDER on a 4K
	// screen grab is multi-second, and the downscales above already cover that
	// case (4K ÷ {4,3,2} = 960/1280/1920px). A normal file/QR is well under this.
	b := img.Bounds()
	if b.Dx() <= 2000 && b.Dy() <= 2000 {
		variants = append(variants, img)
	} else {
		variants = append(variants, fullResTiles(img)...)
	}

	for _, v := range variants {
		src := gozxing.NewLuminanceSourceFromImage(v)
		for _, s := range []gozxing.LuminanceSource{src, gozxing.NewInvertedLuminanceSource(src)} {
			for _, bz := range []gozxing.Binarizer{gozxing.NewHybridBinarizer(s), gozxing.NewGlobalHistgramBinarizer(s)} {
				bmp, err := gozxing.NewBinaryBitmap(bz)
				if err != nil {
					lastErr = err
					continue
				}
				if res, derr := reader.Decode(bmp, hints); derr == nil {
					return res.GetText(), nil
				} else {
					lastErr = derr
				}
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no QR code found")
	}
	return "", lastErr
}

// boxDownscale averages factor×factor blocks into one gray pixel. Averaging
// blends a module's dot + surrounding gap into a single mid-tone the binarizer
// then thresholds to a clean cell — the trick that makes dotted QRs decodable.
// Returns nil when the factor is trivial or the result would be too small.
// fullResTiles cuts a large image into overlapping tiles at full resolution.
// A tile is at most qrTile pixels a side and the next one starts qrTileStep
// further on, so any code up to qrTile-qrTileStep pixels across lies whole in
// at least one of them; a larger code survives the downscales anyway.
func fullResTiles(img image.Image) []image.Image {
	sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil
	}
	b := img.Bounds()
	starts := func(lo, hi int) []int {
		if hi-lo <= qrTile {
			return []int{lo}
		}
		var out []int
		for p := lo; ; p += qrTileStep {
			if p+qrTile >= hi {
				out = append(out, hi-qrTile)
				return out
			}
			out = append(out, p)
		}
	}
	var tiles []image.Image
	for _, y := range starts(b.Min.Y, b.Max.Y) {
		for _, x := range starts(b.Min.X, b.Max.X) {
			r := image.Rect(x, y, x+qrTile, y+qrTile).Intersect(b)
			tiles = append(tiles, sub.SubImage(r))
		}
	}
	return tiles
}

const (
	qrTile     = 1600
	qrTileStep = 1200
)

func boxDownscale(img image.Image, factor int) image.Image {
	if factor < 2 {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx()/factor, b.Dy()/factor
	if w < 80 || h < 80 {
		return nil
	}
	out := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum, cnt uint32
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					g := color.GrayModel.Convert(img.At(b.Min.X+x*factor+dx, b.Min.Y+y*factor+dy)).(color.Gray)
					sum += uint32(g.Y)
					cnt++
				}
			}
			out.SetGray(x, y, color.Gray{Y: uint8(sum / cnt)})
		}
	}
	return out
}

// decodeQRBytes decodes a QR code from raw image bytes (PNG/JPEG/GIF).
func decodeQRBytes(data []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	return decodeQRImage(img)
}

// DecodeQRBytes / DecodeQRImage expose the decoder to the shell (screen
// capture and image-file scanning live there — they touch Win32).
func DecodeQRBytes(data []byte) (string, error) { return decodeQRBytes(data) }

// DecodeQRImage decodes a QR from an already-decoded image (screen grabs).
func DecodeQRImage(img image.Image) (string, error) { return decodeQRImage(img) }
