package gifcanvas

import (
	"image"
	"image/color"
	"image/gif"
	"testing"
)

var (
	red         = color.RGBA{R: 0xFF, A: 0xFF}
	green       = color.RGBA{G: 0xFF, A: 0xFF}
	blue        = color.RGBA{B: 0xFF, A: 0xFF}
	transparent = color.RGBA{}
)

// pal is red, green, blue, transparent.
func pal() color.Palette {
	return color.Palette{red, green, blue, transparent}
}

// frame builds a paletted image over r where every pixel is idx.
func frame(r image.Rectangle, idx byte) *image.Paletted {
	im := image.NewPaletted(r, pal())
	for i := range im.Pix {
		im.Pix[i] = idx
	}
	return im
}

func TestNewIsTransparent(t *testing.T) {
	c := New(4, 3)
	if got := c.Rect; got != image.Rect(0, 0, 4, 3) {
		t.Fatalf("bounds: got %v, want 0,0-4,3", got)
	}
	for i, v := range c.Pix {
		if v != 0 {
			t.Fatalf("Pix[%d] = %d, want 0 (a new canvas must be fully transparent)", i, v)
		}
	}
}

func TestCompositeSkipsTransparentPixels(t *testing.T) {
	c := New(4, 1)
	c.Composite(frame(image.Rect(0, 0, 4, 1), 0)) // all red

	// A frame that is entirely the transparent index must leave the canvas alone.
	c.Composite(frame(image.Rect(0, 0, 4, 1), 3))

	for x := range 4 {
		if got := c.RGBAAt(x, 0); got != red {
			t.Errorf("pixel %d: got %v, want red — transparent pixels must not overwrite", x, got)
		}
	}
}

func TestCompositeOverwritesOpaquePixels(t *testing.T) {
	c := New(4, 1)
	c.Composite(frame(image.Rect(0, 0, 4, 1), 0)) // red
	c.Composite(frame(image.Rect(1, 0, 3, 1), 1)) // green over the middle two

	want := []color.RGBA{red, green, green, red}
	for x, w := range want {
		if got := c.RGBAAt(x, 0); got != w {
			t.Errorf("pixel %d: got %v, want %v", x, got, w)
		}
	}
}

func TestCompositeClipsToCanvas(t *testing.T) {
	c := New(4, 2)
	// A frame extending past the canvas must be clipped, not panic.
	c.Composite(frame(image.Rect(2, 1, 10, 9), 1))

	if got := c.RGBAAt(3, 1); got != green {
		t.Errorf("in-bounds pixel: got %v, want green", got)
	}
	if got := c.RGBAAt(0, 0); got != transparent {
		t.Errorf("untouched pixel: got %v, want transparent", got)
	}
}

func TestClearRect(t *testing.T) {
	c := New(4, 2)
	c.Composite(frame(image.Rect(0, 0, 4, 2), 2)) // all blue
	c.ClearRect(image.Rect(1, 0, 3, 1))

	if got := c.RGBAAt(1, 0); got != transparent {
		t.Errorf("cleared pixel: got %v, want transparent", got)
	}
	if got := c.RGBAAt(0, 0); got != blue {
		t.Errorf("pixel outside the cleared rect: got %v, want blue", got)
	}
	if got := c.RGBAAt(1, 1); got != blue {
		t.Errorf("pixel on the next row: got %v, want blue", got)
	}
}

func TestCloneIsIndependent(t *testing.T) {
	c := New(2, 1)
	c.Composite(frame(image.Rect(0, 0, 2, 1), 0))

	clone := c.Clone()
	c.ClearRect(image.Rect(0, 0, 2, 1))

	if got := clone.RGBAAt(0, 0); got != red {
		t.Errorf("clone changed when the original was cleared: got %v, want red", got)
	}
}

func TestCopyFrom(t *testing.T) {
	a := New(2, 1)
	a.Composite(frame(image.Rect(0, 0, 2, 1), 1))
	b := New(2, 1)
	b.CopyFrom(a)

	if !a.Equal(b) {
		t.Error("CopyFrom did not produce an equal canvas")
	}
	a.ClearRect(image.Rect(0, 0, 2, 1))
	if b.RGBAAt(0, 0) != green {
		t.Error("CopyFrom did not make an independent copy")
	}
}

func TestCopyFromMismatchedBoundsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("CopyFrom with mismatched bounds should panic rather than silently truncate")
		}
	}()
	New(2, 1).CopyFrom(New(3, 1))
}

func TestEqualAndCountDiff(t *testing.T) {
	a := New(4, 2)
	b := New(4, 2)
	if !a.Equal(b) {
		t.Error("two blank canvases of the same size must be equal")
	}
	if got := a.CountDiff(b); got != 0 {
		t.Errorf("CountDiff of equal canvases: got %d, want 0", got)
	}

	b.Composite(frame(image.Rect(1, 0, 3, 1), 0))
	if a.Equal(b) {
		t.Error("canvases with different pixels must not be equal")
	}
	if got := a.CountDiff(b); got != 2 {
		t.Errorf("CountDiff: got %d, want 2", got)
	}
}

func TestCountDiffDifferentSizes(t *testing.T) {
	a := New(2, 2) // 4 px
	b := New(4, 2) // 8 px, 4 of them outside a

	if a.Equal(b) {
		t.Error("canvases of different sizes must not be equal")
	}
	// The 4 shared pixels match; the 4 pixels only in b count as differing.
	if got := a.CountDiff(b); got != 4 {
		t.Errorf("CountDiff: got %d, want 4", got)
	}
}

func TestSizeUsesFrameExtentWhenHeaderIsSmall(t *testing.T) {
	g := &gif.GIF{
		Image:  []*image.Paletted{frame(image.Rect(0, 0, 20, 15), 0)},
		Delay:  []int{0},
		Config: image.Config{Width: 4, Height: 4},
	}
	w, h := Size(g)
	if w != 20 || h != 15 {
		t.Errorf("Size: got %dx%d, want 20x15 (frames extend past the header)", w, h)
	}
}

func TestSizeKeepsLargerHeader(t *testing.T) {
	g := &gif.GIF{
		Image:  []*image.Paletted{frame(image.Rect(0, 0, 4, 4), 0)},
		Delay:  []int{0},
		Config: image.Config{Width: 20, Height: 15},
	}
	w, h := Size(g)
	if w != 20 || h != 15 {
		t.Errorf("Size: got %dx%d, want 20x15 (the logical screen is bigger than the frames)", w, h)
	}
}

func TestDisposalNormalization(t *testing.T) {
	g := &gif.GIF{Disposal: []byte{0, gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious}}
	want := []byte{gif.DisposalNone, gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious}
	for i, w := range want {
		if got := Disposal(g, i); got != w {
			t.Errorf("Disposal(%d): got %d, want %d", i, got, w)
		}
	}
	// Past the end of the slice, and for a nil slice, the default applies.
	if got := Disposal(g, 99); got != gif.DisposalNone {
		t.Errorf("Disposal past end: got %d, want DisposalNone", got)
	}
	if got := Disposal(&gif.GIF{}, 0); got != gif.DisposalNone {
		t.Errorf("Disposal with nil slice: got %d, want DisposalNone", got)
	}
}

func TestRenderFramesDisposalNone(t *testing.T) {
	// Frame 1 paints over part of frame 0 and the canvas persists.
	g := &gif.GIF{
		Image: []*image.Paletted{
			frame(image.Rect(0, 0, 4, 1), 0), // red across
			frame(image.Rect(1, 0, 3, 1), 1), // green in the middle
		},
		Delay:    []int{10, 10},
		Disposal: []byte{gif.DisposalNone, gif.DisposalNone},
		Config:   image.Config{Width: 4, Height: 1},
	}

	got := RenderFrames(g)
	if len(got) != 2 {
		t.Fatalf("frame count: got %d, want 2", len(got))
	}
	want := []color.RGBA{red, green, green, red}
	for x, w := range want {
		if c := got[1].RGBAAt(x, 0); c != w {
			t.Errorf("frame 1 pixel %d: got %v, want %v", x, c, w)
		}
	}
}

func TestRenderFramesDisposalBackgroundClearsToTransparent(t *testing.T) {
	// Frame 1 has background disposal, so its rect is transparent afterwards, not
	// restored to frame 0's red or filled with the background color.
	g := &gif.GIF{
		Image: []*image.Paletted{
			frame(image.Rect(0, 0, 4, 1), 0), // red across
			frame(image.Rect(1, 0, 3, 1), 1), // green in the middle
			frame(image.Rect(0, 0, 1, 1), 2), // blue at the far left
		},
		Delay:           []int{10, 10, 10},
		Disposal:        []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone},
		Config:          image.Config{Width: 4, Height: 1, ColorModel: pal()},
		BackgroundIndex: 0, // red: must be ignored
	}

	got := RenderFrames(g)
	want := []color.RGBA{blue, transparent, transparent, red}
	for x, w := range want {
		if c := got[2].RGBAAt(x, 0); c != w {
			t.Errorf("frame 2 pixel %d: got %v, want %v", x, c, w)
		}
	}
}

func TestRenderFramesDisposalPreviousRestores(t *testing.T) {
	// Frame 1 has previous disposal, so frame 2 draws over frame 0's canvas.
	g := &gif.GIF{
		Image: []*image.Paletted{
			frame(image.Rect(0, 0, 4, 1), 0), // red across
			frame(image.Rect(1, 0, 3, 1), 1), // green in the middle
			frame(image.Rect(0, 0, 1, 1), 2), // blue at the far left
		},
		Delay:    []int{10, 10, 10},
		Disposal: []byte{gif.DisposalNone, gif.DisposalPrevious, gif.DisposalNone},
		Config:   image.Config{Width: 4, Height: 1},
	}

	got := RenderFrames(g)

	// Frame 1 itself is still visible while it is displayed.
	if c := got[1].RGBAAt(1, 0); c != green {
		t.Errorf("frame 1 pixel 1: got %v, want green", c)
	}
	// By frame 2 the green is gone: the canvas rolled back to frame 0.
	want := []color.RGBA{blue, red, red, red}
	for x, w := range want {
		if c := got[2].RGBAAt(x, 0); c != w {
			t.Errorf("frame 2 pixel %d: got %v, want %v", x, c, w)
		}
	}
}

func TestRenderFramesIgnoresBackgroundIndex(t *testing.T) {
	// A GIF whose only difference is BackgroundIndex must render identically:
	// the background color is not part of what a viewer sees.
	build := func(bg byte) *gif.GIF {
		return &gif.GIF{
			Image:           []*image.Paletted{frame(image.Rect(1, 0, 3, 1), 1)},
			Delay:           []int{10},
			Config:          image.Config{Width: 4, Height: 1, ColorModel: pal()},
			BackgroundIndex: bg,
		}
	}
	a := RenderFrames(build(0))[0]
	b := RenderFrames(build(2))[0]
	if !a.Equal(b) {
		t.Error("BackgroundIndex changed the rendering; it must be ignored")
	}
	if got := a.RGBAAt(0, 0); got != transparent {
		t.Errorf("uncovered pixel: got %v, want transparent", got)
	}
}

func TestRenderFramesEmptyGIF(t *testing.T) {
	if got := RenderFrames(&gif.GIF{}); len(got) != 0 {
		t.Errorf("RenderFrames of an empty GIF: got %d frames, want 0", len(got))
	}
}

func TestColorsWithin(t *testing.T) {
	opaque := func(r, g, b uint8) color.RGBA { return color.RGBA{R: r, G: g, B: b, A: 0xFF} }
	tests := []struct {
		name string
		a, b color.RGBA
		tol  int
		want bool
	}{
		{"identical, no tolerance", opaque(10, 20, 30), opaque(10, 20, 30), 0, true},
		{"one off, no tolerance", opaque(10, 20, 30), opaque(11, 20, 30), 0, false},
		{"within tolerance", opaque(10, 20, 30), opaque(14, 16, 34), 4, true},
		{"one channel outside", opaque(10, 20, 30), opaque(10, 20, 35), 4, false},
		// Alpha differences are never within tolerance.
		{"transparent vs opaque black", color.RGBA{}, opaque(0, 0, 0), 8, false},
		{"transparent vs transparent", color.RGBA{}, color.RGBA{}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ColorsWithin(tc.a, tc.b, tc.tol); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEqualWithinAndMaxChannelDiff(t *testing.T) {
	pal := color.Palette{
		color.RGBA{R: 100, G: 100, B: 100, A: 0xFF},
		color.RGBA{R: 104, G: 100, B: 100, A: 0xFF}, // 4 away from entry 0
		color.RGBA{}, // transparent
	}
	fill := func(idx byte) *Canvas {
		c := New(2, 2)
		im := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
		for i := range im.Pix {
			im.Pix[i] = idx
		}
		c.Composite(im)
		return c
	}
	base, near, blank := fill(0), fill(1), New(2, 2)

	if got := base.MaxChannelDiff(near); got != 4 {
		t.Errorf("MaxChannelDiff: got %d, want 4", got)
	}
	if base.EqualWithin(near, 3) {
		t.Error("a difference of 4 must not be within a tolerance of 3")
	}
	if !base.EqualWithin(near, 4) {
		t.Error("a difference of 4 must be within a tolerance of 4")
	}
	// EqualWithin(tol) must agree with MaxChannelDiff <= tol.
	for tol := range 8 {
		if base.EqualWithin(near, tol) != (base.MaxChannelDiff(near) <= tol) {
			t.Errorf("tolerance %d: EqualWithin and MaxChannelDiff disagree", tol)
		}
	}
	// Transparent against opaque is the full alpha range apart, whatever the color.
	if got := base.MaxChannelDiff(blank); got != 255 {
		t.Errorf("opaque vs blank: got %d, want 255", got)
	}
	if base.EqualWithin(blank, 200) {
		t.Error("no tolerance short of the full range may equate opaque with transparent")
	}
	// Different sizes never compare close.
	if got := base.MaxChannelDiff(New(4, 4)); got != 255 {
		t.Errorf("mismatched bounds: got %d, want 255", got)
	}
}
