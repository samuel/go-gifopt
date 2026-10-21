// Package gifpix holds the pixel-level rules the encoder and the optimizer have to
// agree on: how many bits a frame's pixels are coded at, and its pixel data.
//
// The optimizer chooses between candidate frames by compressing them, so it must code
// pixels the way the encoder will. A frame coded at 4 bits per literal compresses very
// differently from the same frame at 8.
package gifpix

import (
	"image"
	"math/bits"
)

// LitWidth returns the LZW literal width a frame is coded at: enough bits for the
// largest pixel index the frame actually uses, never more than its palette needs, and
// at least the 2 the format requires.
//
// Using the largest index rather than the palette size means a frame that uses only
// the first few entries of a 256-color palette is coded at 3 or 4 bits, not 8.
func LitWidth(pm *image.Paletted) int {
	// The palette size is the upper bound.
	width := bitsFor(len(pm.Palette))
	if pm.Bounds().Empty() {
		return max(width, 2)
	}
	if used := bitsFor(maxIndex(pm) + 1); used < width {
		width = used
	}
	return max(width, 2)
}

// Pix returns the frame's pixel indices as one contiguous slice, without the padding a
// sub-image leaves at the end of each row. The frame's own slice is returned when it is
// already contiguous.
func Pix(pm *image.Paletted) []byte {
	b := pm.Bounds()
	dx, dy := b.Dx(), b.Dy()
	if dx == 0 || dy == 0 {
		return nil
	}
	if pm.Stride == dx {
		off := pm.PixOffset(b.Min.X, b.Min.Y)
		return pm.Pix[off : off+dx*dy]
	}
	out := make([]byte, 0, dx*dy)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		off := pm.PixOffset(b.Min.X, y)
		out = append(out, pm.Pix[off:off+dx]...)
	}
	return out
}

// maxIndex returns the largest palette index used by the frame.
func maxIndex(pm *image.Paletted) int {
	b := pm.Bounds()
	dx := b.Dx()
	maxIdx := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		off := pm.PixOffset(b.Min.X, y)
		for _, v := range pm.Pix[off : off+dx] {
			if int(v) > maxIdx {
				maxIdx = int(v)
			}
		}
	}
	return maxIdx
}

// bitsFor returns how many bits are needed to hold the values 0..n-1.
func bitsFor(n int) int {
	if n < 2 {
		return 0
	}
	return bits.Len(uint(n - 1))
}
