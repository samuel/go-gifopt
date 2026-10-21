// Package frames rebuilds the frames of an animated GIF so each stores only what
// changed, producing a smaller file that renders identically to the input.
package frames

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"

	"samuelks.com/gifopt/internal/gifcanvas"
)

// Config controls Optimize. The zero value is lossless.
type Config struct {
	// DisableFrameCompressibilityCheck skips compressing each candidate frame to
	// pick the smallest. Faster, but gives up savings.
	DisableFrameCompressibilityCheck bool
	// DisableTransparencyBetweenFrames stops pixels unchanged from the previous
	// frame being made transparent.
	DisableTransparencyBetweenFrames bool
	// DisableDisposalSelection stops trying DisposalBackground per frame and
	// keeping whichever compresses smaller. A disposal the animation requires,
	// because the input clears part of the canvas, is still used.
	DisableDisposalSelection bool
	// LossyForceTransparentColor permits changing pixel colors to free a palette
	// slot for transparency when all 256 entries are in use: the least used color
	// is dropped and its pixels remapped to the nearest remaining one. Off by
	// default.
	LossyForceTransparentColor bool
	// LossyTransparencyTolerance permits leaving a pixel unpainted when the canvas
	// already shows a color within this many steps per channel of the wanted one,
	// so a displayed pixel can be off by that much. Off (0) by default. Frame
	// count, timing and palettes are unaffected, and the error does not accumulate
	// because each comparison is against what the output renders.
	//
	// It helps animations made from video and does nothing for flat, cartoon-like
	// ones, whose palettes are too coarse for a small tolerance to matter. Over the
	// corpus, 4 saves about 1% and is hard to see; 8 saves about 16% and is visible
	// side by side. A tolerance slows optimization, because every candidate is built
	// both exactly and tolerantly.
	LossyTransparencyTolerance int
}

// MaxScaleMultiplier is the largest multiplier Scale accepts.
const MaxScaleMultiplier = 8

// Scale enlarges every frame of the GIF by an integer multiplier, turning each pixel
// into a multiplier x multiplier square. The logical screen is scaled too. Delays,
// disposal methods and palettes are unchanged.
//
// A multiplier of 1 or less does nothing; larger than MaxScaleMultiplier is clamped
// to it.
func Scale(g *gif.GIF, multiplier int) {
	if g == nil || multiplier <= 1 {
		return
	}
	multiplier = min(multiplier, MaxScaleMultiplier)

	maxX, maxY := g.Config.Width*multiplier, g.Config.Height*multiplier
	for i, im := range g.Image {
		r := image.Rect(
			im.Rect.Min.X*multiplier,
			im.Rect.Min.Y*multiplier,
			im.Rect.Max.X*multiplier,
			im.Rect.Max.Y*multiplier,
		)
		out := image.NewPaletted(r, im.Palette)
		width := r.Dx()
		for y := im.Rect.Min.Y; y < im.Rect.Max.Y; y++ {
			srcOff := im.PixOffset(im.Rect.Min.X, y)
			src := im.Pix[srcOff : srcOff+im.Rect.Dx()]

			// Widen one row, then copy it to the rest of its block of rows.
			dstOff := out.PixOffset(r.Min.X, y*multiplier)
			row := out.Pix[dstOff : dstOff+width]
			for x, v := range src {
				for k := range multiplier {
					row[x*multiplier+k] = v
				}
			}
			for k := 1; k < multiplier; k++ {
				off := out.PixOffset(r.Min.X, y*multiplier+k)
				copy(out.Pix[off:off+width], row)
			}
		}
		g.Image[i] = out
		maxX = max(maxX, r.Max.X)
		maxY = max(maxY, r.Max.Y)
	}
	g.Config.Width, g.Config.Height = maxX, maxY
}

// Optimize rewrites an animated GIF in place to be smaller without changing what it
// renders. Duplicate palette entries are merged, a single global palette is used
// where the colors fit, frames that render identically are collapsed into one with
// their delays summed, and what is left is rewritten as a chain of deltas: each
// output frame paints only the pixels that differ from what the frames before it
// left on the canvas.
//
// Every candidate frame is verified against a rendering of the input before it is
// accepted. If a difference cannot be expressed, the frames are left as they are
// rather than approximated. The exceptions are the two Config fields whose names
// begin with Lossy, both off by default.
//
// Candidate sizes are compared with compress/lzw, so an encoder that compresses
// worse than that may still produce a larger file. The output uses only
// DisposalNone and DisposalBackground.
func Optimize(g *gif.GIF, cfg Config) error {
	if g == nil || len(g.Image) == 0 {
		return errors.New("frames: no images")
	}
	if len(g.Delay) != len(g.Image) {
		return fmt.Errorf("frames: delay count %d does not match image count %d", len(g.Delay), len(g.Image))
	}
	// TODO: account for negative min X and Y just in case
	for i, im := range g.Image {
		if len(im.Palette) == 0 {
			return fmt.Errorf("frames: image %d has no palette", i)
		}
	}
	cfg.LossyTransparencyTolerance = min(max(cfg.LossyTransparencyTolerance, 0), 255)

	// Match the transparent entry's color type to the rest of the palette so direct
	// comparisons work.
	var colorTransparent color.Color
	switch g.Image[0].Palette[0].(type) {
	case color.RGBA:
		colorTransparent = color.RGBA{A: 0}
	case color.NRGBA:
		colorTransparent = color.NRGBA{A: 0}
	default:
		colorTransparent = color.Transparent
	}

	// The background color has to be resolved before the palettes are rewritten.
	background := backgroundColor(g)

	for _, im := range g.Image {
		dedupeImagePalette(im, colorTransparent)
	}
	matchImagePalettes(g.Image, colorTransparent)

	// TODO: maybe move all unused colors to the end of the palette

	// Never shrink the logical screen: any margin around the frames is part of what
	// the animation shows.
	w, h := gifcanvas.Size(g)
	g.Config.Width, g.Config.Height = w, h
	setGlobalPalette(g)
	g.BackgroundIndex = paletteIndexOf(g, background)

	// The disposal slice must match the frame count on every return path below, or
	// encoders reject the result.
	disposal := make([]byte, len(g.Image))
	for i := range disposal {
		disposal[i] = gifcanvas.Disposal(g, i)
	}
	g.Disposal = disposal

	if len(g.Image) == 1 {
		return nil
	}

	// Must run before rendering, since LossyForceTransparentColor rewrites pixel
	// colors.
	transparent := chooseTransparentColors(g, cfg, colorTransparent)

	images, outDelays, outDisposal, err := deltaChain(chain{
		frames:      g.Image,
		delays:      g.Delay,
		disposal:    disposal,
		transparent: transparent,
		width:       w,
		height:      h,
		cfg:         cfg,
	})
	if err != nil {
		return err
	}
	if images == nil {
		// Not expressible as deltas. Keep the palette changes and leave the frames
		// as they are.
		return nil
	}
	g.Image, g.Delay, g.Disposal = images, outDelays, outDisposal
	return nil
}

// backgroundColor returns the color g.BackgroundIndex refers to, or nil when there
// is no global palette to resolve it against.
func backgroundColor(g *gif.GIF) color.Color {
	pal, ok := g.Config.ColorModel.(color.Palette)
	if !ok || int(g.BackgroundIndex) >= len(pal) {
		return nil
	}
	return pal[g.BackgroundIndex]
}

// paletteIndexOf returns the index of c in the global palette, so g.BackgroundIndex
// keeps pointing at the same color after the palettes are rewritten. It returns 0
// when c is no longer present or there is no global palette.
func paletteIndexOf(g *gif.GIF, c color.Color) byte {
	pal, ok := g.Config.ColorModel.(color.Palette)
	if !ok || c == nil {
		return 0
	}
	for i, pc := range pal {
		if i > 255 {
			break
		}
		if pc != nil && colorEqual(pc, c) {
			return byte(i)
		}
	}
	return 0
}

// setGlobalPalette points g.Config.ColorModel at the shared palette when every
// frame uses the same one, and clears it otherwise.
func setGlobalPalette(g *gif.GIF) {
	for _, im := range g.Image[1:] {
		if !paletteEqual(g.Image[0].Palette, im.Palette) {
			g.Config.ColorModel = nil
			return
		}
	}
	g.Config.ColorModel = g.Image[0].Palette
}

// chooseTransparentColors picks the palette index each frame uses for transparency,
// leaving -1 for a frame that cannot have one. An uncovered canvas pixel can only be
// expressed with a transparent index, so one is chosen even when
// DisableTransparencyBetweenFrames is set.
//
// Palettes are modified in place: an existing transparent entry is reused, an unused
// entry is repurposed, or an entry is appended. With LossyForceTransparentColor pixel
// colors may be rewritten too, so render the animation only after calling this.
func chooseTransparentColors(g *gif.GIF, cfg Config, colorTransparent color.Color) []int {
	transparent := make([]int, len(g.Image))
	for i := range transparent {
		transparent[i] = -1
	}

	if g.Config.ColorModel != nil {
		// With a shared palette, every frame uses the same index.
		tc := findTransparentColor(g.Image[0].Palette)
		if tc < 0 {
			if tc = firstUnusedPaletteEntry(g.Image); tc >= 0 {
				pal := paletteWithTransparentEntry(g.Image[0].Palette, tc, colorTransparent)
				for _, im := range g.Image {
					im.Palette = pal
				}
				g.Config.ColorModel = pal
			}
		}
		if tc >= 0 {
			for i := range transparent {
				transparent[i] = tc
			}
			return transparent
		}
	} else {
		for i, im := range g.Image {
			transparent[i] = findTransparentColor(im.Palette)
		}
	}

	// Any frame still without an index needs its own modified palette, so there is
	// no longer a global one.
	for i, im := range g.Image {
		if transparent[i] >= 0 {
			continue
		}
		palLen := min(len(im.Palette), 256)
		used := paletteUseCounts(im)
		tc := -1
		for j, count := range used[:palLen] {
			if count == 0 {
				tc = j
				break
			}
		}
		if tc < 0 && palLen < 256 {
			tc = palLen
		}
		if tc < 0 && cfg.LossyForceTransparentColor && !cfg.DisableTransparencyBetweenFrames {
			tc = replaceLeastUsedColor(im, used[:palLen])
		}
		if tc < 0 {
			continue
		}
		g.Config.ColorModel = nil
		// The palette may be shared with other frames, so copy it first.
		pal := append(color.Palette(nil), im.Palette...)
		im.Palette = paletteWithTransparentEntry(pal, tc, colorTransparent)
		transparent[i] = tc
	}
	return transparent
}

// paletteWithTransparentEntry makes palette entry tc transparent, appending it when
// tc is one past the end. Existing entries are changed in place, so copy pal first if
// it is shared and the change is for one frame only.
func paletteWithTransparentEntry(pal color.Palette, tc int, colorTransparent color.Color) color.Palette {
	if tc >= len(pal) {
		return append(pal, colorTransparent)
	}
	pal[tc] = colorTransparent
	return pal
}

// paletteUseCounts returns how many pixels of im use each palette index.
func paletteUseCounts(im *image.Paletted) [256]int {
	var used [256]int
	dx := im.Rect.Dx()
	for y := im.Rect.Min.Y; y < im.Rect.Max.Y; y++ {
		off := im.PixOffset(im.Rect.Min.X, y)
		for _, b := range im.Pix[off : off+dx] {
			used[b]++
		}
	}
	return used
}

// firstUnusedPaletteEntry returns the lowest palette index no frame uses, which may
// be one past the end of the palette, or -1 when all 256 entries are in use.
func firstUnusedPaletteEntry(ims []*image.Paletted) int {
	var used [256]int
	for _, im := range ims {
		counts := paletteUseCounts(im)
		for i, c := range counts {
			used[i] += c
		}
	}
	for i, c := range used {
		if c == 0 {
			return i
		}
	}
	return -1
}

// replaceLeastUsedColor frees a palette slot by remapping the pixels of the least
// used color to the nearest remaining color, and returns the freed index. This is
// lossy and only used with LossyForceTransparentColor.
func replaceLeastUsedColor(im *image.Paletted, used []int) int {
	leastUsed := smallestIntValueIndex(used)
	if leastUsed < 0 {
		return -1
	}
	nearest := nearestColorIndex(im.Palette, leastUsed)
	if nearest < 0 {
		return -1
	}
	for i, v := range im.Pix {
		if v == byte(leastUsed) {
			im.Pix[i] = byte(nearest)
		}
	}
	return leastUsed
}

// nearestColorIndex returns the palette entry closest to entry skip, or -1 when the
// palette holds nothing else.
func nearestColorIndex(pal color.Palette, skip int) int {
	r0, g0, b0, a0 := pal[skip].RGBA()
	nearest := -1
	closest := 4 * 256 * 256 // larger than any valid distance
	for i, c := range pal {
		if i == skip || c == nil {
			continue
		}
		r, g, b, a := c.RGBA()
		dr, dg, db, da := int(r>>8)-int(r0>>8), int(g>>8)-int(g0>>8), int(b>>8)-int(b0>>8), int(a>>8)-int(a0>>8)
		if d := dr*dr + dg*dg + db*db + da*da; d < closest {
			nearest = i
			closest = d
		}
	}
	return nearest
}
