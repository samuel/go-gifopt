package lzwopt

import (
	"image/gif"
	"os"
	"testing"
)

// syntheticFrame returns a pixel buffer representative of a GIF frame:
// a 320×240 pseudo-random sequence over a 256-color palette.
func syntheticFrame() ([]byte, int) {
	const nColors = 256
	n := 320 * 240
	data := make([]byte, n)
	for i := range data {
		data[i] = byte((i*7 + i/13 + i*i/97) % nColors)
	}
	minCodeSize := 2
	for (1 << minCodeSize) < nColors {
		minCodeSize++
	}
	if minCodeSize > 8 {
		minCodeSize = 8
	}
	return data, minCodeSize
}

// BenchmarkBitStreamAppendCode measures the cost of packing LZW codes into a BitStream.
func BenchmarkBitStreamAppendCode(b *testing.B) {
	// Simulate appending ~10 000 12-bit codes (upper range of GIF LZW code sizes).
	const nCodes = 10_000
	const codeSize = 12
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		bs := &BitStream{}
		for j := range nCodes {
			bs.AppendCode(uint(j&0xFFF), codeSize)
		}
		_ = bs
	}
}

// BenchmarkBitStreamToGIFSubBlocks measures sub-block serialization of a large stream.
func BenchmarkBitStreamToGIFSubBlocks(b *testing.B) {
	// Build a stream with 10 000 12-bit codes once, then benchmark packing only.
	const nCodes = 10_000
	const codeSize = 12
	bs := &BitStream{}
	for j := range nCodes {
		bs.AppendCode(uint(j&0xFFF), codeSize)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = bs.ToGIFSubBlocks()
	}
}

// BenchmarkInitDictionary measures the cost of resetting the LZW dictionary.
func BenchmarkInitDictionary(b *testing.B) {
	data, minCodeSize := syntheticFrame()
	enc := newLZWEncoder(data, minCodeSize, 4095)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		enc.initDictionary()
	}
}

// BenchmarkEncodeBlock measures encodeBlock, the innermost hot loop, in isolation
// with DP split tracking enabled.
func BenchmarkEncodeBlock(b *testing.B) {
	data, minCodeSize := syntheticFrame()
	opts := DefaultOptions()
	opts.NonGreedy = true

	enc := newLZWEncoder(data, minCodeSize, 4095)
	var scan scanResult

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		enc.encodeBlock(0, 0, nil, true, opts, &scan, 1, 0)
	}
}

// BenchmarkEncode measures end-to-end LZW encoding of a synthetic 320×240 frame
// with the block search at its finest, which is the worst case for the DP sweep.
func BenchmarkEncode(b *testing.B) {
	data, minCodeSize := syntheticFrame()
	opts := DefaultOptions()
	opts.Alignment = 1
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := Encode(data, minCodeSize, opts)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeAutoAlignment measures the same frame with the default settings,
// where the block search granularity scales itself to the frame size.
func BenchmarkEncodeAutoAlignment(b *testing.B) {
	data, minCodeSize := syntheticFrame()
	opts := DefaultOptions()
	b.ReportAllocs()
	for range b.N {
		if _, err := Encode(data, minCodeSize, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeGIF measures end-to-end encoding of the first frame of a real GIF.
// Skipped if the test GIF is not present.
func BenchmarkEncodeGIF(b *testing.B) {
	f, err := os.Open(testGIFPath)
	if os.IsNotExist(err) {
		b.Skipf("test GIF not found at %s", testGIFPath)
	}
	if err != nil {
		b.Fatalf("open %s: %v", testGIFPath, err)
	}
	defer f.Close() //nolint:errcheck // read-only test fixture; a close error cannot affect the test

	g, err := gif.DecodeAll(f)
	if err != nil {
		b.Fatalf("gif.DecodeAll: %v", err)
	}

	// Use the first frame for a stable, deterministic benchmark.
	img := g.Image[0]
	bounds := img.Bounds()
	pixels := make([]byte, bounds.Dx()*bounds.Dy())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixels[(y-bounds.Min.Y)*bounds.Dx()+(x-bounds.Min.X)] = img.ColorIndexAt(x, y)
		}
	}
	palLen := len(img.Palette)
	minCodeSize := 2
	for (1 << minCodeSize) < palLen {
		minCodeSize++
	}
	if minCodeSize > 8 {
		minCodeSize = 8
	}

	opts := DefaultOptions()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := Encode(pixels, minCodeSize, opts)
		if err != nil {
			b.Fatal(err)
		}
	}
}
