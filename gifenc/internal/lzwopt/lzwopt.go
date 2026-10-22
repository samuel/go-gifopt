// Package lzwopt implements an optimizing GIF LZW encoder based on the flexiGIF
// algorithm. It uses flexible dictionary reset (dynamic programming over candidate
// restart positions) and optional one-step lookahead matching to produce smaller
// output than a greedy LZW encoder.
package lzwopt

import "fmt"

// Options controls the optimizer.
type Options struct {
	// Alignment is the search granularity in bytes: the DP considers a block boundary
	// every Alignment bytes, so 1 searches every byte and larger values trade
	// compression for speed. 0 or negative derives it from AlignmentPositions.
	//
	// Prefer AlignmentPositions: a fixed granularity searches frames of different
	// sizes with different effort.
	Alignment int
	// AlignmentPositions is roughly how many candidate block starts the sweep considers
	// per frame, whatever the frame size; 0 means the package default. Encoding time is
	// directly proportional to it.
	AlignmentPositions int
	// MaxDictionary forces a clear before the dictionary reaches this size
	// (0 = the GIF maximum of 4096). Must be in [(1<<minCodeSize)+2, 4096] when set.
	MaxDictionary int
	// NonGreedy enables one-step lookahead matching.
	NonGreedy bool
	// MinNonGreedyMatch is the minimum match length considered for non-greedy search (default 2).
	MinNonGreedyMatch int
	// MinImprovement is the minimum extra bytes saved to accept a non-greedy match (default 1).
	MinImprovement int
	// SplitRuns allows non-greedy to split long runs of the same byte (default false).
	SplitRuns bool
	// SmartGreedy runs an additional greedy DP sweep after the non-greedy one, keeping
	// the better result per block start. It only applies with NonGreedy, and should
	// stay on with it: it costs 30% of the encoding time for 0.2% in size, and
	// prevents non-greedy matching from compressing worse than plain greedy. To go
	// faster, turn NonGreedy off instead, which is three times cheaper than the pair
	// and costs about 0.7% in size.
	SmartGreedy bool
	// StartWithClearCode prepends an initial clear code (required by most decoders, default true).
	StartWithClearCode bool
	// Progress is an optional callback invoked during the DP optimization sweep.
	// done is the number of input bytes processed so far; total is len(data).
	Progress func(done, total int)
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{
		Alignment:          0, // auto, see Options.Alignment
		AlignmentPositions: autoAlignmentPositions,
		MinNonGreedyMatch:  2,
		MinImprovement:     1,
		NonGreedy:          true,
		SmartGreedy:        true,
		StartWithClearCode: true,
	}
}

// autoAlignmentPositions is the default for Options.AlignmentPositions. Frames no
// larger than this are searched at every byte.
//
// It is the knee of the measured curve: over the corpus it comes within 0.78% of
// searching 2048 positions for 7.7x less time, and the size saved per extra second of
// encoding falls more than tenfold from 128 to 256 positions to 512 to 2048.
const autoAlignmentPositions = 256

// resolveAlignment returns the search granularity for n bytes of input, expanding a
// non-positive alignment into one that sweeps about positions candidates.
func resolveAlignment(alignment, positions, n int) int {
	if alignment > 0 {
		return alignment
	}
	if positions <= 0 {
		positions = autoAlignmentPositions
	}
	if n <= positions {
		return 1
	}
	return (n + positions - 1) / positions
}

// Encode compresses data using optimized GIF LZW and returns GIF sub-block bytes.
// minCodeSize is typically ceil(log2(numColors)) clamped to [2,8].
func Encode(data []byte, minCodeSize int, opts Options) ([]byte, error) {
	return (*Workers)(nil).Encode(data, minCodeSize, opts)
}

// Encode is the package-level Encode, run as one of w's workers. It waits for a worker
// to be free, and borrows any others that are idle. A nil w searches on the calling
// goroutine alone.
func (w *Workers) Encode(data []byte, minCodeSize int, opts Options) ([]byte, error) {
	if minCodeSize < 2 || minCodeSize > 8 {
		return nil, fmt.Errorf("lzwopt: minCodeSize %d out of range [2,8]", minCodeSize)
	}
	maxDict := opts.MaxDictionary
	if maxDict == 0 {
		maxDict = defaultMaxDictionary
	}
	// Below the initial size there is no room for a single entry; above 4096 a code
	// cannot be addressed in 12 bits.
	if lo := (1 << minCodeSize) + 2; maxDict < lo || maxDict > maxDictionary {
		return nil, fmt.Errorf("lzwopt: MaxDictionary %d out of range [%d,%d]", maxDict, lo, maxDictionary)
	}
	// A symbol at or above the clear code cannot be coded, and the trie has no slot
	// for it.
	if minCodeSize < 8 {
		for i, b := range data {
			if int(b)>>minCodeSize != 0 {
				return nil, fmt.Errorf("lzwopt: byte %d at offset %d does not fit in minCodeSize %d", b, i, minCodeSize)
			}
		}
	}
	enc := newLZWEncoder(data, minCodeSize, maxDict)
	defer enc.release()
	if len(data) == 0 {
		var bs BitStream
		enc.encodeBlock(0, 0, &bs, true, opts, nil, 0, 0)
		return bs.ToGIFSubBlocks(), nil
	}
	if opts.NonGreedy {
		enc.prefix = prefixHashes(data)
	}
	if w != nil {
		w.acquire()
		defer w.release()
	}
	bs := enc.optimizeAndMerge(opts, w)
	return bs.ToGIFSubBlocks(), nil
}
