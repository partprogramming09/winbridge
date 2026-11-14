package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// solid returns an image filled with one colour.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

func TestDownscaleKeepsAspectAndColour(t *testing.T) {
	img := solid(1920, 1080, color.RGBA{200, 100, 50, 255})
	out := Downscale(img, 960)
	if out.Bounds().Dx() != 960 || out.Bounds().Dy() != 540 {
		t.Fatalf("size %v, want 960x540", out.Bounds())
	}
	if c := out.RGBAAt(100, 100); c != (color.RGBA{200, 100, 50, 255}) {
		t.Fatalf("a flat colour must stay flat, got %v", c)
	}
}

func TestDownscaleAveragesPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			v := uint8(0)
			if x%2 == 1 {
				v = 200
			}
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	out := Downscale(img, 2)
	if c := out.RGBAAt(0, 0); c.R != 100 {
		t.Fatalf("each target pixel averages a 2x2 block of 0 and 200, got %v", c)
	}
}

func TestDownscaleLeavesSmallImagesAlone(t *testing.T) {
	img := solid(100, 50, color.RGBA{1, 2, 3, 255})
	if Downscale(img, 0) != img || Downscale(img, 100) != img || Downscale(img, 200) != img {
		t.Fatal("no resize is expected when maxWidth is 0 or not smaller")
	}
}

func TestEncodeJPEGQualityChangesSize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	high, err := EncodeJPEG(img, 95)
	if err != nil {
		t.Fatal(err)
	}
	low, _ := EncodeJPEG(img, 10)
	if len(low) >= len(high) {
		t.Fatalf("quality 10 (%d bytes) must be smaller than 95 (%d bytes)", len(low), len(high))
	}
	if _, err := jpeg.Decode(bytes.NewReader(high)); err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
}
