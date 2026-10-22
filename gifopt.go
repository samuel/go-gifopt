// Package gifopt makes GIFs smaller without changing what they look like.
//
// Optimize does everything in one call. It runs two steps that can also be used
// separately: package frames rebuilds the frames so each stores only what changed,
// and package gifenc encodes the result with better LZW compression than a standard
// encoder.
package gifopt

import (
	"fmt"
	"image/gif"
	"io"

	"samuelks.com/gifopt/frames"
	"samuelks.com/gifopt/gifenc"
)

// Level trades encoding time for size. Every level is lossless.
//
// What varies is how many candidate block starts the LZW search sweeps per frame, and
// whether it uses non-greedy matching. Measured over the corpus against
// LevelThorough: LevelBalanced costs 0.78% for 5x less time, and LevelFast costs 12.7%
// for 11x less, from 1.7% to 64% per file.
type Level int

const (
	// LevelDefault is LevelBalanced.
	LevelDefault Level = iota
	// LevelFast is the least search that still compresses sensibly.
	LevelFast
	// LevelBalanced is the knee of the time/size curve, and gifenc's defaults.
	LevelBalanced
	// LevelThorough takes much longer for the last fraction of a percent.
	LevelThorough
)

// Options controls Optimize. The zero value is lossless and uses LevelBalanced.
type Options struct {
	Level Level

	// LossyTransparencyTolerance and LossyForceTransparentColor are the only
	// settings that change what a viewer sees. They are off by default and never
	// part of a Level; see frames.Config for what each permits.
	LossyTransparencyTolerance int
	LossyForceTransparentColor bool

	// Concurrency is the number of goroutines used for parallel LZW compression.
	// 0 or negative defaults to runtime.NumCPU().
	Concurrency int

	// Progress is an optional callback invoked while encoding.
	Progress func(gifenc.Progress)
}

// FramesConfig returns the frame optimization settings Optimize uses.
func (o Options) FramesConfig() frames.Config {
	return frames.Config{
		LossyTransparencyTolerance: o.LossyTransparencyTolerance,
		LossyForceTransparentColor: o.LossyForceTransparentColor,
	}
}

// EncoderOptions returns the encoder settings Optimize uses. It fails on an unknown
// Level.
func (o Options) EncoderOptions() (gifenc.Options, error) {
	lzw := gifenc.DefaultLZWOptions()
	switch o.Level {
	case LevelDefault, LevelBalanced:
	case LevelFast:
		lzw = withSearch(lzw, 16, false)
	case LevelThorough:
		lzw = withSearch(lzw, 2048, true)
	default:
		return gifenc.Options{}, fmt.Errorf("gifopt: unknown level %d, want %d to %d",
			o.Level, LevelDefault, LevelThorough)
	}
	return gifenc.Options{
		LZW:         &lzw,
		Concurrency: o.Concurrency,
		Progress:    o.Progress,
	}, nil
}

// withSearch returns opts with the given search breadth, turning NonGreedy and
// SmartGreedy on or off together: non-greedy alone can compress worse than plain
// greedy, and turning the pair off is three times faster for about 0.7% in size.
func withSearch(opts gifenc.LZWOptions, positions int, nonGreedy bool) gifenc.LZWOptions {
	opts.AlignmentPositions = positions
	opts.NonGreedy = nonGreedy
	opts.SmartGreedy = nonGreedy
	return opts
}

// Optimize decodes a GIF from r and writes a smaller one that renders the same to w.
// A nil opts uses the defaults.
func Optimize(w io.Writer, r io.Reader, opts *Options) error {
	g, err := gif.DecodeAll(r)
	if err != nil {
		return fmt.Errorf("gifopt: decoding: %w", err)
	}
	return OptimizeGIF(w, g, opts)
}

// OptimizeGIF is Optimize for a GIF that is already decoded. It rewrites g's frames
// in place.
func OptimizeGIF(w io.Writer, g *gif.GIF, opts *Options) error {
	var o Options
	if opts != nil {
		o = *opts
	}
	encOpts, err := o.EncoderOptions()
	if err != nil {
		return err
	}
	if err := frames.Optimize(g, o.FramesConfig()); err != nil {
		return err
	}
	return gifenc.EncodeAll(w, g, &encOpts)
}
