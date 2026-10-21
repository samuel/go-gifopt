// Package gifdiff compares two GIFs on what they render over time rather than on how
// they are structured. Each GIF is composited to a sequence of canvases, and
// consecutive identical canvases are coalesced into one state whose duration is the sum
// of their delays.
//
// Two GIFs are visually equal when their coalesced states hold the same canvases in
// the same order for the same durations, regardless of frame count, frame bounds,
// palettes or disposal methods. This makes it suitable for checking an optimizer that
// merges frames.
package gifdiff

import (
	"fmt"
	"image"
	"image/gif"

	"samuelks.com/gifopt/internal/gifcanvas"
)

// State is one visually distinct step of an animation: a fully composited
// canvas together with how long it is displayed.
type State struct {
	// Canvas is the composited image a viewer sees during this state.
	Canvas *image.RGBA
	// Delay is how long the state is displayed, in hundredths of a second,
	// summed over every source frame coalesced into it.
	Delay int
	// Frames are the indices of the source frames coalesced into this state.
	Frames []int
}

// Render composites g and returns its visually distinct states in order.
// Consecutive frames that render to identical canvases are coalesced.
func Render(g *gif.GIF) []State {
	frames := gifcanvas.RenderFrames(g)
	var states []State
	var prev *gifcanvas.Canvas

	for i, c := range frames {
		delay := 0
		if i < len(g.Delay) {
			delay = g.Delay[i]
		}
		if prev != nil && prev.Equal(c) {
			last := &states[len(states)-1]
			last.Delay += delay
			last.Frames = append(last.Frames, i)
			continue
		}
		states = append(states, State{Canvas: c.RGBA, Delay: delay, Frames: []int{i}})
		prev = c
	}

	return states
}

// Duration returns the total playing time of one loop of the animation, in
// hundredths of a second.
func Duration(g *gif.GIF) int {
	total := 0
	for _, d := range g.Delay {
		total += d
	}
	return total
}

// StateDiff describes a difference at one step of the animation.
type StateDiff struct {
	// Index is the position in the coalesced state sequence.
	Index int
	// DiffPixels is the number of differing pixels, or 0 when only the
	// duration differs.
	DiffPixels int
	// MaxDelta is the largest per-channel difference across the state's pixels. It
	// separates a state that is slightly the wrong shade from one showing the wrong
	// thing.
	MaxDelta int
	// DelayA and DelayB are the durations of the state in each GIF.
	DelayA, DelayB int
	// FramesA and FramesB are the source frame indices behind each state.
	FramesA, FramesB []int
}

// Result is the outcome of comparing two GIFs.
type Result struct {
	// SizeA and SizeB are the logical screen dimensions.
	SizeA, SizeB image.Point
	// StatesA and StatesB are the number of visually distinct states.
	StatesA, StatesB int
	// FramesA and FramesB are the raw frame counts.
	FramesA, FramesB int
	// DurationA and DurationB are the total playing times, in hundredths of a
	// second.
	DurationA, DurationB int
	// LoopCountA and LoopCountB are the GIF loop counts.
	LoopCountA, LoopCountB int
	// Diffs holds the per-state differences, in state order.
	Diffs []StateDiff
}

// HasVisualDiff reports whether the two GIFs render differently. Canvas size, state
// count, per-state pixels and per-state duration are visual; raw frame count,
// per-frame delay and loop count are not.
func (r Result) HasVisualDiff() bool {
	return r.SizeA != r.SizeB || r.StatesA != r.StatesB || len(r.Diffs) > 0
}

// VisualMessages returns one human-readable line per visual difference.
func (r Result) VisualMessages() []string {
	var msgs []string
	if r.SizeA != r.SizeB {
		msgs = append(msgs, fmt.Sprintf("canvas size %dx%d vs %dx%d",
			r.SizeA.X, r.SizeA.Y, r.SizeB.X, r.SizeB.Y))
	}
	if r.StatesA != r.StatesB {
		msgs = append(msgs, fmt.Sprintf("visually distinct states %d vs %d",
			r.StatesA, r.StatesB))
	}
	for _, d := range r.Diffs {
		switch {
		case d.DiffPixels > 0 && d.DelayA != d.DelayB:
			msgs = append(msgs, fmt.Sprintf(
				"state %d: %d differing pixel(s), up to %d per channel, shown for %dcs vs %dcs (source frames %v vs %v)",
				d.Index, d.DiffPixels, d.MaxDelta, d.DelayA, d.DelayB, d.FramesA, d.FramesB))
		case d.DiffPixels > 0:
			msgs = append(msgs, fmt.Sprintf(
				"state %d: %d differing pixel(s), up to %d per channel (source frames %v vs %v)",
				d.Index, d.DiffPixels, d.MaxDelta, d.FramesA, d.FramesB))
		default:
			msgs = append(msgs, fmt.Sprintf(
				"state %d: shown for %dcs vs %dcs (source frames %v vs %v)",
				d.Index, d.DelayA, d.DelayB, d.FramesA, d.FramesB))
		}
	}
	return msgs
}

// InfoMessages returns one line per non-visual difference: structural changes
// that do not alter what a viewer sees.
func (r Result) InfoMessages() []string {
	var msgs []string
	if r.FramesA != r.FramesB {
		msgs = append(msgs, fmt.Sprintf("frame count %d vs %d (%d visually distinct state(s))",
			r.FramesA, r.FramesB, r.StatesA))
	}
	if r.LoopCountA != r.LoopCountB {
		msgs = append(msgs, fmt.Sprintf("loop count %d vs %d", r.LoopCountA, r.LoopCountB))
	}
	return msgs
}

// Compare renders both GIFs and reports their differences.
func Compare(a, b *gif.GIF) Result {
	wA, hA := gifcanvas.Size(a)
	wB, hB := gifcanvas.Size(b)

	statesA := Render(a)
	statesB := Render(b)

	res := Result{
		SizeA:      image.Point{X: wA, Y: hA},
		SizeB:      image.Point{X: wB, Y: hB},
		StatesA:    len(statesA),
		StatesB:    len(statesB),
		FramesA:    len(a.Image),
		FramesB:    len(b.Image),
		DurationA:  Duration(a),
		DurationB:  Duration(b),
		LoopCountA: a.LoopCount,
		LoopCountB: b.LoopCount,
	}

	// Compare only the states both have. A count mismatch is reported through
	// StatesA and StatesB.
	for i := range min(len(statesA), len(statesB)) {
		sa, sb := statesA[i], statesB[i]
		ca := &gifcanvas.Canvas{RGBA: sa.Canvas}
		cb := &gifcanvas.Canvas{RGBA: sb.Canvas}
		diffPx := ca.CountDiff(cb)
		if diffPx == 0 && sa.Delay == sb.Delay {
			continue
		}
		res.Diffs = append(res.Diffs, StateDiff{
			Index:      i,
			DiffPixels: diffPx,
			MaxDelta:   ca.MaxChannelDiff(cb),
			DelayA:     sa.Delay,
			DelayB:     sb.Delay,
			FramesA:    sa.Frames,
			FramesB:    sb.Frames,
		})
	}

	return res
}
