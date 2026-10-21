package lzwopt

import (
	"bytes"
	"compress/lzw"
	"fmt"
	"image/gif"
	"io"
	"os"
	"slices"
	"testing"
)

const testGIFPath = "testdata/test_4frame_encoder_test.gif"

// stripGIFSubBlocks unwraps GIF sub-block framing, returning the raw LZW bytes.
func stripGIFSubBlocks(data []byte) []byte {
	var out []byte
	for len(data) > 0 {
		n := int(data[0])
		data = data[1:]
		if n == 0 {
			break
		}
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return out
}

// roundTrip encodes data with lzwopt and decodes with compress/lzw, returning the decoded bytes.
func roundTrip(t *testing.T, data []byte, minCodeSize int, opts Options) []byte {
	t.Helper()
	encoded, err := Encode(data, minCodeSize, opts)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	raw := stripGIFSubBlocks(encoded)
	r := lzw.NewReader(bytes.NewReader(raw), lzw.LSB, minCodeSize)
	decoded, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("lzw decode: %v", err)
	}
	return decoded
}

func TestRoundTrip(t *testing.T) {
	opts := DefaultOptions()

	tests := []struct {
		name        string
		data        []byte
		minCodeSize int
	}{
		{"all-same", bytes.Repeat([]byte{0x03}, 100), 2},
		// minCodeSize=3 so all bytes 0x01..0x04 are valid palette indices (< clearCode=8).
		{"repeated-pattern", bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 50), 3},
		{"sequential", func() []byte {
			b := make([]byte, 200)
			for i := range b {
				b[i] = byte(i % 16)
			}
			return b
		}(), 4},
		{"minCodeSize-8", []byte("Hello, World! Hello, World! Hello!"), 8},
		{"single-byte", []byte{0x00}, 2},
		{"exactly-256", func() []byte {
			b := make([]byte, 256)
			for i := range b {
				b[i] = byte(i)
			}
			return b
		}(), 8},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decoded := roundTrip(t, tc.data, tc.minCodeSize, opts)
			if !bytes.Equal(decoded, tc.data) {
				t.Errorf("round-trip mismatch: got %d bytes, want %d bytes", len(decoded), len(tc.data))
			}
		})
	}
}

func TestEmptyInput(t *testing.T) {
	opts := DefaultOptions()
	encoded, err := Encode([]byte{}, 2, opts)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	raw := stripGIFSubBlocks(encoded)
	r := lzw.NewReader(bytes.NewReader(raw), lzw.LSB, 2)
	decoded, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("lzw decode: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected empty output, got %d bytes", len(decoded))
	}
}

// stdLZWSize encodes data with Go's standard compress/lzw and returns the byte count.
func stdLZWSize(t *testing.T, data []byte, minCodeSize int) int {
	t.Helper()
	var buf bytes.Buffer
	w := lzw.NewWriter(&buf, lzw.LSB, minCodeSize)
	if _, err := w.Write(data); err != nil {
		t.Fatalf("std lzw write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("std lzw close: %v", err)
	}
	return buf.Len()
}

// TestSizeVsStd verifies that lzwopt raw LZW output is never larger than
// Go's standard encoder for a variety of inputs.
func TestSizeVsStd(t *testing.T) {
	opts := DefaultOptions()

	inputs := []struct {
		name        string
		data        []byte
		minCodeSize int
	}{
		{"all-same-100", bytes.Repeat([]byte{0x03}, 100), 2},
		// minCodeSize=3 → clearCode=8; use values 0..7 only
		{"repeated-200", bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 50), 3},
		{"sequential-200", func() []byte {
			b := make([]byte, 200)
			for i := range b {
				b[i] = byte(i % 16)
			}
			return b
		}(), 4},
		{"string-8bit", []byte("Hello, World! Hello, World! Hello!"), 8},
		{"single-byte", []byte{0x00}, 2},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := Encode(tc.data, tc.minCodeSize, opts)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			// Compare raw LZW byte counts (strip GIF sub-block framing).
			rawSize := len(stripGIFSubBlocks(encoded))
			stdSize := stdLZWSize(t, tc.data, tc.minCodeSize)
			if rawSize > stdSize {
				t.Errorf("lzwopt raw size %d > std size %d for %q", rawSize, stdSize, tc.name)
			}
		})
	}
}

// frameProgress returns an Options.Progress callback that logs encoding progress
// for a specific GIF frame using t.Log.
func frameProgress(t *testing.T, frameIdx int) func(done, total int) {
	t.Helper()
	lastPct := -1
	return func(done, total int) {
		if total == 0 {
			return
		}
		pct := done * 10 / total * 10 // round to nearest 10%
		if pct != lastPct {
			lastPct = pct
			t.Logf("frame %d: %d%%", frameIdx, pct)
		}
	}
}

// TestSizeVsStdGIF compares lzwopt against the standard encoder for each frame
// of the test GIF. Skipped if the file is not present.
func TestSizeVsStdGIF(t *testing.T) {
	f, err := os.Open(testGIFPath)
	if os.IsNotExist(err) {
		t.Skipf("test GIF not found at %s", testGIFPath)
	}
	if err != nil {
		t.Fatalf("open %s: %v", testGIFPath, err)
	}
	defer f.Close() //nolint:errcheck // read-only test fixture; a close error cannot affect the test

	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatalf("gif.DecodeAll: %v", err)
	}

	opts := DefaultOptions()
	for i, img := range g.Image {
		opts.Progress = frameProgress(t, i)
		bounds := img.Bounds()
		pixels := make([]byte, bounds.Dx()*bounds.Dy())
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				idx := img.ColorIndexAt(x, y)
				pixels[(y-bounds.Min.Y)*bounds.Dx()+(x-bounds.Min.X)] = idx
			}
		}

		// Determine minCodeSize from palette size.
		palLen := len(img.Palette)
		minCodeSize := 2
		for (1 << minCodeSize) < palLen {
			minCodeSize++
		}
		if minCodeSize > 8 {
			minCodeSize = 8
		}

		encoded, err := Encode(pixels, minCodeSize, opts)
		if err != nil {
			t.Fatalf("frame %d Encode: %v", i, err)
		}
		rawSize := len(stripGIFSubBlocks(encoded))
		stdSize := stdLZWSize(t, pixels, minCodeSize)
		if rawSize > stdSize {
			t.Errorf("frame %d: lzwopt raw size %d > std size %d", i, rawSize, stdSize)
		}

		// Verify round-trip.
		decoded := roundTrip(t, pixels, minCodeSize, opts)
		if !bytes.Equal(decoded, pixels) {
			t.Errorf("frame %d: round-trip mismatch", i)
		}
	}
}

// TestAlignmentInvariant verifies that different alignment values all produce
// decodable output, and that size increases (or stays equal) as alignment grows.
func TestAlignmentInvariant(t *testing.T) {
	// minCodeSize=4 → clearCode=16; byte values 1..8 are all < 16 and thus valid.
	data := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 50)
	minCodeSize := 4

	alignments := []int{1, 4, 16}
	sizes := make([]int, len(alignments))

	for i, a := range alignments {
		opts := DefaultOptions()
		opts.Alignment = a

		encoded, err := Encode(data, minCodeSize, opts)
		if err != nil {
			t.Fatalf("alignment=%d Encode: %v", a, err)
		}
		sizes[i] = len(encoded)

		// Must decode correctly.
		decoded := roundTrip(t, data, minCodeSize, opts)
		if !bytes.Equal(decoded, data) {
			t.Errorf("alignment=%d: round-trip mismatch", a)
		}
	}

	// Larger alignment should not produce smaller output than alignment=1.
	for i := 1; i < len(alignments); i++ {
		if sizes[i] < sizes[0] {
			t.Errorf("alignment=%d size %d < alignment=1 size %d (unexpected improvement)",
				alignments[i], sizes[i], sizes[0])
		}
	}
}

// TestNonGreedy checks that one-step lookahead produces decodable output. It is a
// heuristic and can come out larger than greedy on synthetic highly-compressible
// patterns, so size is only compared against greedy on real frames, in
// TestNonGreedyGIF.
func TestNonGreedy(t *testing.T) {
	inputs := []struct {
		name        string
		data        []byte
		minCodeSize int
	}{
		{"all-same-100", bytes.Repeat([]byte{0x03}, 100), 2},
		{"repeated-200", bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 50), 3},
		{"sequential-200", func() []byte {
			b := make([]byte, 200)
			for i := range b {
				b[i] = byte(i % 16)
			}
			return b
		}(), 4},
		{"mixed", func() []byte {
			b := make([]byte, 300)
			for i := range b {
				b[i] = byte((i*7 + i/3) % 8)
			}
			return b
		}(), 3},
		{"single-byte", []byte{0x00}, 2},
	}

	ngOpts := DefaultOptions()
	ngOpts.NonGreedy = true

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			decoded := roundTrip(t, tc.data, tc.minCodeSize, ngOpts)
			if !bytes.Equal(decoded, tc.data) {
				t.Errorf("non-greedy round-trip mismatch: got %d bytes, want %d", len(decoded), len(tc.data))
			}
		})
	}
}

// TestNonGreedyGIF checks that non-greedy comes out no larger than greedy in
// aggregate across a real GIF's frames. The lookahead counts bytes covered and
// models neither code width nor sub-block rounding, so per-frame regressions of a
// few bytes are expected and only logged.
func TestNonGreedyGIF(t *testing.T) {
	f, err := os.Open(testGIFPath)
	if os.IsNotExist(err) {
		t.Skipf("test GIF not found at %s", testGIFPath)
	}
	if err != nil {
		t.Fatalf("open %s: %v", testGIFPath, err)
	}
	defer f.Close() //nolint:errcheck // read-only test fixture; a close error cannot affect the test

	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatalf("gif.DecodeAll: %v", err)
	}

	greedyOpts := DefaultOptions()
	greedyOpts.NonGreedy = false
	ngOpts := DefaultOptions()
	ngOpts.NonGreedy = true

	var totalGreedy, totalNG int
	for i, img := range g.Image {
		greedyOpts.Progress = frameProgress(t, i)
		ngOpts.Progress = frameProgress(t, i)
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

		// Round-trip must be correct.
		decoded := roundTrip(t, pixels, minCodeSize, ngOpts)
		if !bytes.Equal(decoded, pixels) {
			t.Errorf("frame %d: non-greedy round-trip mismatch", i)
		}

		greedyEnc, err := Encode(pixels, minCodeSize, greedyOpts)
		if err != nil {
			t.Fatalf("frame %d: greedy Encode: %v", i, err)
		}
		ngEnc, err := Encode(pixels, minCodeSize, ngOpts)
		if err != nil {
			t.Fatalf("frame %d: non-greedy Encode: %v", i, err)
		}
		totalGreedy += len(greedyEnc)
		totalNG += len(ngEnc)
		if len(ngEnc) > len(greedyEnc) {
			// Per-frame regressions are expected; only the aggregate can fail.
			t.Logf("frame %d: non-greedy size %d > greedy size %d (delta %+d)",
				i, len(ngEnc), len(greedyEnc), len(ngEnc)-len(greedyEnc))
		}
	}
	if totalNG > totalGreedy {
		t.Errorf("non-greedy total %d > greedy total %d (delta %+d)",
			totalNG, totalGreedy, totalNG-totalGreedy)
	}
}

// TestDPSplit checks the output when the DP splits the input into several blocks.
// The dataset fills the dictionary with unique patterns, then turns repetitive, so a
// fresh dictionary pays.
func TestDPSplit(t *testing.T) {
	const minCodeSize = 8 // 256-color palette

	// Fill the dictionary: clearCode=256, eosCode=257, first entry=258, max=4095 → 3837 slots.
	// Emit 3837 unique 2-byte pairs to saturate the dictionary.
	firstHalf := make([]byte, 0, 8000)
	count := 0
	for a := 0; a < 256 && count < 3837; a++ {
		for b := 0; b < 256 && count < 3837; b++ {
			firstHalf = append(firstHalf, byte(a), byte(b))
			count++
		}
	}
	// Second half: highly repetitive — benefits from a fresh dictionary after reset.
	secondHalf := bytes.Repeat([]byte{0}, 10000)
	data := slices.Concat(firstHalf, secondHalf)

	t.Logf("data size: %d bytes", len(data))

	for _, alignment := range []int{1, 100, 1000, 65536} {
		opts := DefaultOptions()
		opts.Alignment = alignment
		t.Run(fmt.Sprintf("alignment=%d", alignment), func(t *testing.T) {
			decoded := roundTrip(t, data, minCodeSize, opts)
			if !bytes.Equal(decoded, data) {
				t.Errorf("round-trip mismatch: got %d bytes, want %d", len(decoded), len(data))
			}
		})
	}
}

// TestMatchesStdLZW verifies that for small inputs (dict never fills), our
// greedy encoder produces output that decodes to the same data as the standard encoder.
func TestMatchesStdLZW(t *testing.T) {
	opts := DefaultOptions()
	inputs := []struct {
		data        []byte
		minCodeSize int
	}{
		{bytes.Repeat([]byte{1, 2, 3}, 10), 2},
		{[]byte{0, 1, 2, 3, 0, 1, 2, 3, 0, 1}, 2},
	}

	for _, tc := range inputs {
		// Encode with standard lzw.
		var stdBuf bytes.Buffer
		w := lzw.NewWriter(&stdBuf, lzw.LSB, tc.minCodeSize)
		if _, err := w.Write(tc.data); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		// Decode standard output.
		stdR := lzw.NewReader(bytes.NewReader(stdBuf.Bytes()), lzw.LSB, tc.minCodeSize)
		stdDecoded, err := io.ReadAll(stdR)
		if err != nil {
			t.Fatalf("std decode: %v", err)
		}

		// Encode with lzwopt and round-trip.
		decoded := roundTrip(t, tc.data, tc.minCodeSize, opts)

		if !bytes.Equal(decoded, stdDecoded) {
			t.Errorf("decoded mismatch with std: got %v, want %v", decoded, stdDecoded)
		}
	}
}

// TestMaxDictionaryValidation covers the range Encode accepts.
func TestMaxDictionaryValidation(t *testing.T) {
	data := bytes.Repeat([]byte{1, 2, 3}, 100)
	tests := []struct {
		name        string
		maxDict     int
		minCodeSize int
		wantErr     bool
	}{
		{"zero means the GIF maximum", 0, 8, false},
		{"lowest legal", 1<<8 + 2, 8, false},
		{"one below the lowest legal", 1<<8 + 1, 8, true},
		{"4096 is addressable", 4096, 8, false},
		{"4097 is not", 4097, 8, true},
		{"negative", -1, 8, true},
		{"lowest legal scales with minCodeSize", 1<<2 + 2, 2, false},
		{"below the initial dictionary for this code size", 1<<2 + 1, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultOptions()
			opts.MaxDictionary = tc.maxDict
			_, err := Encode(data, tc.minCodeSize, opts)
			if tc.wantErr && err == nil {
				t.Fatalf("MaxDictionary %d: expected an error", tc.maxDict)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("MaxDictionary %d: %v", tc.maxDict, err)
			}
		})
	}
}

// TestMaxDictionaryRoundTrips checks that a full dictionary still produces a stream a
// standard decoder accepts: the code width has to stop at 12 bits.
func TestMaxDictionaryRoundTrips(t *testing.T) {
	// Enough varied data to drive the dictionary all the way to saturation.
	data := make([]byte, 64*1024)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	opts := DefaultOptions()
	// The exhaustive DP sweep is quadratic; a coarse alignment keeps this fast
	// without changing what is being tested.
	opts.Alignment = 4096

	for _, maxDict := range []int{258, 1000, 4095, 4096} {
		t.Run(fmt.Sprintf("maxdict-%d", maxDict), func(t *testing.T) {
			o := opts
			o.MaxDictionary = maxDict
			if decoded := roundTrip(t, data, 8, o); !bytes.Equal(decoded, data) {
				t.Errorf("round-trip mismatch: got %d bytes, want %d", len(decoded), len(data))
			}
		})
	}
}

func TestResolveAlignment(t *testing.T) {
	tests := []struct {
		name      string
		alignment int
		positions int
		n         int
		want      int
	}{
		{"explicit alignment wins", 64, 999, 100000, 64},
		{"explicit alignment wins over the default too", 7, 0, 100000, 7},
		{"auto with the package default", 0, 0, 100000, (100000 + autoAlignmentPositions - 1) / autoAlignmentPositions},
		{"auto with an explicit target", 0, 100, 100000, 1000},
		{"a target coarser than the data searches every byte", 0, 100, 50, 1},
		{"exactly the target searches every byte", 0, 100, 100, 1},
		{"one over the target", 0, 100, 101, 2},
		{"negative alignment is auto", -1, 16, 1600, 100},
		{"negative target is the package default", 0, -5, 100000, (100000 + autoAlignmentPositions - 1) / autoAlignmentPositions},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveAlignment(tc.alignment, tc.positions, tc.n); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestAlignmentPositionsControlsTheSweep checks that the option actually reaches the
// sweep: a coarser target must produce output no smaller than a finer one.
func TestAlignmentPositionsControlsTheSweep(t *testing.T) {
	data := make([]byte, 20000)
	for i := range data {
		data[i] = byte((i*7 + i/23) % 64)
	}
	var prev int
	for _, positions := range []int{1, 16, 128, 2048} {
		opts := DefaultOptions()
		opts.AlignmentPositions = positions
		out, err := Encode(data, 6, opts)
		if err != nil {
			t.Fatalf("positions %d: %v", positions, err)
		}
		if decoded := roundTrip(t, data, 6, opts); !bytes.Equal(decoded, data) {
			t.Fatalf("positions %d: round-trip mismatch", positions)
		}
		if prev != 0 && len(out) > prev {
			t.Errorf("positions %d: %d bytes, larger than the coarser sweep's %d", positions, len(out), prev)
		}
		prev = len(out)
	}
}
