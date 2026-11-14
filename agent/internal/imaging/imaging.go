// Package imaging has the image steps of Snapshot that do not depend on Windows: downscaling and JPEG encoding.
package imaging

import (
	"bytes"
	"image"
	"image/jpeg"
)

// Downscale shrinks img to maxWidth pixels wide, keeping the aspect ratio, by averaging the source pixels that fall in
// each target pixel. It returns img unchanged when maxWidth is 0 or the image is not wider.
func Downscale(img *image.RGBA, maxWidth int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxWidth <= 0 || w <= maxWidth || h == 0 {
		return img
	}
	newH := h * maxWidth / w
	if newH < 1 {
		newH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, maxWidth, newH))
	for y := 0; y < newH; y++ {
		y0, y1 := y*h/newH, (y+1)*h/newH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < maxWidth; x++ {
			x0, x1 := x*w/maxWidth, (x+1)*w/maxWidth
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, n uint32
			for sy := y0; sy < y1; sy++ {
				row := img.Pix[(sy-b.Min.Y+0)*img.Stride:]
				for sx := x0; sx < x1; sx++ {
					p := row[(sx)*4 : (sx)*4+4]
					r += uint32(p[0])
					g += uint32(p[1])
					bl += uint32(p[2])
					n++
				}
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return dst
}

// EncodeJPEG encodes img at the given quality (1 to 100).
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
