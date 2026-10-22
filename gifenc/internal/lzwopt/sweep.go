package lzwopt

// split is a place a swept block could end: the block from the swept position to pos
// costs bits, terminator included.
type split struct {
	pos       int
	bits      int
	tokens    int
	nongreedy int
}

// scanResult is what scanning one candidate block records. Scanning walks the
// dictionary, which is nearly all the work of the sweep, and depends only on the data,
// so blocks can be scanned in any order and on any goroutine. Only resolve, which
// prices the splits against the blocks after them, has to run in sweep order.
type scanResult struct {
	splits []split
	// tokens is how many tokens were scanned, and reachedEnd whether they reached the
	// end of the data.
	tokens     int
	reachedEnd bool
}

// covers reports whether the scan went as far as a scan bounded at limit tokens would
// have. Such a scan stops at the first token at or past limit that follows a split.
func (r *scanResult) covers(limit int) bool {
	return r.reachedEnd || max(r.splits[0].tokens, limit) <= r.tokens
}

type bestBlock struct {
	length    int
	bits      int
	totalBits int64
	tokens    int
	nongreedy int
}

// resolve updates best for the block scanned from from, considering the splits a scan
// bounded at limit tokens would have recorded.
func resolve(best []bestBlock, from, alignment int, r *scanResult, limit int) {
	fromIdx := from / alignment
	last := max(r.splits[0].tokens, limit)
	for _, s := range r.splits {
		if s.tokens > last {
			break
		}
		// Every block after this one has been resolved already, since the sweep runs
		// right to left. At the end of the data, nextIdx is past the last swept
		// position, whose zero entry is the base case.
		nextIdx := (s.pos + alignment - 1) / alignment
		// Only bits are counted: sub-block length bytes depend only on the total bits,
		// so they cannot change which split wins.
		cost := int64(s.bits) + best[nextIdx].totalBits
		if best[fromIdx].totalBits == 0 || cost < best[fromIdx].totalBits {
			best[fromIdx] = bestBlock{
				length:    s.pos - from,
				bits:      s.bits,
				totalBits: cost,
				tokens:    s.tokens,
				nongreedy: s.nongreedy,
			}
		}
	}
}

// Workers bounds how many goroutines search at once across any number of concurrent
// Encode calls. Each call counts as one while it runs, and borrows idle workers to
// scan its sweep ahead of itself. While every worker is busy with a call of its own,
// as when there are more frames to encode than workers, nothing is borrowed and each
// sweep runs on its own goroutine; once some fall idle, as when the last frames are
// being encoded, the remaining calls use them.
//
// The output does not depend on whether Workers is used or how many workers it has.
type Workers struct {
	tokens chan struct{}
}

// NewWorkers returns Workers allowing n searches at once, at least 1.
func NewWorkers(n int) *Workers {
	return &Workers{tokens: make(chan struct{}, max(n, 1))}
}

func (w *Workers) acquire() { w.tokens <- struct{}{} }

func (w *Workers) tryAcquire() bool {
	select {
	case w.tokens <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *Workers) release() { <-w.tokens }

// sweepJob is a block scan handed to a borrowed worker.
type sweepJob struct {
	result   scanResult
	done     chan struct{}
	borrowed bool
}

// optimizeAndMerge runs the right-to-left DP sweep to find the optimal split points,
// then encodes the data in one forward pass. The sweep borrows idle workers from w
// when it is non-nil.
func (e *lzwEncoder) optimizeAndMerge(opts Options, w *Workers) *BitStream {
	n := len(e.data)

	alignment := resolveAlignment(opts.Alignment, opts.AlignmentPositions, n)

	best := make([]bestBlock, n/alignment+2)

	startPos := (n / alignment) * alignment
	if startPos == n {
		startPos -= alignment
	}
	if startPos < 0 {
		startPos = 0
	}
	positions := startPos/alignment + 1

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
	// Job k scans the block at position k%positions, counted from the right, in sweep
	// k/positions. The second sweep is pure greedy, and overwrites best[i] where greedy
	// wins: non-greedy matching can pollute the dictionary with duplicate entries and
	// come out larger.
	total := sweeps * positions
	jobPos := func(k int) int { return startPos - (k%positions)*alignment }
	jobOpts := func(k int) Options {
		o := sweepOpts
		o.NonGreedy = opts.NonGreedy && k/positions == 0
		return o
	}

	// Borrowed workers scan ahead of resolution, each bounded by the token limit when
	// it was handed out. The limit only grows, so a scan that stopped short of the
	// limit it is resolved under is redone here; that is rare, because the limit grows
	// slowly.
	var jobs []sweepJob
	if w != nil {
		jobs = make([]sweepJob, cap(w.tokens))
		for i := range jobs {
			jobs[i].done = make(chan struct{}, 1)
		}
	}
	borrow := func(k, limit int) {
		j := &jobs[k%len(jobs)]
		j.borrowed = true
		pos, o := jobPos(k), jobOpts(k)
		go func() {
			b := newLZWEncoder(e.data, e.minCodeSize, e.maxDict)
			b.prefix = e.prefix
			b.encodeBlock(pos, 0, nil, true, o, &j.result, alignment, limit)
			b.release()
			w.release()
			j.done <- struct{}{}
		}()
	}

	var inline scanResult
	// started is the first job neither scanned here nor handed out.
	started := 0
	for k := range total {
		pos := jobPos(k)
		started = max(started, k+1)
		if w != nil {
			// The slot the last job handed out reuses was resolved before this one.
			for ; started < min(total, k+len(jobs)) && w.tryAcquire(); started++ {
				borrow(started, tokenLimit)
			}
		}
		r := &inline
		if w != nil && jobs[k%len(jobs)].borrowed {
			j := &jobs[k%len(jobs)]
			j.borrowed = false
			<-j.done
			r = &j.result
		}
		if r == &inline || !r.covers(tokenLimit) {
			e.encodeBlock(pos, 0, nil, true, jobOpts(k), &inline, alignment, tokenLimit)
			r = &inline
		}
		resolve(best, pos, alignment, r, tokenLimit)
		tokenLimit = max(tokenLimit, 2*best[pos/alignment].tokens)
		if opts.Progress != nil {
			// Spread across all sweeps so progress only increases and reaches the
			// total after the last one.
			sweep := k / positions
			opts.Progress((sweep*n+(n-pos))/sweeps, n)
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
		e.encodeBlock(pos, length, bs, isFinal, encOpts, nil, 0, 0)
		pos += length
	}

	return bs
}
