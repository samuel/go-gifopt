package frames

import (
	"bytes"
	"compress/lzw"
	"image"
	"image/color"

	"samuelks.com/gifopt/internal/gifcanvas"
	"samuelks.com/gifopt/internal/gifpix"
)

// pixMatchesCanvas reports whether the frame pixel at (x,y) could be left transparent
// without changing the canvas. Transparent frame pixels always match.
//
// tol is Config.LossyTransparencyTolerance, the largest per-channel difference that
// still counts as a match. Both transparency heuristics go through here, so this is
// the only place the tolerance applies.
func pixMatchesCanvas(frame *image.Paletted, canvas *gifcanvas.Canvas, x, y, tol int) bool {
	idx := frame.Pix[frame.PixOffset(x, y)]
	fc := frame.Palette[idx]
	if isColorTransparent(fc) {
		return true
	}
	fr, fg, fb, fa := fc.RGBA()
	pix := color.RGBA{R: uint8(fr >> 8), G: uint8(fg >> 8), B: uint8(fb >> 8), A: uint8(fa >> 8)}
	return gifcanvas.ColorsWithin(pix, canvas.RGBAAt(x, y), tol)
}

// applyTransparencyRunBased returns a copy of the frame with the run-based
// transparency heuristic applied: matching pixels are only made transparent when they
// form a group spanning two or more consecutive runs of different colors. Single-color
// runs are left opaque to avoid breaking surrounding LZW runs.
func applyTransparencyRunBased(frame *image.Paletted, canvas *gifcanvas.Canvas, tc byte, tol int) *image.Paletted {
	out := clonePalettedImage(frame)
	dx := frame.Rect.Dx()
	if dx == 0 {
		return out
	}
	for y := frame.Rect.Min.Y; y < frame.Rect.Max.Y; y++ {
		// nsame: 0 = outside an eligible segment, 1 = in the first eligible run and
		// not yet committed, >=2 = committed to transparent.
		nsame := 0
		beginSame := frame.Rect.Min.X // start of the current tentative run
		var lastOut byte              // last written output pixel

		for x := frame.Rect.Min.X; x < frame.Rect.Max.X; x++ {
			srcOff := frame.PixOffset(x, y)
			dstOff := out.PixOffset(x, y)
			cur := frame.Pix[srcOff]
			fc := frame.Palette[cur]
			eligible := !isColorTransparent(fc) && pixMatchesCanvas(frame, canvas, x, y, tol)

			switch {
			case !eligible:
				nsame = 0
				out.Pix[dstOff] = cur
				lastOut = cur
			case nsame == 0:
				nsame = 1
				beginSame = x
				out.Pix[dstOff] = cur
				lastOut = cur
			case nsame == 1:
				if !colorEqual(frame.Palette[cur], frame.Palette[lastOut]) {
					// Second eligible run of a different color: commit, and make the
					// first run transparent retroactively.
					for rx := beginSame; rx < x; rx++ {
						out.Pix[out.PixOffset(rx, y)] = tc
					}
					nsame = 2
					out.Pix[dstOff] = tc
					lastOut = tc
				} else {
					// Same color, so extend the tentative run.
					out.Pix[dstOff] = cur
					lastOut = cur
				}
			default: // nsame >= 2, committed
				out.Pix[dstOff] = tc
				lastOut = tc
			}
		}
	}
	return out
}

// applyTransparencyMaximal returns a copy of the frame with every pixel that matches
// the canvas made transparent, including the single-color runs
// applyTransparencyRunBased leaves opaque.
//
// This is gifsicle's t2_data variant. Which of the two wins depends on the image, so
// both are built and the smaller one kept.
func applyTransparencyMaximal(frame *image.Paletted, canvas *gifcanvas.Canvas, tc byte, tol int) *image.Paletted {
	out := clonePalettedImage(frame)
	for y := frame.Rect.Min.Y; y < frame.Rect.Max.Y; y++ {
		for x := frame.Rect.Min.X; x < frame.Rect.Max.X; x++ {
			cur := frame.Pix[frame.PixOffset(x, y)]
			if isColorTransparent(frame.Palette[cur]) {
				continue
			}
			if pixMatchesCanvas(frame, canvas, x, y, tol) {
				out.Pix[out.PixOffset(x, y)] = tc
			}
		}
	}
	return out
}

// isColorTransparent reports whether the color's alpha is zero.
func isColorTransparent(c color.Color) bool {
	_, _, _, a := c.RGBA()
	return a == 0
}

// colorEqual reports whether the colors are exactly equal.
func colorEqual(c1, c2 color.Color) bool {
	if c1 == c2 {
		return true
	}
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, a2 := c2.RGBA()
	return [4]uint32{r1, g1, b1, a1} == [4]uint32{r2, g2, b2, a2}
}

// findTransparentColor returns the index of the first transparent entry, or -1.
func findTransparentColor(pal []color.Color) int {
	for i, c := range pal {
		_, _, _, a := c.RGBA()
		if a == 0 {
			return i
		}
	}
	return -1
}

// minOpaqueRect returns the smallest rectangle containing every opaque pixel.
func minOpaqueRect(im *image.Paletted) image.Rectangle {
	minY := 0
minYLoop:
	for ; minY < im.Rect.Dy(); minY++ {
		for x := range im.Bounds().Dx() {
			if !isColorTransparent(im.Palette[im.Pix[minY*im.Stride+x]]) {
				break minYLoop
			}
		}
	}
	if minY == im.Rect.Dy() {
		// No opaque pixels.
		return image.Rect(0, 0, 0, 0)
	}
	maxY := im.Rect.Dy()
maxYLoop:
	for ; maxY > minY; maxY-- {
		for x := range im.Bounds().Dx() {
			if !isColorTransparent(im.Palette[im.Pix[(maxY-1)*im.Stride+x]]) {
				break maxYLoop
			}
		}
	}
	minX := 0
minXLoop:
	for ; minX < im.Rect.Dx(); minX++ {
		for y := minY; y < maxY; y++ {
			if !isColorTransparent(im.Palette[im.Pix[y*im.Stride+minX]]) {
				break minXLoop
			}
		}
	}
	maxX := im.Rect.Dx()
maxXLoop:
	for ; maxX > minX; maxX-- {
		for y := minY; y < maxY; y++ {
			if !isColorTransparent(im.Palette[im.Pix[y*im.Stride+(maxX-1)]]) {
				break maxXLoop
			}
		}
	}
	return image.Rect(minX+im.Rect.Min.X, minY+im.Rect.Min.Y, maxX+im.Rect.Min.X, maxY+im.Rect.Min.Y)
}

// dedupeImagePalette replaces duplicate palette entries with removedColor and points
// their pixels at the first occurrence.
//
// The palette is replaced rather than modified in place: gif.DecodeAll gives every
// frame the same global color table slice, so editing it would change other frames.
func dedupeImagePalette(im *image.Paletted, removedColor color.Color) {
	var remap [256]byte
	for i := range remap {
		remap[i] = byte(i)
	}
	var duplicates bool
	for i, c1 := range im.Palette[:len(im.Palette)-1] {
		if remap[i] != byte(i) {
			continue
		}
		for j, c2 := range im.Palette[i+1:] {
			if c1 == c2 {
				remap[byte(j+i+1)] = byte(i)
				duplicates = true
			}
		}
	}
	if !duplicates {
		return
	}

	pal := append(color.Palette(nil), im.Palette...)
	for i := range pal {
		if remap[i] != byte(i) {
			pal[i] = removedColor
		}
	}
	im.Palette = pal
	for k, v := range im.Pix {
		im.Pix[k] = remap[v]
	}
}

// paletteEqual reports whether the two palettes are exactly the same. Entries must be
// the same color type, so RGBA does not match NRGBA.
func paletteEqual(p1, p2 []color.Color) bool {
	if len(p1) != len(p2) {
		return false
	}
	for i, c := range p1 {
		if c != p2[i] {
			return false
		}
	}
	return true
}

// palettedImageEqual reports whether the two paletted images have equal bounds and
// pixel colors. The palettes may differ.
//
// Rows are compared individually because a sub-image's stride can be wider than its
// bounds.
func palettedImageEqual(im1, im2 *image.Paletted) bool {
	if im1.Rect != im2.Rect {
		return false
	}
	samePalette := paletteEqual(im1.Palette, im2.Palette)
	dx := im1.Rect.Dx()
	for y := im1.Rect.Min.Y; y < im1.Rect.Max.Y; y++ {
		row1 := im1.Pix[im1.PixOffset(im1.Rect.Min.X, y):][:dx]
		row2 := im2.Pix[im2.PixOffset(im2.Rect.Min.X, y):][:dx]
		if samePalette {
			// Equal palettes mean equal indices are equal colors.
			if !bytes.Equal(row1, row2) {
				return false
			}
			continue
		}
		for x, v1 := range row1 {
			v2 := row2[x]
			if int(v1) >= len(im1.Palette) || int(v2) >= len(im2.Palette) {
				// Out-of-range index: report a difference rather than panic.
				return false
			}
			if !colorEqual(im1.Palette[v1], im2.Palette[v2]) {
				return false
			}
		}
	}
	return true
}

// compressedImageSize returns the size of the image's pixels compressed with LZW.
//
// The literal width and pixel data come from internal/gifpix, matching gifenc. It is
// still an estimate (compress/lzw is plain greedy, and sub-block length bytes aren't
// counted), but it is consistent across candidates.
func compressedImageSize(im *image.Paletted) (int, error) {
	cw := &countWriter{}
	w := lzw.NewWriter(cw, lzw.LSB, gifpix.LitWidth(im))
	if _, err := w.Write(gifpix.Pix(im)); err != nil {
		_ = w.Close()
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return cw.count, nil
}

// clonePalettedImage returns a copy of im with the same bounds.
func clonePalettedImage(im *image.Paletted) *image.Paletted {
	out := image.NewPaletted(im.Rect, im.Palette)
	if out.Stride == im.Stride {
		copy(out.Pix, im.Pix)
	} else {
		for y := range im.Rect.Dy() {
			copy(out.Pix[y*out.Stride:y*out.Stride+out.Rect.Dx()], im.Pix[y*im.Stride:])
		}
	}
	return out
}

// paddedPaletteLen returns how many entries a palette of n colors occupies in a GIF
// color table: the next power of two, and never fewer than 2.
func paddedPaletteLen(n int) int {
	size := 2
	for size < n {
		size <<= 1
	}
	return size
}

// matchImagePalettes tries to build one global palette for every image. Colors are
// numbered in first-use order; numbering them by how many frames use each one was
// measured and came out no better.
//
// TODO: an optional threshold to merge similar colors, though requantizing the
// palette is probably the better way to go further.
func matchImagePalettes(ims []*image.Paletted, unusedColor color.Color) {
	colorMap := make(map[color.Color]int)
	var colors []color.Color
	// remap[i][j] is the global palette index for frame i's palette entry j.
	remap := make([][256]int, len(ims))
	for i, im := range ims {
		var counts [256]int
		dx := im.Rect.Dx()
		for y := im.Rect.Min.Y; y < im.Rect.Max.Y; y++ {
			off := im.PixOffset(im.Rect.Min.X, y)
			for _, b := range im.Pix[off : off+dx] {
				counts[b]++
			}
		}
		for j, c := range im.Palette {
			if j >= 256 || counts[j] == 0 {
				continue
			}
			k, ok := colorMap[c]
			if !ok {
				k = len(colors)
				colorMap[c] = k
				colors = append(colors, c)
			}
			remap[i][j] = k
		}
	}
	if len(colors) > 256 {
		// Unifying would mean dropping colors, so the frames keep their own palettes.
		return
	}

	// Pad to the next power of two, not to 256: a GIF color table is written at the
	// padded size, so 12 colors take 16 entries (48 bytes) rather than 256 (768).
	// Padding entries can also hold the transparent index.
	for n := paddedPaletteLen(len(colors)); len(colors) < n; {
		colors = append(colors, unusedColor)
	}

	// Every frame shares one slice to save memory. Encoders compare palette bytes,
	// not slices, to decide whether a frame needs a local color table. Callers that
	// modify one frame's palette must replace the slice, not write into it.
	for i, im := range ims {
		im.Palette = colors
		dx := im.Rect.Dx()
		for y := im.Rect.Min.Y; y < im.Rect.Max.Y; y++ {
			off := im.PixOffset(im.Rect.Min.X, y)
			row := im.Pix[off : off+dx]
			for j, b := range row {
				row[j] = byte(remap[i][b])
			}
		}
	}
}
