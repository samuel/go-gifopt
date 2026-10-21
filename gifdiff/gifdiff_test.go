package gifdiff_test

import (
	"image"
	"image/color"
	"image/gif"
	"slices"
	"testing"

	"samuelks.com/gifopt/gifdiff"
)

var (
	red   = color.RGBA{R: 0xFF, A: 0xFF}
	green = color.RGBA{G: 0xFF, A: 0xFF}
)

func pal() color.Palette {
	return color.Palette{red, green, color.RGBA{}}
}

// frame builds a paletted image over r with every pixel set to idx.
func frame(r image.Rectangle, idx byte) *image.Paletted {
	im := image.NewPaletted(r, pal())
	for i := range im.Pix {
		im.Pix[i] = idx
	}
	return im
}

func animation(images []*image.Paletted, delays []int, w, h int) *gif.GIF {
	return &gif.GIF{
		Image:  images,
		Delay:  delays,
		Config: image.Config{ColorModel: pal(), Width: w, Height: h},
	}
}

func TestRenderCoalescesIdenticalFrames(t *testing.T) {
	// Four frames, all rendering the same canvas.
	g := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 0),
	}, []int{5, 7, 3, 1}, 4, 1)

	states := gifdiff.Render(g)
	if len(states) != 1 {
		t.Fatalf("states: got %d, want 1 (identical frames must coalesce)", len(states))
	}
	if states[0].Delay != 16 {
		t.Errorf("coalesced delay: got %d, want 16 (5+7+3+1)", states[0].Delay)
	}
	if want := []int{0, 1, 2, 3}; !slices.Equal(states[0].Frames, want) {
		t.Errorf("source frames: got %v, want %v", states[0].Frames, want)
	}
}

func TestRenderKeepsDistinctFrames(t *testing.T) {
	g := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 1),
		frame(image.Rect(0, 0, 4, 1), 0),
	}, []int{5, 5, 5}, 4, 1)

	states := gifdiff.Render(g)
	if len(states) != 3 {
		t.Fatalf("states: got %d, want 3", len(states))
	}
	// A frame that returns to an earlier appearance is still its own state:
	// coalescing is only for *consecutive* identical canvases.
	for i, s := range states {
		if s.Delay != 5 {
			t.Errorf("state %d delay: got %d, want 5", i, s.Delay)
		}
	}
}

func TestCompareIdentical(t *testing.T) {
	build := func() *gif.GIF {
		return animation([]*image.Paletted{
			frame(image.Rect(0, 0, 4, 2), 0),
			frame(image.Rect(0, 0, 4, 2), 1),
		}, []int{10, 10}, 4, 2)
	}

	res := gifdiff.Compare(build(), build())
	if res.HasVisualDiff() {
		t.Errorf("identical GIFs reported a visual difference: %v", res.VisualMessages())
	}
	if len(res.InfoMessages()) != 0 {
		t.Errorf("identical GIFs reported info differences: %v", res.InfoMessages())
	}
}

// TestCompareIgnoresStructure checks that full frames and delta frames with
// transparency compare equal.
func TestCompareIgnoresStructure(t *testing.T) {
	// Full frames: red row, then red row with a green pixel at x=2.
	full := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		func() *image.Paletted {
			im := frame(image.Rect(0, 0, 4, 1), 0)
			im.Pix[2] = 1
			return im
		}(),
	}, []int{10, 10}, 4, 1)

	// Delta frames: the second frame is a 1x1 sub-image holding only the change.
	delta := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(2, 0, 3, 1), 1),
	}, []int{10, 10}, 4, 1)

	res := gifdiff.Compare(full, delta)
	if res.HasVisualDiff() {
		t.Errorf("structurally different but visually identical GIFs differ: %v", res.VisualMessages())
	}
}

// TestCompareMergedFramesAreNotVisual checks that collapsing duplicate frames, as
// Optimize does, is reported as info rather than as a visual change.
func TestCompareMergedFramesAreNotVisual(t *testing.T) {
	many := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 0),
	}, []int{4, 4, 4}, 4, 1)

	one := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
	}, []int{12}, 4, 1)

	res := gifdiff.Compare(many, one)
	if res.HasVisualDiff() {
		t.Errorf("merging duplicate frames reported a visual difference: %v", res.VisualMessages())
	}
	if res.FramesA != 3 || res.FramesB != 1 {
		t.Errorf("frame counts: got %d and %d, want 3 and 1", res.FramesA, res.FramesB)
	}
	info := res.InfoMessages()
	if len(info) != 1 {
		t.Fatalf("info messages: got %v, want exactly one about frame count", info)
	}
}

func TestComparePixelDifference(t *testing.T) {
	a := animation([]*image.Paletted{frame(image.Rect(0, 0, 4, 1), 0)}, []int{10}, 4, 1)
	b := animation([]*image.Paletted{frame(image.Rect(0, 0, 4, 1), 1)}, []int{10}, 4, 1)

	res := gifdiff.Compare(a, b)
	if !res.HasVisualDiff() {
		t.Fatal("GIFs with different pixels must report a visual difference")
	}
	if len(res.Diffs) != 1 {
		t.Fatalf("diffs: got %d, want 1", len(res.Diffs))
	}
	if res.Diffs[0].DiffPixels != 4 {
		t.Errorf("differing pixels: got %d, want 4", res.Diffs[0].DiffPixels)
	}
}

// TestCompareDurationDifference checks the one timing difference that is visual: the
// same images shown for different lengths of time.
func TestCompareDurationDifference(t *testing.T) {
	a := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 1),
	}, []int{10, 10}, 4, 1)
	b := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 1),
	}, []int{50, 10}, 4, 1)

	res := gifdiff.Compare(a, b)
	if !res.HasVisualDiff() {
		t.Fatal("the same frames shown for different durations must be a visual difference")
	}
	if len(res.Diffs) != 1 || res.Diffs[0].DiffPixels != 0 {
		t.Fatalf("diffs: got %+v, want one entry with 0 differing pixels", res.Diffs)
	}
	if res.Diffs[0].DelayA != 10 || res.Diffs[0].DelayB != 50 {
		t.Errorf("delays: got %d and %d, want 10 and 50", res.Diffs[0].DelayA, res.Diffs[0].DelayB)
	}
}

func TestCompareStateCountDifference(t *testing.T) {
	a := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 1),
	}, []int{10, 10}, 4, 1)
	b := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
	}, []int{20}, 4, 1)

	res := gifdiff.Compare(a, b)
	if !res.HasVisualDiff() {
		t.Fatal("dropping a visually distinct state must be a visual difference")
	}
	if res.StatesA != 2 || res.StatesB != 1 {
		t.Errorf("state counts: got %d and %d, want 2 and 1", res.StatesA, res.StatesB)
	}
}

func TestCompareCanvasSizeDifference(t *testing.T) {
	a := animation([]*image.Paletted{frame(image.Rect(0, 0, 4, 2), 0)}, []int{10}, 4, 2)
	b := animation([]*image.Paletted{frame(image.Rect(0, 0, 4, 2), 0)}, []int{10}, 8, 2)

	res := gifdiff.Compare(a, b)
	if !res.HasVisualDiff() {
		t.Fatal("a different logical screen size must be a visual difference")
	}
	msgs := res.VisualMessages()
	if len(msgs) == 0 {
		t.Fatal("expected a message about canvas size")
	}
}

// TestCompareDisposalRenderingEquivalence checks that repainting and background
// disposal are compared on what they render, not on their disposal bytes.
func TestCompareDisposalRenderingEquivalence(t *testing.T) {
	// A: frame 1 has background disposal, so frame 2 sees a cleared canvas.
	a := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 1),
		frame(image.Rect(0, 0, 2, 1), 0),
	}, []int{10, 10, 10}, 4, 1)
	a.Disposal = []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone}

	// B: no disposal, but frame 2 explicitly paints what A ends up with: red on the
	// left half, transparent on the right.
	third := frame(image.Rect(0, 0, 4, 1), 2) // all transparent
	third.Pix[0] = 0
	third.Pix[1] = 0
	b := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 1),
		third,
	}, []int{10, 10, 10}, 4, 1)
	b.Disposal = []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone}

	res := gifdiff.Compare(a, b)
	if res.HasVisualDiff() {
		t.Errorf("equivalent renderings differ: %v", res.VisualMessages())
	}
}

func TestDuration(t *testing.T) {
	g := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 2, 1), 0),
		frame(image.Rect(0, 0, 2, 1), 1),
	}, []int{7, 13}, 2, 1)
	if got := gifdiff.Duration(g); got != 20 {
		t.Errorf("Duration: got %d, want 20", got)
	}
}

// TestCompareZeroDelays checks that an animation whose every delay is zero is still
// compared on its images. A time-sampled comparison would pass anything.
func TestCompareZeroDelays(t *testing.T) {
	a := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 1),
	}, []int{0, 0}, 4, 1)
	b := animation([]*image.Paletted{
		frame(image.Rect(0, 0, 4, 1), 0),
		frame(image.Rect(0, 0, 4, 1), 0),
	}, []int{0, 0}, 4, 1)

	res := gifdiff.Compare(a, b)
	if !res.HasVisualDiff() {
		t.Error("zero-delay frames with different images must still report a difference")
	}
}

func TestCompareLoopCountIsInfoOnly(t *testing.T) {
	a := animation([]*image.Paletted{frame(image.Rect(0, 0, 2, 1), 0)}, []int{10}, 2, 1)
	b := animation([]*image.Paletted{frame(image.Rect(0, 0, 2, 1), 0)}, []int{10}, 2, 1)
	a.LoopCount = 0
	b.LoopCount = 5

	res := gifdiff.Compare(a, b)
	if res.HasVisualDiff() {
		t.Errorf("loop count must not be a visual difference: %v", res.VisualMessages())
	}
	if len(res.InfoMessages()) != 1 {
		t.Errorf("info messages: got %v, want one about loop count", res.InfoMessages())
	}
}
