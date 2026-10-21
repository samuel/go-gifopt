package frames_test

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"testing"

	"samuelks.com/gifopt/frames"
)

// syntheticAnimation returns full-canvas frames with a moving block, the case the
// delta chain targets: most of each frame is unchanged.
func syntheticAnimation(w, h, frames int) *gif.GIF {
	pal := make(color.Palette, 32)
	for i := range pal {
		pal[i] = color.RGBA{R: uint8(i * 8), G: uint8(255 - i*8), B: uint8(i * 3), A: 0xFF}
	}

	g := &gif.GIF{
		Config:   image.Config{ColorModel: pal, Width: w, Height: h},
		Disposal: make([]byte, frames),
	}
	for f := range frames {
		im := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		// A static background plus a block that moves with the frame index.
		for y := range h {
			for x := range w {
				im.Pix[im.PixOffset(x, y)] = byte((x/8 + y/8) % 32)
			}
		}
		bx := (f * 7) % max(w-16, 1)
		by := (f * 5) % max(h-16, 1)
		for y := by; y < by+16 && y < h; y++ {
			for x := bx; x < bx+16 && x < w; x++ {
				im.Pix[im.PixOffset(x, y)] = byte(f % 32)
			}
		}
		g.Image = append(g.Image, im)
		g.Delay = append(g.Delay, 5)
		g.Disposal[f] = gif.DisposalNone
	}
	return g
}

// BenchmarkOptimize measures the frame optimizer on a synthetic animation.
// Allocations per frame are the number to watch.
func BenchmarkOptimize(b *testing.B) {
	sizes := []struct {
		name          string
		w, h, nFrames int
	}{
		{"64x64x8", 64, 64, 8},
		{"320x240x30", 320, 240, 30},
		{"320x240x120", 320, 240, 120},
	}
	for _, s := range sizes {
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				// Optimize works in place, so each iteration needs fresh frames.
				// The build is included in the measurement for all sizes alike.
				g := syntheticAnimation(s.w, s.h, s.nFrames)
				if err := frames.Optimize(g, frames.Config{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOptimizeCorpusGIF measures the optimizer on a real animation, which
// exercises real palettes, partial frames and existing transparency.
func BenchmarkOptimizeCorpusGIF(b *testing.B) {
	for _, name := range []string{"test_4frame_encoder_test.gif", "You Win This Round Frank.gif"} {
		b.Run(name, func(b *testing.B) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if os.IsNotExist(err) {
				b.Skipf("%s not present", name)
			}
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for range b.N {
				g, err := gif.DecodeAll(bytes.NewReader(data))
				if err != nil {
					b.Fatal(err)
				}
				if err := frames.Optimize(g, frames.Config{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
