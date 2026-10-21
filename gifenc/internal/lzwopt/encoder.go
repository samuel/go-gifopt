package lzwopt

import "sync"

const (
	// maxCodeSize is the widest LZW code a GIF may contain. Codes are never wider
	// than 12 bits, whatever MaxDictionary asks for.
	maxCodeSize = 12
	// maxDictionary is the largest dictionary a GIF stream can address: 4096
	// entries, the last of which is code 4095.
	maxDictionary = 1 << maxCodeSize
	// saturatingDictionary is the smallest cap at which encoder and decoder both stop
	// growing their dictionaries without a clear code. Below it the encoder must emit a
	// clear code when the dictionary fills, because a decoder keeps adding entries
	// until it sees one.
	saturatingDictionary = maxDictionary - 1
	// defaultMaxDictionary is used when MaxDictionary is 0. The full 4096 entries make
	// code 4095 available, which the format allows and standard decoders handle.
	defaultMaxDictionary = maxDictionary
	// dpSaturationSlack multiplies the dictionary size to get the floor on how many
	// tokens of a candidate block the DP sweep scans. Twice the dictionary size is well
	// past saturation, where every token costs a flat 12 bits.
	//
	// The actual bound also grows with the longest block the sweep has chosen, since
	// the best block length depends on the content. Measured lengths range from 250
	// tokens on a flat frame to 18,350 on a periodic pattern, so any fixed cap is
	// either too slow or costs size.
	dpSaturationSlack = 2
)

type lzwEncoder struct {
	data []byte
	// trie is the dictionary storage, borrowed from dictPool and returned by
	// release. dict and dictGen alias it.
	trie        *dictBuffer
	dict        []int32  // flat: dict[code*256 + symbol] = child code
	dictGen     []uint32 // generation tag parallel to dict; entry valid iff dictGen[i] == genCounter
	genCounter  uint32   // incremented by initDictionary instead of zeroing dict
	dictSize    int
	maxDict     int
	clearCode   int
	eosCode     int
	minCodeSize int
}

type bestBlock struct {
	length    int
	bits      int
	totalBits int64
	tokens    int
	nongreedy int
}

// dictBuffer holds the trie arrays for one encoder. They are pooled because a full-size
// dictionary is 4096*256 entries, about 8.4 MB across the two arrays, and a fresh set
// would otherwise be allocated per frame per worker.
type dictBuffer struct {
	dict []int32
	gen  []uint32
	// counter is the highest generation ever written into gen. A borrower continues
	// counting from it instead of clearing gen: every value in gen is <= counter, so
	// none can match a later generation.
	counter uint32
}

var dictPool sync.Pool

// getDictBuffer returns buffers of at least size entries.
func getDictBuffer(size int) *dictBuffer {
	if b, ok := dictPool.Get().(*dictBuffer); ok {
		if cap(b.dict) >= size && cap(b.gen) >= size {
			b.dict = b.dict[:size]
			b.gen = b.gen[:size]
			return b
		}
		// Too small. Drop it rather than grow it: mixing sizes in one pool would keep
		// evicting whichever size is not currently in use.
	}
	return &dictBuffer{dict: make([]int32, size), gen: make([]uint32, size)}
}

func newLZWEncoder(data []byte, minCodeSize, maxDict int) *lzwEncoder {
	if maxDict == 0 {
		maxDict = defaultMaxDictionary
	}
	trie := getDictBuffer(maxDict * 256)
	return &lzwEncoder{
		data:        data,
		trie:        trie,
		dict:        trie.dict,
		dictGen:     trie.gen,
		genCounter:  trie.counter,
		maxDict:     maxDict,
		minCodeSize: minCodeSize,
	}
}

// release returns the dictionary buffers to the pool. The encoder must not be
// used afterwards.
func (e *lzwEncoder) release() {
	if e.trie == nil {
		return
	}
	e.trie.counter = e.genCounter
	dictPool.Put(e.trie)
	e.trie, e.dict, e.dictGen = nil, nil, nil
}

func (e *lzwEncoder) initDictionary() {
	e.clearCode = 1 << e.minCodeSize
	e.eosCode = e.clearCode + 1
	e.dictSize = e.eosCode + 1
	e.genCounter++
	if e.genCounter == 0 {
		// Wrapped: zero the array and restart at 1 so stale entries are not valid.
		for i := range e.dictGen {
			e.dictGen[i] = 0
		}
		e.genCounter = 1
	}
}

// findMatch walks the dictionary trie from data[from] and returns the number of bytes
// matched (1..maxLength). The dictionary must already be initialized.
func (e *lzwEncoder) findMatch(from, maxLength int) int {
	if from >= len(e.data) || maxLength <= 0 {
		return 0
	}
	code := int(e.data[from])
	length := 1
	for length < maxLength && from+length < len(e.data) {
		idx := code*256 + int(e.data[from+length])
		if e.dictGen[idx] != e.genCounter {
			break
		}
		code = int(e.dict[idx])
		length++
	}
	return length
}

// matchAt is findMatch, also returning the final dictionary code.
func (e *lzwEncoder) matchAt(from, maxLength int) (int, int) {
	if from >= len(e.data) || maxLength <= 0 {
		return 0, -1
	}
	code := int(e.data[from])
	length := 1
	for length < maxLength && from+length < len(e.data) {
		idx := code*256 + int(e.data[from+length])
		if e.dictGen[idx] != e.genCounter {
			break
		}
		code = int(e.dict[idx])
		length++
	}
	return length, code
}

// isRun reports whether all bytes in b are equal.
func isRun(b []byte) bool {
	if len(b) <= 1 {
		return true
	}
	v := b[0]
	for _, c := range b[1:] {
		if c != v {
			return false
		}
	}
	return true
}

// encodeBlock encodes data[from : from+length], where length 0 means to the end of the
// data, initializing the dictionary fresh. A BitStream is built and returned only when
// emitBits is set. isFinal terminates the block with EOS rather than a clear code.
//
// When best is non-nil, it also records split costs: at every token boundary it
// checks whether splitting there costs fewer totalBits than the current
// best[from/alignment], and updates it if so. alignment must then be > 0.
//
// maxTokens bounds how many tokens of a candidate are scanned, 0 meaning to the end of
// the data. The scan never stops before one split candidate has been considered, so
// every swept position gets a usable entry in best. It is ignored when best is nil,
// because a block being emitted has to cover exactly the length it was asked for.
func (e *lzwEncoder) encodeBlock(from, length int, emitBits, isFinal bool, opts Options, best []bestBlock, alignment, maxTokens int) *BitStream {
	e.initDictionary()

	n := len(e.data)
	end := from + length
	if length == 0 {
		end = n
	}

	if alignment <= 0 {
		alignment = 1
	}

	var bs *BitStream
	if emitBits {
		bs = &BitStream{}
	}

	codeSize := e.minCodeSize + 1
	numBits := 0
	numTokens := 0
	numNongreedy := 0
	// scanned records whether a split candidate has been considered. Stopping at
	// maxTokens is only safe after one has.
	scanned := false

	fromIdx := from / alignment

	if opts.StartWithClearCode {
		if emitBits {
			bs.AppendCode(uint(e.clearCode), codeSize)
		}
		numBits += codeSize
	}

	minNG := opts.MinNonGreedyMatch
	if minNG <= 0 {
		minNG = 2
	}
	minImprove := opts.MinImprovement
	if minImprove <= 0 {
		minImprove = 1
	}

	pos := from
	for pos < end {
		remaining := end - pos
		matchLen, code := e.matchAt(pos, remaining)

		// One-step lookahead: try a shorter match if it enables a longer next one,
		// netting fewer total tokens.
		if opts.NonGreedy && matchLen >= minNG {
			// Runs of identical bytes compress well on their own; splitting them
			// rarely helps and can hurt, so skip unless SplitRuns is set.
			if opts.SplitRuns || !isRun(e.data[pos:pos+matchLen]) {
				greedyNext := e.findMatch(pos+matchLen, end-(pos+matchLen))
				bestSum := matchLen + greedyNext
				choice := matchLen

				for shorter := matchLen - 1; shorter >= 1; shorter-- {
					next := e.findMatch(pos+shorter, end-(pos+shorter))
					if shorter+next >= bestSum+minImprove {
						bestSum = shorter + next
						choice = shorter
					}
				}

				if choice < matchLen {
					matchLen, code = e.matchAt(pos, choice)
					numNongreedy++
				}
			}
		}

		if emitBits {
			bs.AppendCode(uint(code), codeSize)
		}
		numBits += codeSize
		numTokens++

		// Add the dictionary entry for (code, next byte), but only into a vacant trie
		// slot: a non-greedy shorter match may point at a node whose child already
		// exists, and overwriting it would corrupt later lookups.
		//
		// dictSize is incremented regardless, to mirror the decoder's counter, which
		// advances for every emitted code including a block's last. Skipping it there
		// would desync the code width whenever the increment crosses the overflow
		// threshold.
		if pos+matchLen < end && e.dictSize < e.maxDict {
			idx := code*256 + int(e.data[pos+matchLen])
			if e.dictGen[idx] != e.genCounter {
				e.dict[idx] = int32(e.dictSize)
				e.dictGen[idx] = e.genCounter
			}
		}
		if e.dictSize < e.maxDict {
			e.dictSize++
		}
		switch {
		case e.dictSize >= e.maxDict && e.maxDict < saturatingDictionary:
			// A dictionary capped below the format's limit has to be reset explicitly:
			// a decoder grows its own until it sees a clear code, so stopping quietly
			// would leave it widening its codes past ours. At the format limit both
			// sides saturate on their own.
			if emitBits {
				bs.AppendCode(uint(e.clearCode), codeSize)
			}
			numBits += codeSize
			e.initDictionary()
			codeSize = e.minCodeSize + 1
		case e.dictSize > (1<<codeSize) && codeSize < maxCodeSize:
			// Codes never exceed 12 bits, even as the dictionary keeps filling.
			codeSize++
		}

		// Record the cost of ending the block at splitPos. This runs after the
		// dictionary update so codeSize reflects any increment this token triggered:
		// the terminator is emitted after the last real code, where the decoder's
		// width matches the post-increment value.
		if best != nil {
			splitPos := pos + matchLen
			isEnd := splitPos == n
			if isEnd || splitPos%alignment == 0 {
				// Ceiling division, so a splitPos not on an alignment boundary maps to
				// the zero-initialized base case beyond the last swept position.
				nextIdx := (splitPos + alignment - 1) / alignment
				if nextIdx < len(best) {
					// Skip positions whose following block has not been computed yet;
					// at the end of data totalBits == 0 is the correct base case.
					if isEnd || best[nextIdx].totalBits != 0 {
						// Only bits are counted: sub-block length bytes depend only on
						// the total bits, so they cannot change which split wins.
						terminatorBits := codeSize
						cost := int64(numBits+terminatorBits) + best[nextIdx].totalBits
						if best[fromIdx].totalBits == 0 || cost < best[fromIdx].totalBits {
							best[fromIdx] = bestBlock{
								length:    splitPos - from,
								bits:      numBits + terminatorBits,
								totalBits: cost,
								tokens:    numTokens,
								nongreedy: numNongreedy,
							}
						}
						// best[fromIdx] now holds a candidate, so the scan may stop at
						// maxTokens.
						scanned = true
					}
				}
			}
		}

		pos += matchLen

		if maxTokens > 0 && best != nil && scanned && numTokens >= maxTokens {
			break
		}
	}

	if emitBits {
		if isFinal {
			bs.AppendCode(uint(e.eosCode), codeSize)
		} else {
			bs.AppendCode(uint(e.clearCode), codeSize)
		}
	}

	return bs
}

// optimizeAndMerge runs the right-to-left DP sweep to find the optimal split points,
// then encodes the data in one forward pass, building each block's BitStream as it
// goes.
func (e *lzwEncoder) optimizeAndMerge(opts Options) *BitStream {
	n := len(e.data)

	alignment := resolveAlignment(opts.Alignment, opts.AlignmentPositions, n)

	bestSize := n/alignment + 2
	best := make([]bestBlock, bestSize)

	startPos := (n / alignment) * alignment
	if startPos == n {
		startPos -= alignment
	}
	if startPos < 0 {
		startPos = 0
	}

	sweepOpts := opts
	sweepOpts.StartWithClearCode = false

	// Without a bound, every swept position is encoded to the end of the data, which
	// makes the sweep quadratic in the frame size.
	//
	// The bound starts at the floor and grows to twice the longest block the sweep has
	// chosen. That is safe because the sweep runs right to left: blocks near the end
	// are short and grow smoothly, so the bound has grown past a long block by the time
	// one is needed.
	tokenLimit := dpSaturationSlack * e.maxDict

	sweeps := 1
	if opts.SmartGreedy && opts.NonGreedy {
		sweeps = 2
	}

	// The second sweep is pure greedy, and overwrites best[i] where greedy wins:
	// non-greedy matching can pollute the dictionary with duplicate entries and come
	// out larger.
	for sweep := range sweeps {
		sweepOpts.NonGreedy = opts.NonGreedy && sweep == 0
		for pos := startPos; pos >= 0; pos -= alignment {
			e.encodeBlock(pos, 0, false, true, sweepOpts, best, alignment, tokenLimit)
			tokenLimit = max(tokenLimit, 2*best[pos/alignment].tokens)
			if opts.Progress != nil {
				// Spread across all sweeps so progress only increases and reaches the
				// total after the last one.
				opts.Progress((sweep*n+(n-pos))/sweeps, n)
			}
			if pos == 0 {
				break
			}
		}
	}

	bs := &BitStream{}

	if opts.StartWithClearCode {
		clearCode := 1 << e.minCodeSize
		codeSize := e.minCodeSize + 1
		bs.AppendCode(uint(clearCode), codeSize)
	}

	blockOpts := opts
	blockOpts.StartWithClearCode = false

	pos := 0
	for pos < n {
		idx := pos / alignment
		length := best[idx].length
		if length <= 0 {
			length = n - pos
		}
		isFinal := pos+length >= n
		// Where the sweep chose greedy for this block (nongreedy == 0), encode it
		// greedily so it matches the DP's cost estimate.
		encOpts := blockOpts
		if opts.SmartGreedy && opts.NonGreedy && best[idx].nongreedy == 0 {
			encOpts.NonGreedy = false
		}
		blockBS := e.encodeBlock(pos, length, true, isFinal, encOpts, nil, 0, 0)
		bs.Append(blockBS)
		pos += length
	}

	return bs
}
