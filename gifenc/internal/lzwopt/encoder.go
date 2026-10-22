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

// dictEntries is the size of the trie array: a child slot for every code and every
// byte. A frame coded at fewer bits uses only a prefix of it.
const dictEntries = maxDictionary * 256

// stringSlots is the size of the dictionary's string hash set, twice the largest
// dictionary so a probe rarely goes past its first slot.
const (
	stringSlotBits = maxCodeSize + 1
	stringSlots    = 1 << stringSlotBits
)

// stringHashBase is the multiplier of the polynomial string hash.
const stringHashBase = 0x100000001b3

// hashPowers[i] is stringHashBase**i, for every length a dictionary string can have.
var hashPowers = func() *[maxDictionary + 1]uint64 {
	var p [maxDictionary + 1]uint64
	p[0] = 1
	for i := 1; i < len(p); i++ {
		p[i] = p[i-1] * stringHashBase
	}
	return &p
}()

// prefixHashes returns the polynomial hash of every prefix of data, so the hash of any
// substring is available in constant time from substringHash.
func prefixHashes(data []byte) []uint64 {
	h := make([]uint64, len(data)+1)
	for i, b := range data {
		h[i+1] = h[i]*stringHashBase + uint64(b) + 1
	}
	return h
}

type lzwEncoder struct {
	data []byte
	// prefix is prefixHashes(data), shared by every encoder of the same data. It is
	// nil unless the non-greedy search is on, which is the only user of the string
	// set.
	prefix []uint64
	// trie is the dictionary storage, borrowed from dictPool and returned by
	// release.
	trie *dictBuffer
	// maxLenFrom[s] is the length of the longest dictionary string starting with s,
	// and maxLen the longest of all. A trie walk from s cannot match more, which lets
	// the non-greedy search skip walks that cannot beat the current choice.
	maxLenFrom  [256]int32
	maxLen      int
	dictSize    int
	maxDict     int
	clearCode   int
	eosCode     int
	minCodeSize int
}

// dictBuffer is the dictionary trie. dict[code<<minCodeSize | symbol] is the child
// code, or 0 for none: no child is code 0, since codes below the first entry are
// literals, clear and EOS. used lists the slots filled since the last reset, so
// initDictionary clears only those.
//
// strings is a hash set of the polynomial hashes of every dictionary string, kept
// only for the non-greedy search, which mostly asks whether one particular string is
// in the dictionary and gets no for an answer. A walk would answer by following
// every byte of it; the set answers in one probe. A slot holds hash|1, and 0 when
// empty. stringsUsed lists the filled slots, as used does for dict.
//
// Every step of a trie walk is a dependent load into dict, so its footprint in cache
// is what encoding costs; 16-bit entries halve it. It is pooled because it is over
// 2 MB,
// and a fresh one would otherwise be allocated per frame per worker. A buffer is
// always empty while in the pool.
type dictBuffer struct {
	dict        [dictEntries]uint16
	used        []uint32
	strings     [stringSlots]uint64
	stringsUsed []uint32
}

var dictPool = sync.Pool{
	New: func() any {
		return &dictBuffer{
			used:        make([]uint32, 0, maxDictionary),
			stringsUsed: make([]uint32, 0, maxDictionary),
		}
	},
}

func newLZWEncoder(data []byte, minCodeSize, maxDict int) *lzwEncoder {
	if maxDict == 0 {
		maxDict = defaultMaxDictionary
	}
	return &lzwEncoder{
		data:        data,
		trie:        dictPool.Get().(*dictBuffer), //nolint:forcetypeassert // the pool only ever holds *dictBuffer
		maxDict:     maxDict,
		minCodeSize: minCodeSize,
	}
}

// release returns the dictionary buffer to the pool. The encoder must not be used
// afterwards.
func (e *lzwEncoder) release() {
	if e.trie == nil {
		return
	}
	e.trie.reset()
	dictPool.Put(e.trie)
	e.trie = nil
}

func (b *dictBuffer) reset() {
	for _, i := range b.used {
		b.dict[i&(dictEntries-1)] = 0
	}
	b.used = b.used[:0]
	for _, i := range b.stringsUsed {
		b.strings[i&(stringSlots-1)] = 0
	}
	b.stringsUsed = b.stringsUsed[:0]
}

// substringHash returns the hash of data[from : from+length].
func (e *lzwEncoder) substringHash(from, length int) uint64 {
	return e.prefix[from+length] - e.prefix[from]*hashPowers[length]
}

// stringSlot returns where the probe for hash h starts.
func stringSlot(h uint64) uint32 {
	return uint32((h * 0x9E3779B97F4A7C15) >> (64 - stringSlotBits))
}

// addString records data[from : from+length] in the string set.
func (e *lzwEncoder) addString(from, length int) {
	h := e.substringHash(from, length) | 1
	t := e.trie
	for i := stringSlot(h); ; i = (i + 1) & (stringSlots - 1) {
		switch t.strings[i] {
		case 0:
			t.strings[i] = h
			t.stringsUsed = append(t.stringsUsed, i)
			return
		case h:
			// A collision with another string. It only makes hasString report a
			// string that is not there, which the walk it leads to corrects.
			return
		}
	}
}

// hasString reports whether data[from : from+length] might be a dictionary string. It
// is never wrong when it reports false.
func (e *lzwEncoder) hasString(from, length int) bool {
	h := e.substringHash(from, length) | 1
	t := e.trie
	for i := stringSlot(h); ; i = (i + 1) & (stringSlots - 1) {
		switch t.strings[i] {
		case 0:
			return false
		case h:
			return true
		}
	}
}

func (e *lzwEncoder) initDictionary() {
	e.clearCode = 1 << e.minCodeSize
	e.eosCode = e.clearCode + 1
	e.dictSize = e.eosCode + 1
	e.trie.reset()
	for i := range e.clearCode {
		e.maxLenFrom[i] = 1
	}
	e.maxLen = 1
}

// slot returns the trie slot index for extending code by sym.
func (e *lzwEncoder) slot(code int, sym byte) uint32 {
	return uint32(code<<e.minCodeSize|int(sym)) & (dictEntries - 1)
}

// matchAt walks the dictionary trie from data[from] and returns the number of bytes
// matched (1..maxLength) and the code of the matched string. from+maxLength must not
// exceed len(data).
func (e *lzwEncoder) matchAt(from, maxLength int) (int, int) {
	if maxLength <= 0 {
		return 0, -1
	}
	in := e.data[from : from+maxLength]
	code := int(in[0])
	length := 1
	for _, sym := range in[1:] {
		child := e.trie.dict[e.slot(code, sym)]
		if child == 0 {
			break
		}
		code = int(child)
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
// data, initializing the dictionary fresh. The codes are appended to bs when it is
// non-nil; otherwise only their cost is counted. isFinal terminates the block with EOS
// rather than a clear code.
//
// When scan is non-nil, the block is a DP sweep candidate: every token boundary that is
// a multiple of alignment, or the end of the data, is recorded in scan as a place the
// block could end. alignment must then be > 0.
//
// maxTokens bounds how many tokens of a candidate are scanned, 0 meaning to the end of
// the data. The scan never stops before one split has been recorded, so every swept
// position gets a usable entry. It is ignored when scan is nil, because a block being
// emitted has to cover exactly the length it was asked for.
func (e *lzwEncoder) encodeBlock(from, length int, bs *BitStream, isFinal bool, opts Options, scan *scanResult, alignment, maxTokens int) {
	e.initDictionary()

	n := len(e.data)
	end := from + length
	if length == 0 {
		end = n
	}

	if alignment <= 0 {
		alignment = 1
	}
	// boundary is the first multiple of alignment not yet passed, so split positions
	// can be checked for alignment without a division per token.
	boundary := (from/alignment + 1) * alignment

	codeSize := e.minCodeSize + 1
	numBits := 0
	numTokens := 0
	numNongreedy := 0
	if scan != nil {
		scan.splits = scan.splits[:0]
		scan.reachedEnd = false
	}

	if opts.StartWithClearCode {
		if bs != nil {
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

	// useStrings is whether the string set is kept up to date. It costs an insert per
	// token, and only the non-greedy search reads it.
	useStrings := opts.NonGreedy && e.prefix != nil

	// The non-greedy search walks the trie from where the next token will start, so
	// it is kept for the next iteration instead of walked again. aheadPos is where it
	// starts, or -1 when there is none.
	aheadPos, aheadLen, aheadCode := -1, 0, 0

	pos := from
	for pos < end {
		remaining := end - pos
		var matchLen, code int
		if pos == aheadPos {
			matchLen, code = aheadLen, aheadCode
		} else {
			matchLen, code = e.matchAt(pos, remaining)
		}
		aheadPos = -1

		// One-step lookahead: try a shorter match if it enables a longer next one,
		// netting fewer total tokens.
		if opts.NonGreedy && matchLen >= minNG {
			// Runs of identical bytes compress well on their own; splitting them
			// rarely helps and can hurt, so skip unless SplitRuns is set.
			if opts.SplitRuns || !isRun(e.data[pos:pos+matchLen]) {
				q := pos + matchLen
				aheadLen, aheadCode = e.matchAt(q, end-q)
				aheadPos = q
				choice := matchLen
				// A shorter match has to reach need to be taken. Its total cannot
				// exceed the remaining data, nor shorter plus the longest dictionary
				// string starting where it would, and it can only reach need if the
				// next need-shorter bytes are a dictionary string. Walks that cannot
				// reach need are skipped.
				need := matchLen + aheadLen + minImprove
				for shorter := matchLen - 1; shorter >= 1 && remaining >= need && shorter+e.maxLen >= need; shorter-- {
					q := pos + shorter
					if t := need - shorter; t > int(e.maxLenFrom[e.data[q]]) || (useStrings && !e.hasString(q, t)) {
						continue
					}
					next, nextCode := e.matchAt(q, end-q)
					if shorter+next >= need {
						need = shorter + next + minImprove
						choice = shorter
						aheadPos, aheadLen, aheadCode = q, next, nextCode
					}
				}

				if choice < matchLen {
					matchLen, code = e.matchAt(pos, choice)
					numNongreedy++
				}
			}
		}

		if bs != nil {
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
		if next := pos + matchLen; next < end && e.dictSize < e.maxDict {
			sym := e.data[next]
			if i := e.slot(code, sym); e.trie.dict[i] == 0 {
				e.trie.dict[i] = uint16(e.dictSize)
				e.trie.used = append(e.trie.used, i)
				l := int32(matchLen + 1)
				first := e.data[pos]
				if l > e.maxLenFrom[first] {
					e.maxLenFrom[first] = l
					e.maxLen = max(e.maxLen, int(l))
				}
				if useStrings {
					e.addString(pos, int(l))
				}
				// The new entry extends the lookahead walk if that walk stopped at
				// code for want of exactly this child. It has no children of its
				// own, so the walk cannot get further.
				if aheadPos == next && aheadCode == code && aheadPos+aheadLen < end && e.data[aheadPos+aheadLen] == sym {
					aheadLen++
					aheadCode = e.dictSize
				}
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
			if bs != nil {
				bs.AppendCode(uint(e.clearCode), codeSize)
			}
			numBits += codeSize
			e.initDictionary()
			codeSize = e.minCodeSize + 1
			aheadPos = -1
		case e.dictSize > (1<<codeSize) && codeSize < maxCodeSize:
			// Codes never exceed 12 bits, even as the dictionary keeps filling.
			codeSize++
		}

		// Record ending the block at splitPos. This runs after the dictionary update so
		// codeSize reflects any increment this token triggered: the terminator is
		// emitted after the last real code, where the decoder's width matches the
		// post-increment value.
		if scan != nil {
			splitPos := pos + matchLen
			aligned := false
			if splitPos >= boundary {
				if splitPos == boundary {
					aligned = true
					boundary += alignment
				} else {
					aligned = splitPos%alignment == 0
					boundary = (splitPos/alignment + 1) * alignment
				}
			}
			if aligned || splitPos == n {
				scan.splits = append(scan.splits, split{
					pos:       splitPos,
					bits:      numBits + codeSize,
					tokens:    numTokens,
					nongreedy: numNongreedy,
				})
			}
		}

		pos += matchLen

		if maxTokens > 0 && scan != nil && len(scan.splits) > 0 && numTokens >= maxTokens {
			break
		}
	}
	if scan != nil {
		scan.tokens = numTokens
		scan.reachedEnd = pos == n
	}

	if bs != nil {
		if isFinal {
			bs.AppendCode(uint(e.eosCode), codeSize)
		} else {
			bs.AppendCode(uint(e.clearCode), codeSize)
		}
	}
}
