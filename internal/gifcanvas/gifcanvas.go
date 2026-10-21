// Package gifcanvas composites GIF frames onto an RGBA canvas.
//
// All rendering in this module goes through it. Both the optimizer (frames) and the
// visual diff tool (gifdiff) use it, so the optimizer can never disagree with the tool
// that verifies it.
//
// # Rendering semantics
//
// The specification is vague about disposal and viewers differ. These are the choices
// made here, matching what browsers do:
//
//   - The canvas starts fully transparent, and GIF.BackgroundIndex is ignored:
//     browsers render the background as transparent, and honoring the index would
//     make otherwise identical animations compare unequal.
//   - Compositing a frame leaves the canvas untouched wherever the frame's pixel maps
//     to a palette entry with zero alpha, and overwrites it everywhere else. GIF
//     transparency is binary, so there is no alpha blending.
//   - DisposalBackground clears the frame's rectangle to transparent.
//   - DisposalPrevious restores the canvas to its state before the frame.
//   - DisposalNone and the unspecified value (0) leave the canvas as it is.
package gifcanvas

import (
	"image"
	"image/color"
	"image/gif"
)

// Canvas is an RGBA image that GIF frames are composited onto. The embedded
// *image.RGBA may be read directly; mutate it through the methods below so this
// package's semantics are applied consistently.
type Canvas struct {
	*image.RGBA
}

// New returns a fully transparent canvas of the given size.
func New(w, h int) *Canvas {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Canvas{RGBA: image.NewRGBA(image.Rect(0, 0, w, h))}
}

// Size returns the logical screen dimensions of g, grown to cover every frame so a GIF
// with a missing or zeroed header still renders correctly.
func Size(g *gif.GIF) (w, h int) {
	w, h = g.Config.Width, g.Config.Height
	for _, im := range g.Image {
		if im.Rect.Max.X > w {
			w = im.Rect.Max.X
		}
		if im.Rect.Max.Y > h {
			h = im.Rect.Max.Y
		}
	}
	return w, h
}

// Clone returns a deep copy of c.
func (c *Canvas) Clone() *Canvas {
	dst := image.NewRGBA(c.Rect)
	copy(dst.Pix, c.Pix)
	return &Canvas{RGBA: dst}
}

// CopyFrom overwrites c with the contents of src, which must have equal bounds. Unlike
// Clone, it does not allocate.
func (c *Canvas) CopyFrom(src *Canvas) {
	if c.Rect != src.Rect {
		panic("gifcanvas: CopyFrom with mismatched bounds")
	}
	copy(c.Pix, src.Pix)
}

// Composite draws frame onto c. Pixels whose palette entry is fully transparent
// leave the canvas unchanged; every other pixel overwrites it.
func (c *Canvas) Composite(frame *image.Paletted) {
	r := frame.Rect.Intersect(c.Rect)
	if r.Empty() {
		return
	}

	// Resolve the palette once per frame rather than per pixel.
	var (
		rgba        [256]color.RGBA
		transparent [256]bool
	)
	for i, col := range frame.Palette {
		if i >= len(rgba) {
			break
		}
		cr, cg, cb, ca := col.RGBA()
		if ca == 0 {
			transparent[i] = true
			continue
		}
		rgba[i] = color.RGBA{R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8), A: uint8(ca >> 8)}
	}

	for y := r.Min.Y; y < r.Max.Y; y++ {
		src := frame.Pix[frame.PixOffset(r.Min.X, y):]
		dst := c.Pix[c.PixOffset(r.Min.X, y):]
		for x := range r.Dx() {
			idx := src[x]
			if transparent[idx] {
				continue
			}
			col := rgba[idx]
			p := x * 4
			dst[p], dst[p+1], dst[p+2], dst[p+3] = col.R, col.G, col.B, col.A
		}
	}
}

// ClearRect sets every pixel in r to transparent.
func (c *Canvas) ClearRect(r image.Rectangle) {
	r = r.Intersect(c.Rect)
	if r.Empty() {
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		off := c.PixOffset(r.Min.X, y)
		row := c.Pix[off : off+r.Dx()*4]
		for i := range row {
			row[i] = 0
		}
	}
}

// Equal reports whether c and other have identical bounds and pixels.
func (c *Canvas) Equal(other *Canvas) bool {
	if c.Rect != other.Rect {
		return false
	}
	// Row by row: Pix may hold padding beyond the last row of a sub-image.
	w := c.Rect.Dx() * 4
	for y := c.Rect.Min.Y; y < c.Rect.Max.Y; y++ {
		a := c.Pix[c.PixOffset(c.Rect.Min.X, y):][:w]
		b := other.Pix[other.PixOffset(other.Rect.Min.X, y):][:w]
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
	}
	return true
}

// ColorsWithin reports whether two rendered colors are close enough to treat as the
// same: every color channel within tol, and the same alpha. A tol of 0 or less is
// exact equality.
//
// Alpha is never tolerated. GIF transparency is binary, so showing nothing where
// something belongs is not a close match at any tolerance.
func ColorsWithin(a, b color.RGBA, tol int) bool {
	if tol <= 0 {
		return a == b
	}
	return a.A == b.A && within(a.R, b.R, tol) && within(a.G, b.G, tol) && within(a.B, b.B, tol)
}

func within(a, b uint8, tol int) bool {
	d := int(a) - int(b)
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// EqualWithin reports whether c and other have identical bounds and every pixel pair
// satisfies ColorsWithin at tol. It is Equal when tol is 0 or less.
func (c *Canvas) EqualWithin(other *Canvas, tol int) bool {
	if tol <= 0 {
		return c.Equal(other)
	}
	if c.Rect != other.Rect {
		return false
	}
	w := c.Rect.Dx() * 4
	for y := c.Rect.Min.Y; y < c.Rect.Max.Y; y++ {
		a := c.Pix[c.PixOffset(c.Rect.Min.X, y):][:w]
		b := other.Pix[other.PixOffset(other.Rect.Min.X, y):][:w]
		for i := 0; i < w; i += 4 {
			if a[i+3] != b[i+3] {
				return false // alpha, never tolerated
			}
			if !within(a[i], b[i], tol) || !within(a[i+1], b[i+1], tol) || !within(a[i+2], b[i+2], tol) {
				return false
			}
		}
	}
	return true
}

// MaxChannelDiff returns the largest per-channel difference between any pixel of c and
// the matching pixel of other. Canvases of different sizes never compare close.
func (c *Canvas) MaxChannelDiff(other *Canvas) int {
	common := c.Rect.Intersect(other.Rect)
	if area(c.Rect) != area(common) || area(other.Rect) != area(common) {
		return 255
	}
	worst := 0
	w := common.Dx() * 4
	for y := common.Min.Y; y < common.Max.Y; y++ {
		a := c.Pix[c.PixOffset(common.Min.X, y):][:w]
		b := other.Pix[other.PixOffset(common.Min.X, y):][:w]
		for i := range a {
			d := int(a[i]) - int(b[i])
			if d < 0 {
				d = -d
			}
			if d > worst {
				worst = d
				if worst == 255 {
					return worst
				}
			}
		}
	}
	return worst
}

// CountDiff returns the number of pixels that differ between c and other. Pixels
// covered by one canvas but not the other count as differing.
func (c *Canvas) CountDiff(other *Canvas) int {
	common := c.Rect.Intersect(other.Rect)
	count := 0
	if !common.Empty() {
		w := common.Dx() * 4
		for y := common.Min.Y; y < common.Max.Y; y++ {
			a := c.Pix[c.PixOffset(common.Min.X, y):][:w]
			b := other.Pix[other.PixOffset(common.Min.X, y):][:w]
			for x := range common.Dx() {
				p := x * 4
				if a[p] != b[p] || a[p+1] != b[p+1] || a[p+2] != b[p+2] || a[p+3] != b[p+3] {
					count++
				}
			}
		}
	}
	count += area(c.Rect) - area(common)
	count += area(other.Rect) - area(common)
	return count
}

// Disposal returns the disposal method that applies to frame i of g, treating a
// missing Disposal slice and the unspecified value as DisposalNone.
func Disposal(g *gif.GIF, i int) byte {
	if i >= len(g.Disposal) {
		return gif.DisposalNone
	}
	switch d := g.Disposal[i]; d {
	case gif.DisposalBackground, gif.DisposalPrevious:
		return d
	default:
		return gif.DisposalNone
	}
}

// RenderFrames composites every frame of g in order, honoring disposal methods and
// transparency, and returns the canvas as a viewer would display it for each frame.
// The returned canvases are independent copies.
//
// This is the reference rendering that gifdiff compares and the optimizer must
// reproduce.
func RenderFrames(g *gif.GIF) []*Canvas {
	w, h := Size(g)
	canvas := New(w, h)
	rendered := make([]*Canvas, len(g.Image))

	// saved holds the pre-frame canvas for DisposalPrevious, populated only when a
	// frame asks for it.
	var saved *Canvas

	for i, frame := range g.Image {
		if Disposal(g, i) == gif.DisposalPrevious {
			if saved == nil {
				saved = New(w, h)
			}
			saved.CopyFrom(canvas)
		}

		canvas.Composite(frame)
		rendered[i] = canvas.Clone()

		switch Disposal(g, i) {
		case gif.DisposalBackground:
			canvas.ClearRect(frame.Rect)
		case gif.DisposalPrevious:
			canvas.CopyFrom(saved)
		}
	}

	return rendered
}

func area(r image.Rectangle) int {
	if r.Empty() {
		return 0
	}
	return r.Dx() * r.Dy()
}
