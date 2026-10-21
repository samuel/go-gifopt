package frames

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"

	"samuelks.com/gifopt/internal/gifcanvas"
)

// The delta model: each output frame is built from the target canvas (what a viewer
// should see, rendered by internal/gifcanvas using the input's disposal methods) and
// the canvas produced by the frames emitted so far. The input frame at that position
// supplies only its palette and candidate transparency.
//
// A frame cannot clear a pixel back to transparent (only a disposal can), and cannot
// paint a color missing from its palette. When either would be needed, that option is
// unavailable.

// paletteIndex maps composited canvas colors back to palette indices, the inverse of
// gifcanvas.Composite.
type paletteIndex struct {
	palette color.Palette
	byColor map[color.RGBA]byte
	// transparent is the palette entry to use for a pixel that must leave the
	// canvas untouched, or -1 when the palette has no transparent entry.
	transparent int
}

// newPaletteIndex builds the reverse lookup for pal. transparent is this frame's
// transparent slot, or -1 if it has none.
func newPaletteIndex(pal color.Palette, transparent int) *paletteIndex {
	p := &paletteIndex{
		palette:     pal,
		byColor:     make(map[color.RGBA]byte, len(pal)),
		transparent: transparent,
	}
	for i, c := range pal {
		if i > 255 {
			break
		}
		if c == nil {
			continue
		}
		cr, cg, cb, ca := c.RGBA()
		if ca == 0 {
			continue
		}
		key := color.RGBA{R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8), A: uint8(ca >> 8)}
		// Keep the lowest index for a color: a smaller maximum pixel index lets
		// the encoder choose a smaller LZW literal width.
		if _, ok := p.byColor[key]; !ok {
			p.byColor[key] = byte(i)
		}
	}
	return p
}

// index returns the palette entry that renders as c. A fully transparent color maps
// to the frame's transparent slot, which may not exist.
func (p *paletteIndex) index(c color.RGBA) (byte, bool) {
	if c.A == 0 {
		if p.transparent < 0 {
			return 0, false
		}
		return byte(p.transparent), true
	}
	i, ok := p.byColor[c]
	return i, ok
}

// canvasDiff describes how a target canvas differs from the canvas a viewer is
// currently looking at.
type canvasDiff struct {
	// diff is the smallest rectangle containing every differing pixel. An empty
	// rectangle means the canvas already shows the target.
	diff image.Rectangle
	// erase is the smallest rectangle containing the differing pixels the target
	// needs transparent. Only a DisposalBackground on the previous frame can clear
	// them.
	erase image.Rectangle
}

// diffCanvas compares target against canvas.
func diffCanvas(target, canvas *gifcanvas.Canvas) canvasDiff {
	b := target.Rect.Intersect(canvas.Rect)
	if b.Empty() {
		return canvasDiff{}
	}

	var diff, erase rectBuilder
	width := b.Dx() * 4
	for y := b.Min.Y; y < b.Max.Y; y++ {
		trow := target.Pix[target.PixOffset(b.Min.X, y):][:width]
		crow := canvas.Pix[canvas.PixOffset(b.Min.X, y):][:width]
		if bytes.Equal(trow, crow) {
			continue
		}
		for x := range b.Dx() {
			o := x * 4
			if trow[o] == crow[o] && trow[o+1] == crow[o+1] && trow[o+2] == crow[o+2] && trow[o+3] == crow[o+3] {
				continue
			}
			diff.add(b.Min.X+x, y)
			if trow[o+3] == 0 {
				erase.add(b.Min.X+x, y)
			}
		}
	}
	return canvasDiff{diff: diff.rect(), erase: erase.rect()}
}

// rectBuilder accumulates the bounding rectangle of a set of points.
type rectBuilder struct {
	minX, minY, maxX, maxY int
	any                    bool
}

func (r *rectBuilder) add(x, y int) {
	if !r.any {
		r.any = true
		r.minX, r.minY, r.maxX, r.maxY = x, y, x+1, y+1
		return
	}
	r.minX = min(r.minX, x)
	r.minY = min(r.minY, y)
	r.maxX = max(r.maxX, x+1)
	r.maxY = max(r.maxY, y+1)
}

func (r *rectBuilder) rect() image.Rectangle {
	if !r.any {
		return image.Rectangle{}
	}
	return image.Rect(r.minX, r.minY, r.maxX, r.maxY)
}

// buildDelta builds the frame that turns canvas into target when composited over it:
// the difference rectangle painted with the target's colors, with unchanged pixels
// made transparent where that compresses better. d must be diffCanvas(target, canvas).
// orig is the input frame at this position, used only as a source of candidate
// transparency, and may be nil. scratch is used to verify the result.
//
// It returns the frame and its estimated compressed size. A nil frame with ok true
// means there is nothing to paint. ok is false when the target can't be reached from
// canvas: a pixel would need erasing, a color is missing from the palette, or the
// frame fails verification. This check is what keeps Optimize lossless.
func buildDelta(d canvasDiff, target, canvas *gifcanvas.Canvas, orig *image.Paletted, pal *paletteIndex, cfg Config, scratch *gifcanvas.Canvas) (*image.Paletted, int, bool, error) {
	if !d.erase.Empty() {
		return nil, 0, false, nil
	}
	if d.diff.Empty() {
		return nil, 0, true, nil
	}

	// Paint the difference rectangle with the target's colors. Runs of one color are
	// common, so caching the last lookup avoids most map lookups.
	painted := image.NewPaletted(d.diff, pal.palette)
	var (
		lastColor color.RGBA
		lastIndex byte
		haveLast  bool
	)
	for y := d.diff.Min.Y; y < d.diff.Max.Y; y++ {
		row := painted.Pix[painted.PixOffset(d.diff.Min.X, y):][:d.diff.Dx()]
		for x := range row {
			c := target.RGBAAt(d.diff.Min.X+x, y)
			if haveLast && c == lastColor {
				row[x] = lastIndex
				continue
			}
			idx, ok := pal.index(c)
			if !ok {
				return nil, 0, false, nil
			}
			lastColor, lastIndex, haveLast = c, idx, true
			row[x] = idx
		}
	}

	// Pixels that already match the canvas can be left transparent. Build a candidate
	// for each way of choosing them and keep whichever compresses smallest.
	tol := cfg.LossyTransparencyTolerance
	candidates := []*image.Paletted{painted}
	if !cfg.DisableTransparencyBetweenFrames && pal.transparent >= 0 {
		tc := byte(pal.transparent)
		// With a tolerance, each candidate is built both exact and tolerant. The exact
		// ones come first so a tie keeps the exact frame, which keeps the tolerance
		// from producing a larger file than leaving it off.
		tolerances := []int{0}
		if tol > 0 {
			tolerances = append(tolerances, tol)
		}
		for _, t := range tolerances {
			if !cfg.DisableFrameCompressibilityCheck {
				// Only useful when candidate sizes are compared.
				if c := keepFrameTransparency(painted, orig, target, canvas, tc, t); c != nil {
					candidates = append(candidates, c)
				}
				candidates = append(candidates, applyTransparencyMaximal(painted, canvas, tc, t))
			}
			candidates = append(candidates, applyTransparencyRunBased(painted, canvas, tc, t))
		}
	}

	// With the size check disabled, use the run-based candidate, the last one built.
	result := candidates[len(candidates)-1]
	size := -1
	if len(candidates) > 1 && !cfg.DisableFrameCompressibilityCheck {
		for _, c := range candidates {
			sz, err := compressedImageSize(c)
			if err != nil {
				return nil, 0, false, err
			}
			if size < 0 || sz < size {
				size, result = sz, c
			}
		}
	}

	// Without a tolerance no trim is needed: every edge of d.diff holds a differing
	// pixel, and those are always painted. A tolerance can leave an edge pixel
	// transparent, so the rectangle has to be re-tightened.
	if tol > 0 {
		r := minOpaqueRect(result)
		switch {
		case r.Empty():
			// Every pixel was within tolerance. The caller emits a filler frame.
			return nil, 0, true, nil
		case r != result.Rect:
			result = result.SubImage(r).(*image.Paletted) //nolint:forcetypeassert // SubImage of a *image.Paletted is always one
			// The size measured above was for the untrimmed candidate.
			size = -1
		}
	}

	// Verify the frame reproduces the target, within the tolerance if there is one.
	scratch.CopyFrom(canvas)
	scratch.Composite(result)
	if !scratch.EqualWithin(target, tol) {
		return nil, 0, false, nil
	}

	if size < 0 {
		// The chosen candidate was not measured above.
		var err error
		if size, err = compressedImageSize(result); err != nil {
			return nil, 0, false, err
		}
	}
	return result, size, true, nil
}

// keepFrameTransparency returns a copy of painted that is transparent wherever orig
// is transparent and the canvas already matches the target. It returns nil when there
// is no such pixel.
//
// The input's transparency often comes from an encoder that already optimized the
// frame, and tends to beat the heuristics.
//
// tol is Config.LossyTransparencyTolerance.
func keepFrameTransparency(painted, orig *image.Paletted, target, canvas *gifcanvas.Canvas, tc byte, tol int) *image.Paletted {
	if orig == nil {
		return nil
	}
	r := painted.Rect.Intersect(orig.Rect)
	if r.Empty() {
		return nil
	}

	var out *image.Paletted
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			idx := int(orig.Pix[orig.PixOffset(x, y)])
			if idx >= len(orig.Palette) || !isColorTransparent(orig.Palette[idx]) {
				continue
			}
			// Only safe where the canvas already agrees with the target.
			if !gifcanvas.ColorsWithin(target.RGBAAt(x, y), canvas.RGBAAt(x, y), tol) {
				continue
			}
			if out == nil {
				out = clonePalettedImage(painted)
			}
			out.Pix[out.PixOffset(x, y)] = tc
		}
	}
	return out
}

// expandFrame grows frame's rectangle to cover need, filling the added pixels so
// they don't change what is rendered. This lets a DisposalBackground clear more than
// the frame paints (gifsicle's expand_difference_bounds). canvas must be what the
// viewer sees while frame is displayed.
//
// ok is false when the fill cannot be expressed with frame's palette.
func expandFrame(frame *image.Paletted, need image.Rectangle, canvas *gifcanvas.Canvas, pal *paletteIndex) (*image.Paletted, bool) {
	r := frame.Rect.Union(need).Intersect(canvas.Rect)
	if r == frame.Rect {
		return frame, true
	}

	out := image.NewPaletted(r, frame.Palette)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			var v byte
			switch {
			case (image.Point{X: x, Y: y}).In(frame.Rect):
				v = frame.Pix[frame.PixOffset(x, y)]
			case pal.transparent >= 0:
				// Leaves the canvas unchanged and compresses well.
				v = byte(pal.transparent)
			default:
				idx, ok := pal.index(canvas.RGBAAt(x, y))
				if !ok {
					return nil, false
				}
				v = idx
			}
			out.Pix[out.PixOffset(x, y)] = v
		}
	}
	return out, true
}

// fillerFrame returns a 1x1 frame that doesn't change what is rendered, for a frame
// that has nothing to paint but must still be emitted. For example, a disposal only
// takes effect when the next frame is displayed, so a pending clear needs a frame
// after it.
func fillerFrame(target *gifcanvas.Canvas, pal *paletteIndex) (*image.Paletted, bool) {
	b := target.Rect
	if b.Empty() {
		return nil, false
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := target.RGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			idx, ok := pal.index(c)
			if !ok {
				continue
			}
			im := image.NewPaletted(image.Rect(x, y, x+1, y+1), pal.palette)
			im.Pix[0] = idx
			return im, true
		}
	}
	// The canvas is blank, so use a transparent pixel.
	if pal.transparent < 0 {
		return nil, false
	}
	im := image.NewPaletted(image.Rect(b.Min.X, b.Min.Y, b.Min.X+1, b.Min.Y+1), pal.palette)
	im.Pix[0] = byte(pal.transparent)
	return im, true
}

// chain is the input animation passed to deltaChain.
type chain struct {
	frames []*image.Paletted
	// delays and disposal have one entry per frame. disposal must already be
	// normalized to DisposalNone, DisposalBackground or DisposalPrevious.
	delays   []int
	disposal []byte
	// transparent[i] is the transparent palette index of frames[i], or -1.
	transparent []int
	// width and height are the logical screen dimensions.
	width, height int
	cfg           Config
}

// deltaChain rewrites the animation as frames that each paint only what changed since
// the previous one, choosing a disposal method per frame.
//
// A nil images result means the animation cannot be expressed this way (see
// buildDelta) and the caller must leave the frames unchanged.
//
// It allocates at most five canvases regardless of frame count.
func deltaChain(c chain) (images []*image.Paletted, delays []int, disposal []byte, err error) {
	// target is what a viewer must see while the current input frame is displayed,
	// following the input's disposal methods.
	target := gifcanvas.New(c.width, c.height)
	// saved is the pre-frame canvas an input DisposalPrevious restores, allocated
	// only if some frame asks for that.
	var saved *gifcanvas.Canvas
	// canvas is what the frames emitted so far actually render.
	canvas := gifcanvas.New(c.width, c.height)
	// verify holds a candidate composited over canvas; trial holds canvas with the
	// previous frame cleared. Both are reused across frames.
	verify := gifcanvas.New(c.width, c.height)
	trial := gifcanvas.New(c.width, c.height)
	// pals[i] is the palette lookup for images[i], needed to expand that frame.
	var pals []*paletteIndex

	for i, frame := range c.frames {
		if c.disposal[i] == gif.DisposalPrevious {
			if saved == nil {
				saved = gifcanvas.New(c.width, c.height)
			}
			saved.CopyFrom(target)
		}
		target.Composite(frame)

		d := diffCanvas(target, canvas)

		// Nothing changed, so merge this frame's delay into the previous one. This
		// also collapses runs of identical frames. An empty diff has nothing to
		// erase, so this can be checked before handling clears.
		if d.diff.Empty() && len(images) > 0 {
			delays[len(delays)-1] += c.delays[i]
			advanceTarget(target, saved, frame, c.disposal[i])
			continue
		}

		pal := newPaletteIndex(frame.Palette, c.transparent[i])

		// Pixels the target needs transparent can only be cleared by the previous
		// frame's disposal. Grow that frame to cover them and set it to
		// DisposalBackground, then recompute the diff so anything else the clear
		// removed is repainted.
		forcedClear := false
		if !d.erase.Empty() {
			if len(images) == 0 {
				// Unreachable: the canvas starts fully transparent.
				return nil, nil, nil, nil
			}
			last := len(images) - 1
			grown, ok := expandFrame(images[last], d.erase, canvas, pals[last])
			if !ok {
				return nil, nil, nil, nil
			}
			images[last] = grown
			disposal[last] = gif.DisposalBackground
			canvas.ClearRect(grown.Rect)
			forcedClear = true
			d = diffCanvas(target, canvas)
		}

		out, size, ok, err := buildDelta(d, target, canvas, frame, pal, c.cfg, verify)
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok {
			return nil, nil, nil, nil
		}

		// Also try clearing the previous frame and keep whichever compresses
		// smaller. buildDelta verifies the alternative too, so a clear that loses
		// content is rejected.
		clearPrev := false
		if out != nil && !forcedClear && !c.cfg.DisableDisposalSelection && len(images) > 0 {
			trial.CopyFrom(canvas)
			trial.ClearRect(images[len(images)-1].Rect)
			altD := diffCanvas(target, trial)
			alt, altSize, altOK, err := buildDelta(altD, target, trial, frame, pal, c.cfg, verify)
			if err != nil {
				return nil, nil, nil, err
			}
			if altOK && alt != nil && altSize < size {
				out, clearPrev = alt, true
			}
		}
		if clearPrev {
			last := len(images) - 1
			disposal[last] = gif.DisposalBackground
			canvas.ClearRect(images[last].Rect)
		}

		if out == nil {
			// Nothing to paint, but the frame is still needed: it carries a delay or
			// a pending clear. This also happens when every pixel is within the
			// tolerance.
			if out, ok = fillerFrame(target, pal); !ok {
				return nil, nil, nil, nil
			}
		}

		canvas.Composite(out)
		images = append(images, out)
		delays = append(delays, c.delays[i])
		disposal = append(disposal, gif.DisposalNone)
		pals = append(pals, pal)

		advanceTarget(target, saved, frame, c.disposal[i])
	}

	if len(images) == 0 {
		// Unreachable: a redundant frame is only dropped when an earlier one was
		// emitted, and the first frame falls back to a filler.
		return nil, nil, nil, nil
	}
	return images, delays, disposal, nil
}

// advanceTarget applies an input frame's disposal method to target, readying it for
// the next input frame. It mirrors gifcanvas.RenderFrames.
func advanceTarget(target, saved *gifcanvas.Canvas, frame *image.Paletted, disposal byte) {
	switch disposal {
	case gif.DisposalBackground:
		target.ClearRect(frame.Rect)
	case gif.DisposalPrevious:
		target.CopyFrom(saved)
	}
}
