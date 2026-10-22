package frames

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"io"
	"strings"
	"testing"

	"samuelks.com/gifopt/gifdiff"
	"samuelks.com/gifopt/gifenc"
	"samuelks.com/gifopt/internal/gifcanvas"
)

var (
	tRed         = color.RGBA{R: 0xFF, A: 0xFF}
	tGreen       = color.RGBA{G: 0xFF, A: 0xFF}
	tBlue        = color.RGBA{B: 0xFF, A: 0xFF}
	tTransparent = color.RGBA{}
)

func testPalette() color.Palette {
	return color.Palette{tRed, tGreen, tBlue, tTransparent}
}

// testGIF assembles a single-palette GIF from frames and delays.
func testGIF(images []*image.Paletted, delays []int, w, h int) *gif.GIF {
	var model color.Palette
	if len(images) > 0 {
		model = images[0].Palette
	}
	return &gif.GIF{
		Image:  images,
		Delay:  delays,
		Config: image.Config{ColorModel: model, Width: w, Height: h},
	}
}

// paletted builds an image over r from a row-major list of palette indices.
func paletted(r image.Rectangle, pal color.Palette, pix ...byte) *image.Paletted {
	im := image.NewPaletted(r, pal)
	if len(pix) != len(im.Pix) {
		panic("paletted: pixel count does not match bounds")
	}
	copy(im.Pix, pix)
	return im
}

func TestSmallestIntValueIndex(t *testing.T) {
	if got := smallestIntValueIndex(nil); got != -1 {
		t.Errorf("empty slice: got %d, want -1", got)
	}
	if got := smallestIntValueIndex([]int{5, 2, 8}); got != 1 {
		t.Errorf("got %d, want 1", got)
	}
	// Ties resolve to the lowest index so results are deterministic.
	if got := smallestIntValueIndex([]int{3, 1, 1}); got != 1 {
		t.Errorf("tie: got %d, want 1 (lowest index)", got)
	}
}

func TestMinOpaqueRect(t *testing.T) {
	pal := testPalette()

	t.Run("all transparent", func(t *testing.T) {
		im := paletted(image.Rect(0, 0, 2, 2), pal, 3, 3, 3, 3)
		if got := minOpaqueRect(im); !got.Empty() {
			t.Errorf("got %v, want an empty rect", got)
		}
	})

	t.Run("tight bounds", func(t *testing.T) {
		// A single opaque pixel at (1,2) in a 4x4 image.
		im := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
		for i := range im.Pix {
			im.Pix[i] = 3
		}
		im.Pix[im.PixOffset(1, 2)] = 0
		if got, want := minOpaqueRect(im), image.Rect(1, 2, 2, 3); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("fully opaque", func(t *testing.T) {
		im := paletted(image.Rect(0, 0, 2, 2), pal, 0, 1, 2, 0)
		if got, want := minOpaqueRect(im), image.Rect(0, 0, 2, 2); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("offset bounds are absolute", func(t *testing.T) {
		// minOpaqueRect must return coordinates in the image's own space, not
		// relative to its origin.
		im := image.NewPaletted(image.Rect(5, 7, 9, 11), pal)
		for i := range im.Pix {
			im.Pix[i] = 3
		}
		im.Pix[im.PixOffset(6, 8)] = 1
		if got, want := minOpaqueRect(im), image.Rect(6, 8, 7, 9); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func TestDedupeImagePalette(t *testing.T) {
	// Entries 0 and 2 are the same color; entry 2 must be collapsed onto 0.
	pal := color.Palette{tRed, tGreen, tRed, tBlue}
	im := paletted(image.Rect(0, 0, 2, 2), pal, 0, 1, 2, 3)

	dedupeImagePalette(im, tTransparent)

	if im.Pix[2] != 0 {
		t.Errorf("duplicate index not remapped: Pix[2] = %d, want 0", im.Pix[2])
	}
	if im.Palette[2] != color.Color(tTransparent) {
		t.Errorf("freed slot: got %v, want transparent", im.Palette[2])
	}
	// Untouched entries keep their colors and pixels keep their appearance.
	if im.Palette[0] != color.Color(tRed) || im.Palette[1] != color.Color(tGreen) || im.Palette[3] != color.Color(tBlue) {
		t.Errorf("non-duplicate entries changed: %v", im.Palette)
	}
}

func TestDedupeImagePaletteNoDuplicates(t *testing.T) {
	pal := testPalette()
	im := paletted(image.Rect(0, 0, 2, 2), pal, 0, 1, 2, 3)
	before := append([]byte(nil), im.Pix...)

	dedupeImagePalette(im, tTransparent)

	for i, v := range im.Pix {
		if v != before[i] {
			t.Fatalf("pixels changed when there were no duplicates: %v vs %v", im.Pix, before)
		}
	}
}

func TestMatchImagePalettes(t *testing.T) {
	// Two frames with disjoint palettes that together fit in 256 colors must end
	// up sharing one palette, with pixels remapped so appearance is unchanged.
	a := paletted(image.Rect(0, 0, 2, 1), color.Palette{tRed, tGreen}, 0, 1)
	b := paletted(image.Rect(0, 0, 2, 1), color.Palette{tBlue, tRed}, 0, 1)

	matchImagePalettes([]*image.Paletted{a, b}, tTransparent)

	if !paletteEqual(a.Palette, b.Palette) {
		t.Fatal("frames do not share a palette after unification")
	}
	// Appearance must be preserved: frame a is red,green and b is blue,red.
	want := [][]color.Color{
		{tRed, tGreen},
		{tBlue, tRed},
	}
	for fi, im := range []*image.Paletted{a, b} {
		for x := range 2 {
			got := im.Palette[im.Pix[x]]
			if !colorEqual(got, want[fi][x]) {
				t.Errorf("frame %d pixel %d: got %v, want %v", fi, x, got, want[fi][x])
			}
		}
	}
}

func TestMatchImagePalettesTooManyColors(t *testing.T) {
	// 257 distinct colors across two frames cannot be unified; the frames must
	// be left alone rather than silently losing colors.
	makeFrame := func(base int, n int) *image.Paletted {
		pal := make(color.Palette, n)
		for i := range pal {
			pal[i] = color.RGBA{R: uint8((base + i) % 256), G: uint8(base), B: uint8(i), A: 0xFF}
		}
		im := image.NewPaletted(image.Rect(0, 0, n, 1), pal)
		for i := range im.Pix {
			im.Pix[i] = byte(i)
		}
		return im
	}
	a := makeFrame(0, 200)
	b := makeFrame(100, 200)
	aPalBefore := append(color.Palette(nil), a.Palette...)

	matchImagePalettes([]*image.Paletted{a, b}, tTransparent)

	if !paletteEqual(a.Palette, aPalBefore) {
		t.Error("palette was modified even though unification was impossible")
	}
}

func TestPalettedImageEqual(t *testing.T) {
	pal := testPalette()

	t.Run("identical", func(t *testing.T) {
		a := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
		b := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
		if !palettedImageEqual(a, b) {
			t.Error("identical images compared unequal")
		}
	})

	t.Run("different pixels", func(t *testing.T) {
		a := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
		b := paletted(image.Rect(0, 0, 2, 1), pal, 0, 2)
		if palettedImageEqual(a, b) {
			t.Error("different images compared equal")
		}
	})

	t.Run("different bounds", func(t *testing.T) {
		a := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
		b := paletted(image.Rect(0, 0, 1, 2), pal, 0, 1)
		if palettedImageEqual(a, b) {
			t.Error("images with different bounds compared equal")
		}
	})

	t.Run("same colors through different palettes", func(t *testing.T) {
		// Index 1 in a and index 0 in b are both green: the images look the same.
		a := paletted(image.Rect(0, 0, 2, 1), color.Palette{tRed, tGreen}, 1, 0)
		b := paletted(image.Rect(0, 0, 2, 1), color.Palette{tGreen, tRed}, 0, 1)
		if !palettedImageEqual(a, b) {
			t.Error("images with equal appearance but different palettes compared unequal")
		}
	})
}

// canvasOf composites frames onto a fresh canvas, in order.
func canvasOf(w, h int, frames ...*image.Paletted) *gifcanvas.Canvas {
	c := gifcanvas.New(w, h)
	for _, f := range frames {
		c.Composite(f)
	}
	return c
}

func TestDiffCanvas(t *testing.T) {
	pal := testPalette()

	t.Run("identical", func(t *testing.T) {
		a := canvasOf(4, 4, paletted(image.Rect(0, 0, 2, 1), pal, 0, 1))
		b := canvasOf(4, 4, paletted(image.Rect(0, 0, 2, 1), pal, 0, 1))
		d := diffCanvas(a, b)
		if !d.diff.Empty() || !d.erase.Empty() {
			t.Errorf("got diff %v erase %v, want both empty", d.diff, d.erase)
		}
	})

	t.Run("single changed pixel", func(t *testing.T) {
		base := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
		next := clonePalettedImage(base)
		next.Pix[next.PixOffset(2, 1)] = 1

		d := diffCanvas(canvasOf(4, 4, next), canvasOf(4, 4, base))
		if want := image.Rect(2, 1, 3, 2); d.diff != want {
			t.Errorf("diff: got %v, want %v", d.diff, want)
		}
		if !d.erase.Empty() {
			t.Errorf("erase: got %v, want empty", d.erase)
		}
	})

	t.Run("bounding box spans every difference", func(t *testing.T) {
		base := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
		next := clonePalettedImage(base)
		next.Pix[next.PixOffset(1, 6)] = 1
		next.Pix[next.PixOffset(5, 2)] = 2

		d := diffCanvas(canvasOf(8, 8, next), canvasOf(8, 8, base))
		if want := image.Rect(1, 2, 6, 7); d.diff != want {
			t.Errorf("diff: got %v, want %v", d.diff, want)
		}
	})

	t.Run("a pixel that must become transparent is an erase", func(t *testing.T) {
		// The canvas is painted; the target leaves that pixel uncovered. No frame
		// can express that, so it has to be reported separately.
		canvas := canvasOf(4, 1, paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 0, 0))
		target := canvasOf(4, 1, paletted(image.Rect(0, 0, 2, 1), pal, 0, 0))

		d := diffCanvas(target, canvas)
		if want := image.Rect(2, 0, 4, 1); d.diff != want {
			t.Errorf("diff: got %v, want %v", d.diff, want)
		}
		if want := image.Rect(2, 0, 4, 1); d.erase != want {
			t.Errorf("erase: got %v, want %v", d.erase, want)
		}
	})
}

func TestPaletteIndex(t *testing.T) {
	pal := color.Palette{tRed, tGreen, tRed, tTransparent}
	pi := newPaletteIndex(pal, 3)

	if got, ok := pi.index(color.RGBA{R: 0xFF, A: 0xFF}); !ok || got != 0 {
		t.Errorf("red: got %d ok=%v, want 0 true", got, ok)
	}
	// A duplicated color resolves to its lowest index, keeping the maximum pixel
	// index, and so the LZW literal width, as small as possible.
	if got, _ := pi.index(color.RGBA{R: 0xFF, A: 0xFF}); got != 0 {
		t.Errorf("duplicate color: got index %d, want the lowest (0)", got)
	}
	if got, ok := pi.index(color.RGBA{}); !ok || got != 3 {
		t.Errorf("transparent: got %d ok=%v, want 3 true", got, ok)
	}
	if _, ok := pi.index(color.RGBA{B: 0xFF, A: 0xFF}); ok {
		t.Error("a color outside the palette must not resolve")
	}

	// Without a transparent slot, a transparent pixel cannot be expressed at all.
	noTrans := newPaletteIndex(color.Palette{tRed, tGreen}, -1)
	if _, ok := noTrans.index(color.RGBA{}); ok {
		t.Error("transparent resolved despite there being no transparent slot")
	}
}

func TestBuildDelta(t *testing.T) {
	pal := testPalette()
	const w, h = 4, 2
	scratch := gifcanvas.New(w, h)

	build := func(t *testing.T, target, canvas *gifcanvas.Canvas, cfg Config) (*image.Paletted, bool) {
		t.Helper()
		pi := newPaletteIndex(pal, 3)
		frame, _, ok, err := buildDelta(diffCanvas(target, canvas), target, canvas, nil, pi, cfg, scratch)
		if err != nil {
			t.Fatalf("buildDelta: %v", err)
		}
		return frame, ok
	}

	t.Run("no change needs no frame", func(t *testing.T) {
		c := canvasOf(w, h, paletted(image.Rect(0, 0, 2, 1), pal, 0, 1))
		frame, ok := build(t, c, canvasOf(w, h, paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)), Config{})
		if !ok {
			t.Fatal("ok = false for an unchanged canvas")
		}
		if frame != nil {
			t.Errorf("got frame %v, want nil", frame.Rect)
		}
	})

	t.Run("frame covers exactly the difference", func(t *testing.T) {
		base := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		next := clonePalettedImage(base)
		next.Pix[next.PixOffset(2, 1)] = 1

		frame, ok := build(t, canvasOf(w, h, next), canvasOf(w, h, base), Config{})
		if !ok {
			t.Fatal("ok = false")
		}
		if want := image.Rect(2, 1, 3, 2); frame.Rect != want {
			t.Errorf("bounds: got %v, want %v", frame.Rect, want)
		}
		if got := frame.Palette[frame.Pix[0]]; !colorEqual(got, tGreen) {
			t.Errorf("pixel color: got %v, want green", got)
		}
	})

	t.Run("a pixel that must be erased is not expressible", func(t *testing.T) {
		canvas := canvasOf(w, h, paletted(image.Rect(0, 0, 2, 1), pal, 0, 0))
		target := gifcanvas.New(w, h) // fully transparent again
		if _, ok := build(t, target, canvas, Config{}); ok {
			t.Error("ok = true; a frame cannot clear a pixel back to transparent")
		}
	})

	t.Run("a missing color is not expressible", func(t *testing.T) {
		// The target holds a color the frame's palette does not have.
		other := color.Palette{color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xFF}}
		target := canvasOf(w, h, paletted(image.Rect(0, 0, 1, 1), other, 0))
		if _, ok := build(t, target, gifcanvas.New(w, h), Config{}); ok {
			t.Error("ok = true despite the palette missing the color")
		}
	})

	t.Run("unchanged pixels inside the frame become transparent", func(t *testing.T) {
		// Changing the last pixel of row 0 and the first of row 1 makes the
		// difference rectangle span the whole image, so most of what it covers is
		// unchanged and can be left transparent.
		base := paletted(image.Rect(0, 0, 4, 2), pal, 0, 0, 1, 1, 0, 0, 1, 1)
		next := clonePalettedImage(base)
		next.Pix[next.PixOffset(3, 0)] = 2
		next.Pix[next.PixOffset(0, 1)] = 2

		frame, ok := build(t, canvasOf(w, h, next), canvasOf(w, h, base),
			Config{DisableFrameCompressibilityCheck: true})
		if !ok {
			t.Fatal("ok = false")
		}
		if want := image.Rect(0, 0, 4, 2); frame.Rect != want {
			t.Fatalf("bounds: got %v, want %v", frame.Rect, want)
		}
		want := [][]byte{
			{3, 3, 3, 2},
			{2, 3, 3, 3},
		}
		for y, row := range want {
			for x, v := range row {
				if got := frame.Pix[frame.PixOffset(x, y)]; got != v {
					t.Errorf("pixel (%d,%d): got index %d, want %d", x, y, got, v)
				}
			}
		}
	})
}

func TestExpandFrame(t *testing.T) {
	pal := testPalette()
	canvas := canvasOf(4, 4, paletted(image.Rect(0, 0, 4, 1), pal, 0, 1, 2, 0))
	frame := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
	pi := newPaletteIndex(pal, 3)

	grown, ok := expandFrame(frame, image.Rect(0, 0, 4, 2), canvas, pi)
	if !ok {
		t.Fatal("expandFrame reported failure")
	}
	if want := image.Rect(0, 0, 4, 2); grown.Rect != want {
		t.Fatalf("bounds: got %v, want %v", grown.Rect, want)
	}
	// The original pixels survive and the added area is transparent, so the frame
	// renders exactly as it did before it grew.
	for x, want := range []byte{0, 1} {
		if got := grown.Pix[grown.PixOffset(x, 0)]; got != want {
			t.Errorf("kept pixel %d: got %d, want %d", x, got, want)
		}
	}
	for _, p := range []image.Point{{X: 2, Y: 0}, {X: 3, Y: 0}, {X: 0, Y: 1}, {X: 3, Y: 1}} {
		if got := grown.Pix[grown.PixOffset(p.X, p.Y)]; got != 3 {
			t.Errorf("added pixel %v: got %d, want 3 (transparent)", p, got)
		}
	}

	// Growing to a rect the frame already covers must not copy anything.
	if same, _ := expandFrame(frame, image.Rect(0, 0, 1, 1), canvas, pi); same != frame {
		t.Error("expandFrame allocated a new image for a rect already covered")
	}
}

func TestFillerFrame(t *testing.T) {
	pal := testPalette()

	t.Run("repaints a color already on screen", func(t *testing.T) {
		target := canvasOf(2, 2, paletted(image.Rect(1, 1, 2, 2), pal, 1))
		frame, ok := fillerFrame(target, newPaletteIndex(pal, 3))
		if !ok {
			t.Fatal("fillerFrame reported failure")
		}
		if want := image.Rect(1, 1, 2, 2); frame.Rect != want {
			t.Errorf("bounds: got %v, want %v", frame.Rect, want)
		}
		if got := frame.Palette[frame.Pix[0]]; !colorEqual(got, tGreen) {
			t.Errorf("color: got %v, want the green already on the canvas", got)
		}
	})

	t.Run("falls back to a transparent pixel on a blank canvas", func(t *testing.T) {
		frame, ok := fillerFrame(gifcanvas.New(2, 2), newPaletteIndex(pal, 3))
		if !ok {
			t.Fatal("fillerFrame reported failure")
		}
		if frame.Pix[0] != 3 {
			t.Errorf("got index %d, want 3 (transparent)", frame.Pix[0])
		}
	})

	t.Run("fails when nothing neutral can be emitted", func(t *testing.T) {
		if _, ok := fillerFrame(gifcanvas.New(2, 2), newPaletteIndex(pal, -1)); ok {
			t.Error("ok = true with a blank canvas and no transparent slot")
		}
	})
}

func TestApplyTransparencyRunBased(t *testing.T) {
	pal := testPalette()
	const tc = 3 // transparent index

	t.Run("two adjacent runs of different colors become transparent", func(t *testing.T) {
		// Canvas holds red,red,green,green. The frame repaints exactly that, so
		// all four pixels are eligible, and they form two runs of different
		// colors — the heuristic commits them to transparent.
		canvas := gifcanvas.New(4, 1)
		canvas.Composite(paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 1, 1))
		frame := paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 1, 1)

		got := applyTransparencyRunBased(frame, canvas, tc, 0)
		for x := range 4 {
			if got.Pix[x] != tc {
				t.Errorf("pixel %d: got %d, want %d (transparent)", x, got.Pix[x], tc)
			}
		}
	})

	t.Run("a single eligible run is left opaque", func(t *testing.T) {
		// Only one run of one color is eligible; making it transparent would
		// not lengthen any run, so it must be preserved.
		canvas := gifcanvas.New(4, 1)
		canvas.Composite(paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 0, 0))
		// The frame differs at the last pixel, so pixels 0..2 are eligible but
		// they are all the same color.
		frame := paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 0, 1)

		got := applyTransparencyRunBased(frame, canvas, tc, 0)
		for x := range 3 {
			if got.Pix[x] == tc {
				t.Errorf("pixel %d became transparent; a single-color run must stay opaque", x)
			}
		}
	})

	t.Run("ineligible pixels are preserved", func(t *testing.T) {
		canvas := gifcanvas.New(4, 1) // fully transparent canvas
		frame := paletted(image.Rect(0, 0, 4, 1), pal, 0, 1, 2, 0)

		got := applyTransparencyRunBased(frame, canvas, tc, 0)
		for x, want := range []byte{0, 1, 2, 0} {
			if got.Pix[x] != want {
				t.Errorf("pixel %d: got %d, want %d", x, got.Pix[x], want)
			}
		}
	})

	t.Run("input is not modified", func(t *testing.T) {
		canvas := gifcanvas.New(4, 1)
		canvas.Composite(paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 1, 1))
		frame := paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 1, 1)

		applyTransparencyRunBased(frame, canvas, tc, 0)
		for x, want := range []byte{0, 0, 1, 1} {
			if frame.Pix[x] != want {
				t.Fatalf("source frame was mutated at pixel %d: got %d, want %d", x, frame.Pix[x], want)
			}
		}
	})
}

func TestClonePalettedImage(t *testing.T) {
	pal := testPalette()
	orig := paletted(image.Rect(2, 3, 5, 6), pal, 0, 1, 2, 1, 2, 0, 2, 0, 1)

	clone := clonePalettedImage(orig)
	if clone.Rect != orig.Rect {
		t.Errorf("bounds: got %v, want %v", clone.Rect, orig.Rect)
	}
	if !palettedImageEqual(clone, orig) {
		t.Error("clone does not match the original")
	}

	clone.Pix[0] = 2
	if orig.Pix[0] == 2 {
		t.Error("mutating the clone changed the original")
	}
}

func TestClonePalettedImageSubImage(t *testing.T) {
	// A sub-image has a stride wider than its width; the clone must copy only
	// the pixels inside the bounds.
	pal := testPalette()
	full := paletted(image.Rect(0, 0, 4, 2), pal, 0, 1, 2, 0, 1, 2, 0, 1)
	sub := full.SubImage(image.Rect(1, 0, 3, 2)).(*image.Paletted)

	clone := clonePalettedImage(sub)
	if clone.Rect != sub.Rect {
		t.Fatalf("bounds: got %v, want %v", clone.Rect, sub.Rect)
	}
	for y := sub.Rect.Min.Y; y < sub.Rect.Max.Y; y++ {
		for x := sub.Rect.Min.X; x < sub.Rect.Max.X; x++ {
			want := sub.Pix[sub.PixOffset(x, y)]
			got := clone.Pix[clone.PixOffset(x, y)]
			if got != want {
				t.Errorf("pixel (%d,%d): got %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestCompressedImageSize(t *testing.T) {
	pal := testPalette()

	// A uniform image must compress smaller than a noisy one of the same size.
	uniform := image.NewPaletted(image.Rect(0, 0, 32, 32), pal)
	noisy := image.NewPaletted(image.Rect(0, 0, 32, 32), pal)
	for i := range noisy.Pix {
		noisy.Pix[i] = byte((i*7 + i/3) % 3)
	}

	uSize, err := compressedImageSize(uniform)
	if err != nil {
		t.Fatalf("uniform: %v", err)
	}
	nSize, err := compressedImageSize(noisy)
	if err != nil {
		t.Fatalf("noisy: %v", err)
	}
	if uSize >= nSize {
		t.Errorf("uniform image compressed to %d bytes, noisy to %d; expected uniform to be smaller", uSize, nSize)
	}
}

func TestCompressedImageSizeIgnoresStridePadding(t *testing.T) {
	// Two images with the same visible pixels must have the same compressed
	// size even when one is a sub-image with padding in its Pix slice.
	pal := testPalette()
	full := image.NewPaletted(image.Rect(0, 0, 8, 4), pal)
	for i := range full.Pix {
		full.Pix[i] = byte(i % 3)
	}
	sub := full.SubImage(image.Rect(2, 1, 6, 3)).(*image.Paletted)
	standalone := clonePalettedImage(sub)

	subSize, err := compressedImageSize(sub)
	if err != nil {
		t.Fatal(err)
	}
	aloneSize, err := compressedImageSize(standalone)
	if err != nil {
		t.Fatal(err)
	}
	if subSize != aloneSize {
		t.Errorf("sub-image compressed to %d bytes, standalone copy to %d; stride padding must be skipped",
			subSize, aloneSize)
	}
}

func TestScale(t *testing.T) {
	pal := testPalette()
	im := paletted(image.Rect(0, 0, 2, 2), pal, 0, 1, 2, 0)
	g := testGIF([]*image.Paletted{im}, []int{10}, 2, 2)

	Scale(g, 2)

	if got := g.Image[0].Rect; got != image.Rect(0, 0, 4, 4) {
		t.Fatalf("bounds: got %v, want 0,0-4,4", got)
	}
	if g.Config.Width != 4 || g.Config.Height != 4 {
		t.Errorf("config: got %dx%d, want 4x4", g.Config.Width, g.Config.Height)
	}
	// Every source pixel becomes a 2x2 block of the same index.
	want := [][]byte{
		{0, 0, 1, 1},
		{0, 0, 1, 1},
		{2, 2, 0, 0},
		{2, 2, 0, 0},
	}
	out := g.Image[0]
	for y, row := range want {
		for x, w := range row {
			if got := out.Pix[out.PixOffset(x, y)]; got != w {
				t.Errorf("pixel (%d,%d): got %d, want %d", x, y, got, w)
			}
		}
	}
}

func TestScaleNoOp(t *testing.T) {
	pal := testPalette()
	for _, mult := range []int{0, 1, -3} {
		im := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
		g := testGIF([]*image.Paletted{im}, []int{10}, 2, 1)
		Scale(g, mult)
		if g.Image[0].Rect != image.Rect(0, 0, 2, 1) {
			t.Errorf("multiplier %d: bounds changed to %v", mult, g.Image[0].Rect)
		}
	}
}

func TestOptimizeRejectsBadInput(t *testing.T) {
	pal := testPalette()

	t.Run("nil", func(t *testing.T) {
		if err := Optimize(nil, Config{}); err == nil {
			t.Error("expected an error for a nil GIF")
		}
	})

	t.Run("no images", func(t *testing.T) {
		if err := Optimize(&gif.GIF{}, Config{}); err == nil {
			t.Error("expected an error for a GIF with no images")
		}
	})

	t.Run("delay count mismatch", func(t *testing.T) {
		im := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
		g := testGIF([]*image.Paletted{im}, []int{10, 10}, 2, 1)
		if err := Optimize(g, Config{}); err == nil {
			t.Error("expected an error when delays and images disagree")
		}
	})

	t.Run("empty palette", func(t *testing.T) {
		im := image.NewPaletted(image.Rect(0, 0, 2, 1), nil)
		g := testGIF([]*image.Paletted{im}, []int{10}, 2, 1)
		if err := Optimize(g, Config{}); err == nil {
			t.Error("expected an error for a frame with no palette")
		}
	})
}

func TestOptimizeAlwaysSetsDisposal(t *testing.T) {
	// Both encoders hard-error when len(Disposal) does not match len(Image), so
	// every return path out of Optimize has to leave the two in step.
	pal := testPalette()
	full := func(idx byte) *image.Paletted {
		return paletted(image.Rect(0, 0, 2, 2), pal, idx, idx, idx, idx)
	}

	tests := []struct {
		name   string
		gif    *gif.GIF
		frames int
	}{
		{"single frame", testGIF([]*image.Paletted{full(0)}, []int{10}, 2, 2), 1},
		{
			// The early return for an animation that collapses to one frame must
			// not leave the input's longer Disposal slice in place.
			"every frame identical",
			testGIF([]*image.Paletted{full(0), full(0), full(0)}, []int{5, 5, 5}, 2, 2),
			1,
		},
		{"distinct frames", testGIF([]*image.Paletted{full(0), full(1)}, []int{5, 5}, 2, 2), 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.gif.Disposal = make([]byte, len(tc.gif.Image))
			if err := Optimize(tc.gif, Config{}); err != nil {
				t.Fatal(err)
			}
			if len(tc.gif.Image) != tc.frames {
				t.Errorf("frames: got %d, want %d", len(tc.gif.Image), tc.frames)
			}
			if len(tc.gif.Disposal) != len(tc.gif.Image) {
				t.Errorf("len(Disposal) = %d, want %d", len(tc.gif.Disposal), len(tc.gif.Image))
			}
			if len(tc.gif.Delay) != len(tc.gif.Image) {
				t.Errorf("len(Delay) = %d, want %d", len(tc.gif.Delay), len(tc.gif.Image))
			}
		})
	}
}

func TestOptimizeKeepsLogicalScreen(t *testing.T) {
	// The frames cover 4x2 of an 8x6 screen. Shrinking the screen to the frame
	// extent would crop a margin that is part of what the animation shows.
	pal := testPalette()
	a := paletted(image.Rect(0, 0, 4, 2), pal, 0, 0, 0, 0, 0, 0, 0, 0)
	b := paletted(image.Rect(0, 0, 4, 2), pal, 1, 1, 1, 1, 0, 0, 0, 0)
	g := testGIF([]*image.Paletted{a, b}, []int{5, 5}, 8, 6)

	if err := Optimize(g, Config{}); err != nil {
		t.Fatal(err)
	}
	if g.Config.Width != 8 || g.Config.Height != 6 {
		t.Errorf("logical screen: got %dx%d, want 8x6", g.Config.Width, g.Config.Height)
	}
}

func TestOptimizeGrowsLogicalScreenToFitFrames(t *testing.T) {
	// A frame reaching past the declared screen would be rejected by the encoders,
	// so the screen grows rather than the frame being cropped.
	pal := testPalette()
	a := paletted(image.Rect(0, 0, 4, 2), pal, 0, 0, 0, 0, 0, 0, 0, 0)
	b := paletted(image.Rect(0, 0, 4, 2), pal, 1, 1, 1, 1, 0, 0, 0, 0)
	g := testGIF([]*image.Paletted{a, b}, []int{5, 5}, 2, 1)

	if err := Optimize(g, Config{}); err != nil {
		t.Fatal(err)
	}
	if g.Config.Width != 4 || g.Config.Height != 2 {
		t.Errorf("logical screen: got %dx%d, want 4x2", g.Config.Width, g.Config.Height)
	}
}

func TestOptimizeRemapsBackgroundIndex(t *testing.T) {
	// Unifying the palettes renumbers every color, so an untouched
	// BackgroundIndex would end up pointing at an unrelated one.
	pal := color.Palette{tGreen, tBlue}
	a := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
	b := paletted(image.Rect(0, 0, 2, 1), pal, 1, 0)
	g := testGIF([]*image.Paletted{a, b}, []int{5, 5}, 2, 1)
	// The input's global palette puts blue at index 2; unification will not.
	g.Config.ColorModel = color.Palette{tRed, tGreen, tBlue, tTransparent}
	g.BackgroundIndex = 2

	if err := Optimize(g, Config{}); err != nil {
		t.Fatal(err)
	}
	got, ok := g.Config.ColorModel.(color.Palette)
	if !ok {
		t.Fatal("no global palette after optimization")
	}
	if int(g.BackgroundIndex) >= len(got) {
		t.Fatalf("BackgroundIndex %d is outside the %d-entry palette", g.BackgroundIndex, len(got))
	}
	if c := got[g.BackgroundIndex]; !colorEqual(c, tBlue) {
		t.Errorf("BackgroundIndex %d holds %v, want the blue it pointed at before", g.BackgroundIndex, c)
	}
}

func TestOptimizeDedupesByRenderedCanvas(t *testing.T) {
	// The two later frames are byte-identical but render differently, because the
	// first of them clears the canvas behind it. Comparing raw frames would merge
	// them and lose a state.
	pal := testPalette()
	base := paletted(image.Rect(0, 0, 4, 2), pal, 0, 0, 0, 0, 0, 0, 0, 0)
	// Green in the corner, transparent over the rest of a 2x2 rect.
	sprite := func() *image.Paletted {
		return paletted(image.Rect(0, 0, 2, 2), pal, 1, 3, 3, 3)
	}
	g := testGIF([]*image.Paletted{base, sprite(), sprite()}, []int{5, 5, 5}, 4, 2)
	g.Disposal = []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone}

	want := gifcanvas.RenderFrames(g)
	if want[1].Equal(want[2]) {
		t.Fatal("fixture is wrong: the two frames already render identically")
	}

	if err := Optimize(g, Config{}); err != nil {
		t.Fatal(err)
	}

	got := gifcanvas.RenderFrames(g)
	if len(got) != len(want) {
		t.Fatalf("frames: got %d, want %d — frames that render differently must not be merged",
			len(got), len(want))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("frame %d renders differently after optimization", i)
		}
	}
}

func TestOptimizeSharedPaletteWithDuplicate(t *testing.T) {
	// Two frames sharing one palette slice, as gif.DecodeAll produces, where the
	// table holds a duplicate entry a later frame uses. Freeing it in place would
	// change what that frame renders.
	build := func() *gif.GIF {
		shared := color.Palette{tRed, tRed, tBlue} // entries 0 and 1 are both red
		a := image.NewPaletted(image.Rect(0, 0, 2, 1), shared)
		a.Pix[0], a.Pix[1] = 2, 2 // blue, blue
		b := image.NewPaletted(image.Rect(0, 0, 2, 1), shared)
		b.Pix[0], b.Pix[1] = 1, 2 // red via the duplicate entry, blue
		return &gif.GIF{
			Image:    []*image.Paletted{a, b},
			Delay:    []int{5, 5},
			Disposal: []byte{gif.DisposalNone, gif.DisposalNone},
			Config:   image.Config{ColorModel: shared, Width: 2, Height: 1},
		}
	}

	reference, subject := build(), build()
	if err := Optimize(subject, Config{}); err != nil {
		t.Fatal(err)
	}
	if res := gifdiff.Compare(reference, subject); res.HasVisualDiff() {
		t.Errorf("optimizing changed the animation:\n  %s", strings.Join(res.VisualMessages(), "\n  "))
	}
}

func TestOptimizeDropsRedundantFrame(t *testing.T) {
	// The middle frame repaints part of the canvas with what is already there, so
	// it renders nothing new and should disappear into its predecessor's delay.
	pal := testPalette()
	first := paletted(image.Rect(0, 0, 4, 1), pal, 0, 0, 0, 0)
	repaint := paletted(image.Rect(1, 0, 3, 1), pal, 0, 0)
	last := paletted(image.Rect(0, 0, 4, 1), pal, 1, 1, 1, 1)
	g := testGIF([]*image.Paletted{first, repaint, last}, []int{5, 7, 9}, 4, 1)

	reference := testGIF([]*image.Paletted{
		clonePalettedImage(first), clonePalettedImage(repaint), clonePalettedImage(last),
	}, []int{5, 7, 9}, 4, 1)

	if err := Optimize(g, Config{}); err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 2 {
		t.Fatalf("frames: got %d, want 2 (the redundant frame must be dropped)", len(g.Image))
	}
	if g.Delay[0] != 12 {
		t.Errorf("delay of the surviving frame: got %d, want 12 (5+7 merged)", g.Delay[0])
	}
	if res := gifdiff.Compare(reference, g); res.HasVisualDiff() {
		t.Errorf("dropping the frame changed the animation:\n  %s", strings.Join(res.VisualMessages(), "\n  "))
	}
}

func TestOptimizeKeepsFrameThatOnlyCarriesAClear(t *testing.T) {
	// The input clears the canvas after the first frame and the second paints
	// nothing. The second frame is what displays the cleared canvas; dropping it
	// would never show it.
	pal := testPalette()
	first := paletted(image.Rect(0, 0, 2, 1), pal, 0, 0)
	blank := paletted(image.Rect(0, 0, 2, 1), pal, 3, 3) // all transparent
	g := &gif.GIF{
		Image:    []*image.Paletted{first, blank},
		Delay:    []int{5, 9},
		Disposal: []byte{gif.DisposalBackground, gif.DisposalNone},
		Config:   image.Config{ColorModel: pal, Width: 2, Height: 1},
	}
	reference := &gif.GIF{
		Image:    []*image.Paletted{clonePalettedImage(first), clonePalettedImage(blank)},
		Delay:    []int{5, 9},
		Disposal: []byte{gif.DisposalBackground, gif.DisposalNone},
		Config:   image.Config{ColorModel: pal, Width: 2, Height: 1},
	}

	if err := Optimize(g, Config{}); err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 2 {
		t.Fatalf("frames: got %d, want 2 (the frame carrying the clear must survive)", len(g.Image))
	}
	if g.Disposal[0] != gif.DisposalBackground {
		t.Errorf("disposal of the first frame: got %d, want DisposalBackground", g.Disposal[0])
	}
	if got := g.Image[1].Rect; got.Dx() != 1 || got.Dy() != 1 {
		t.Errorf("second frame bounds %v: want a 1x1 filler, since it paints nothing", got)
	}
	if res := gifdiff.Compare(reference, g); res.HasVisualDiff() {
		t.Errorf("the animation changed:\n  %s", strings.Join(res.VisualMessages(), "\n  "))
	}
}

func TestScaleMultiplierThree(t *testing.T) {
	pal := testPalette()
	im := paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)
	g := testGIF([]*image.Paletted{im}, []int{10}, 2, 1)

	Scale(g, 3)

	out := g.Image[0]
	if want := image.Rect(0, 0, 6, 3); out.Rect != want {
		t.Fatalf("bounds: got %v, want %v", out.Rect, want)
	}
	for y := range 3 {
		for x := range 6 {
			want := byte(0)
			if x >= 3 {
				want = 1
			}
			if got := out.Pix[out.PixOffset(x, y)]; got != want {
				t.Errorf("pixel (%d,%d): got %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestScaleOffsetFrame(t *testing.T) {
	// A partial frame: its origin has to scale with everything else, and its pixels
	// have to be read from the right offsets.
	pal := testPalette()
	im := paletted(image.Rect(3, 2, 5, 3), pal, 1, 2)
	g := testGIF([]*image.Paletted{im}, []int{10}, 8, 4)

	Scale(g, 2)

	out := g.Image[0]
	if want := image.Rect(6, 4, 10, 6); out.Rect != want {
		t.Fatalf("bounds: got %v, want %v", out.Rect, want)
	}
	for y := 4; y < 6; y++ {
		for x := 6; x < 10; x++ {
			want := byte(1)
			if x >= 8 {
				want = 2
			}
			if got := out.Pix[out.PixOffset(x, y)]; got != want {
				t.Errorf("pixel (%d,%d): got %d, want %d", x, y, got, want)
			}
		}
	}
	if g.Config.Width != 16 || g.Config.Height != 8 {
		t.Errorf("logical screen: got %dx%d, want 16x8", g.Config.Width, g.Config.Height)
	}
}

func TestScaleSubImageSource(t *testing.T) {
	// A sub-image has a stride wider than its bounds; reading it as if the rows were
	// contiguous would pick up the neighboring pixels.
	pal := testPalette()
	full := paletted(image.Rect(0, 0, 4, 2), pal, 0, 1, 2, 0, 2, 1, 0, 2)
	sub := full.SubImage(image.Rect(1, 0, 3, 2)).(*image.Paletted)
	g := testGIF([]*image.Paletted{sub}, []int{10}, 4, 2)

	Scale(g, 2)

	out := g.Image[0]
	if want := image.Rect(2, 0, 6, 4); out.Rect != want {
		t.Fatalf("bounds: got %v, want %v", out.Rect, want)
	}
	// The sub-image is [1 2 / 1 0]; each pixel becomes a 2x2 block.
	want := [][]byte{
		{1, 1, 2, 2},
		{1, 1, 2, 2},
		{1, 1, 0, 0},
		{1, 1, 0, 0},
	}
	for y, row := range want {
		for x, v := range row {
			if got := out.Pix[out.PixOffset(2+x, y)]; got != v {
				t.Errorf("pixel (%d,%d): got %d, want %d", 2+x, y, got, v)
			}
		}
	}
}

func TestScaleClampsMultiplier(t *testing.T) {
	pal := testPalette()
	build := func() *gif.GIF {
		return testGIF([]*image.Paletted{paletted(image.Rect(0, 0, 2, 1), pal, 0, 1)}, []int{10}, 2, 1)
	}
	clamped, atMax := build(), build()
	Scale(clamped, MaxScaleMultiplier*4)
	Scale(atMax, MaxScaleMultiplier)

	if clamped.Image[0].Rect != atMax.Image[0].Rect {
		t.Errorf("bounds: got %v, want %v (a larger multiplier must clamp)",
			clamped.Image[0].Rect, atMax.Image[0].Rect)
	}
	if clamped.Config.Width != atMax.Config.Width || clamped.Config.Height != atMax.Config.Height {
		t.Errorf("logical screen: got %dx%d, want %dx%d",
			clamped.Config.Width, clamped.Config.Height, atMax.Config.Width, atMax.Config.Height)
	}
}

func TestScalePreservesRendering(t *testing.T) {
	// Scaling must not change what the animation shows: same frame count, delays and
	// disposal, everything twice as big.
	pal := testPalette()
	first := paletted(image.Rect(0, 0, 4, 2), pal, 0, 0, 0, 0, 0, 0, 0, 0)
	second := paletted(image.Rect(1, 0, 3, 1), pal, 1, 2)
	g := &gif.GIF{
		Image:    []*image.Paletted{first, second},
		Delay:    []int{5, 7},
		Disposal: []byte{gif.DisposalNone, gif.DisposalBackground},
		Config:   image.Config{ColorModel: pal, Width: 4, Height: 2},
	}
	before := gifcanvas.RenderFrames(g)

	Scale(g, 2)

	after := gifcanvas.RenderFrames(g)
	if len(after) != len(before) {
		t.Fatalf("frames: got %d, want %d", len(after), len(before))
	}
	if g.Delay[0] != 5 || g.Delay[1] != 7 {
		t.Errorf("delays changed: %v", g.Delay)
	}
	if g.Disposal[1] != gif.DisposalBackground {
		t.Errorf("disposal changed: %v", g.Disposal)
	}
	for i := range before {
		for y := before[i].Rect.Min.Y; y < before[i].Rect.Max.Y; y++ {
			for x := before[i].Rect.Min.X; x < before[i].Rect.Max.X; x++ {
				want := before[i].RGBAAt(x, y)
				// Every pixel of the original becomes a 2x2 block of itself.
				for _, p := range []image.Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}, {X: 1, Y: 1}} {
					if got := after[i].RGBAAt(2*x+p.X, 2*y+p.Y); got != want {
						t.Fatalf("frame %d pixel (%d,%d)+%v: got %v, want %v", i, x, y, p, got, want)
					}
				}
			}
		}
	}
}

func TestPixMatchesCanvasTolerance(t *testing.T) {
	// Two palette entries four steps apart, plus a transparent one.
	base := color.RGBA{R: 100, G: 100, B: 100, A: 0xFF}
	near := color.RGBA{R: 104, G: 100, B: 100, A: 0xFF}
	pal := color.Palette{base, near, color.RGBA{}}

	canvas := gifcanvas.New(1, 1)
	canvas.Composite(paletted(image.Rect(0, 0, 1, 1), pal, 0))
	frame := paletted(image.Rect(0, 0, 1, 1), pal, 1)

	if pixMatchesCanvas(frame, canvas, 0, 0, 0) {
		t.Error("a difference of 4 must not match exactly")
	}
	if pixMatchesCanvas(frame, canvas, 0, 0, 3) {
		t.Error("a difference of 4 must not match within a tolerance of 3")
	}
	if !pixMatchesCanvas(frame, canvas, 0, 0, 4) {
		t.Error("a difference of 4 must match within a tolerance of 4")
	}

	// A frame pixel over a transparent canvas is not a match at any tolerance:
	// leaving it alone would show nothing where something belongs.
	if pixMatchesCanvas(frame, gifcanvas.New(1, 1), 0, 0, 255) {
		t.Error("an opaque pixel over a transparent canvas must never match")
	}
}

func TestOptimizeToleranceBoundsTheError(t *testing.T) {
	// A checkerboard, then the same checkerboard three steps off in every pixel. With
	// the tolerance the second frame becomes one flat transparent region. A uniform
	// frame would compress the same either way, and the exact candidate would win the
	// tie.
	pal := color.Palette{
		color.RGBA{R: 100, G: 100, B: 100, A: 0xFF},
		color.RGBA{R: 150, G: 150, B: 150, A: 0xFF},
		color.RGBA{R: 103, G: 100, B: 100, A: 0xFF}, // 3 from entry 0
		color.RGBA{R: 150, G: 153, B: 150, A: 0xFF}, // 3 from entry 1
		color.RGBA{}, // transparent slot
	}
	const size = 16
	build := func() *gif.GIF {
		first := image.NewPaletted(image.Rect(0, 0, size, size), pal)
		second := image.NewPaletted(image.Rect(0, 0, size, size), pal)
		for y := range size {
			for x := range size {
				checker := byte((x + y) % 2)
				first.Pix[first.PixOffset(x, y)] = checker
				second.Pix[second.PixOffset(x, y)] = checker + 2
			}
		}
		return &gif.GIF{
			Image:    []*image.Paletted{first, second},
			Delay:    []int{5, 7},
			Disposal: []byte{gif.DisposalNone, gif.DisposalNone},
			Config:   image.Config{ColorModel: pal, Width: size, Height: size},
		}
	}

	for _, tc := range []struct {
		tolerance int
		wantDiff  bool
	}{
		{0, false}, {2, false}, {3, true}, {8, true},
	} {
		reference, subject := build(), build()
		if err := Optimize(subject, Config{LossyTransparencyTolerance: tc.tolerance}); err != nil {
			t.Fatalf("tolerance %d: %v", tc.tolerance, err)
		}
		res := gifdiff.Compare(reference, subject)

		// Whatever the tolerance, the structure and the timing are untouched.
		if res.StatesA != res.StatesB {
			t.Errorf("tolerance %d: states %d vs %d; a tolerance must not drop a state",
				tc.tolerance, res.StatesA, res.StatesB)
		}
		if len(subject.Image) != 2 {
			t.Errorf("tolerance %d: %d frames, want 2", tc.tolerance, len(subject.Image))
		}
		if gifdiff.Duration(subject) != 12 {
			t.Errorf("tolerance %d: duration %d, want 12", tc.tolerance, gifdiff.Duration(subject))
		}

		worst := 0
		for _, d := range res.Diffs {
			worst = max(worst, d.MaxDelta)
			if d.DelayA != d.DelayB {
				t.Errorf("tolerance %d: timing changed at state %d", tc.tolerance, d.Index)
			}
		}
		switch {
		case tc.wantDiff && worst == 0:
			t.Errorf("tolerance %d: nothing was left alone, so the tolerance did nothing", tc.tolerance)
		case !tc.wantDiff && worst != 0:
			t.Errorf("tolerance %d: pixels changed by %d, but 3 is outside this tolerance",
				tc.tolerance, worst)
		}
		if worst > tc.tolerance {
			t.Errorf("tolerance %d: pixels changed by up to %d, which is over the bound",
				tc.tolerance, worst)
		}
	}
}

func TestOptimizeClipsFramesToScreen(t *testing.T) {
	// No encoder accepts a frame reaching above or left of the screen, and nothing
	// there is ever shown, so every frame has to come out at non-negative
	// coordinates on every return path, including the ones that keep the frames.
	pal := testPalette()
	tests := []struct {
		name   string
		frames []image.Rectangle
	}{
		{"single frame", []image.Rectangle{image.Rect(-2, -1, 3, 2)}},
		{"single frame on one axis", []image.Rectangle{image.Rect(1, -2, 4, 2)}},
		{"single frame entirely off screen", []image.Rectangle{image.Rect(-3, -3, -1, -1)}},
		{"two frames", []image.Rectangle{image.Rect(-2, -1, 4, 3), image.Rect(-1, -2, 2, 2)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			build := func() *gif.GIF {
				var ims []*image.Paletted
				var delays []int
				for i, r := range tc.frames {
					im := image.NewPaletted(r, pal)
					for j := range im.Pix {
						im.Pix[j] = byte((i + j) % 3)
					}
					ims = append(ims, im)
					delays = append(delays, 10)
				}
				return testGIF(ims, delays, 4, 3)
			}
			g := build()
			if err := Optimize(g, Config{}); err != nil {
				t.Fatal(err)
			}
			for i, im := range g.Image {
				if im.Rect.Min.X < 0 || im.Rect.Min.Y < 0 {
					t.Errorf("frame %d at %v reaches off screen", i, im.Rect)
				}
			}
			for _, enc := range []struct {
				name   string
				encode func(io.Writer, *gif.GIF) error
			}{
				{"stdlib", gif.EncodeAll},
				{"gifenc", func(w io.Writer, g *gif.GIF) error { return gifenc.EncodeAll(w, g, nil) }},
			} {
				var buf bytes.Buffer
				if err := enc.encode(&buf, g); err != nil {
					t.Fatalf("%s: encode: %v", enc.name, err)
				}
				out, err := gif.DecodeAll(&buf)
				if err != nil {
					t.Fatalf("%s: decode: %v", enc.name, err)
				}
				if res := gifdiff.Compare(build(), out); res.HasVisualDiff() {
					t.Errorf("%s: rendering changed:\n  %s", enc.name, strings.Join(res.VisualMessages(), "\n  "))
				}
			}
		})
	}
}
