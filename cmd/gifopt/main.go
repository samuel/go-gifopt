// Command gifopt makes GIF files smaller without changing what they look like,
// reporting how much frame optimization and LZW optimization each saved.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image/gif"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"samuelks.com/gifopt"
	"samuelks.com/gifopt/frames"
	"samuelks.com/gifopt/gifdiff"
	"samuelks.com/gifopt/gifenc"
)

// -O 0 is a CLI-only baseline; 1 to 3 are the library's levels.
const (
	levelRepack   = 0 // no frame optimization, minimum search
	levelDefault  = int(gifopt.LevelBalanced)
	levelThorough = int(gifopt.LevelThorough)
)

// settingsForLevel maps -O to settings. Level 0 re-encodes the frames unchanged, as a
// baseline for measuring the frame optimizer. Levels 1 to 3 are gifopt's levels.
func settingsForLevel(level int) (optimizeFrames bool, lzwOpts gifenc.LZWOptions, err error) {
	if level == levelRepack {
		lzwOpts = gifenc.DefaultLZWOptions()
		lzwOpts.AlignmentPositions = 1
		lzwOpts.NonGreedy = false
		lzwOpts.SmartGreedy = false
		return false, lzwOpts, nil
	}
	if level < levelRepack || level > levelThorough {
		return false, lzwOpts, fmt.Errorf("unknown optimization level %d, want %d to %d",
			level, levelRepack, levelThorough)
	}
	encOpts, err := gifopt.Options{Level: gifopt.Level(level)}.EncoderOptions()
	if err != nil {
		return false, lzwOpts, err
	}
	return true, *encOpts.LZW, nil
}

// resolvedConcurrency mirrors how gifenc.EncodeAll reads the option.
func resolvedConcurrency(c int) int {
	if c <= 0 {
		return runtime.NumCPU()
	}
	return c
}

func main() {
	level := flag.Int("O", levelDefault,
		"optimization level: 0=re-encode only, 1=fast, 2=balanced, 3=thorough. "+
			"Compared to level 3, output is typically about 0.8% larger at level 2 (6x faster), "+
			"12% larger at level 1 (18x faster) and 51% larger at level 0. "+
			"Flags set explicitly override the level")

	// Frame optimization flags
	disableCompressCheck := flag.Bool("disable-compress-check", false,
		"don't compress each transparency candidate to pick the smallest (faster, larger output)")
	disableTransparency := flag.Bool("disable-transparency", false,
		"don't make pixels that are unchanged from the previous frame transparent")
	disableDisposalSelection := flag.Bool("disable-disposal-selection", false,
		"don't try clearing the previous frame (DisposalBackground) to save space; clears the animation needs are still used")
	lossyForceTransparent := flag.Bool("lossy-force-transparent", false,
		"LOSSY: when a frame uses all 256 palette entries, free one for transparency by remapping the least-used color to the nearest other color")
	lossyTolerance := flag.Int("lossy-transparency-tolerance", 0,
		"LOSSY: leave a pixel unpainted when the canvas already shows a color within this many steps per channel (0=off). "+
			"Mainly helps GIFs made from video: 4 is hard to notice, 8 saves much more but is visible side by side")

	// LZW flags. The defaults are the library's, so running with no flags matches
	// what gifenc.EncodeAll would produce.
	defLZW := gifenc.DefaultLZWOptions()
	lzwAlignment := flag.Int("lzw-alignment", defLZW.Alignment,
		"block search granularity in bytes (0=use -lzw-alignment-positions; 1=every byte, smallest output "+
			"but much slower on large frames). Prefer -lzw-alignment-positions, which searches every frame "+
			"with the same effort")
	lzwAlignmentPositions := flag.Int("lzw-alignment-positions", defLZW.AlignmentPositions,
		"number of candidate block starts to search per frame, regardless of frame size (0=library default). "+
			"Encoding time is roughly proportional to it")
	lzwMaxDict := flag.Int("lzw-max-dict", defLZW.MaxDictionary,
		"clear the LZW dictionary when it reaches this size (0=4096, the GIF maximum)")
	lzwNonGreedy := flag.Bool("lzw-non-greedy", defLZW.NonGreedy,
		"use one-step lookahead matching, for smaller output at some cost in speed")
	lzwMinNonGreedyMatch := flag.Int("lzw-min-non-greedy-match", defLZW.MinNonGreedyMatch,
		"minimum match length considered for non-greedy search")
	lzwMinImprovement := flag.Int("lzw-min-improvement", defLZW.MinImprovement,
		"minimum extra bytes saved to accept a non-greedy match")
	lzwSplitRuns := flag.Bool("lzw-split-runs", defLZW.SplitRuns,
		"allow non-greedy matching to split long runs of the same byte")
	lzwSmartGreedy := flag.Bool("lzw-smart-greedy", defLZW.SmartGreedy,
		"also run a greedy sweep and keep the better result per block, so non-greedy matching can't do worse than greedy")
	lzwNoClearCode := flag.Bool("lzw-no-clear-code", !defLZW.StartWithClearCode,
		"omit the initial LZW clear code (not recommended; most decoders require it)")

	concurrency := flag.Int("concurrency", 0,
		"number of parallel LZW workers (0 = runtime.NumCPU(), 1 = sequential)")

	verify := flag.Bool("verify", false,
		"compare the result's rendering against the input and fail on any visual difference "+
			"(with -lossy-transparency-tolerance, on any pixel off by more than the tolerance)")

	output := flag.String("o", "", "output file (default: <input>_opt.gif; use - for stdout)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: gifopt [options] <input.gif> [...]\n\n")
		fmt.Fprintf(os.Stderr, "Makes GIFs smaller without changing how they look, by optimizing frames\n")
		fmt.Fprintf(os.Stderr, "and LZW compression.\n\n")
		fmt.Fprintf(os.Stderr, "Start with -O. All levels are lossless. Only the -lossy-* flags change what\n")
		fmt.Fprintf(os.Stderr, "a viewer sees, and they work with any level.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}
	if len(args) > 1 && *output != "" {
		fmt.Fprintln(os.Stderr, "error: -o may not be used with multiple input files")
		os.Exit(1)
	}

	var animCfg frames.Config
	optimizeFrames, lzwOpts, err := settingsForLevel(*level)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Explicitly set flags override the level. flag.Visit visits only flags that were
	// set.
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "disable-compress-check":
			animCfg.DisableFrameCompressibilityCheck = *disableCompressCheck
		case "disable-transparency":
			animCfg.DisableTransparencyBetweenFrames = *disableTransparency
		case "disable-disposal-selection":
			animCfg.DisableDisposalSelection = *disableDisposalSelection
		case "lossy-force-transparent":
			animCfg.LossyForceTransparentColor = *lossyForceTransparent
		case "lossy-transparency-tolerance":
			animCfg.LossyTransparencyTolerance = *lossyTolerance
		case "lzw-alignment":
			lzwOpts.Alignment = *lzwAlignment
		case "lzw-alignment-positions":
			lzwOpts.AlignmentPositions = *lzwAlignmentPositions
		case "lzw-max-dict":
			lzwOpts.MaxDictionary = *lzwMaxDict
		case "lzw-non-greedy":
			lzwOpts.NonGreedy = *lzwNonGreedy
		case "lzw-min-non-greedy-match":
			lzwOpts.MinNonGreedyMatch = *lzwMinNonGreedyMatch
		case "lzw-min-improvement":
			lzwOpts.MinImprovement = *lzwMinImprovement
		case "lzw-split-runs":
			lzwOpts.SplitRuns = *lzwSplitRuns
		case "lzw-smart-greedy":
			lzwOpts.SmartGreedy = *lzwSmartGreedy
		case "lzw-no-clear-code":
			lzwOpts.StartWithClearCode = !*lzwNoClearCode
		}
	})

	exitCode := 0
	for _, inputPath := range args {
		outPath := *output
		if outPath == "" {
			ext := filepath.Ext(inputPath)
			base := strings.TrimSuffix(inputPath, ext)
			outPath = base + "_opt" + ext
		}
		if err := processFile(inputPath, outPath, animCfg, lzwOpts, *concurrency, *verify, optimizeFrames); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

func processFile(inputPath, outPath string, animCfg frames.Config, lzwOpts gifenc.LZWOptions, concurrency int, verify, optimizeFrames bool) error {
	fmt.Printf("Input:  %s\n", inputPath)

	inputData, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", inputPath, err)
	}
	originalSize := len(inputData)

	g, err := gif.DecodeAll(bytes.NewReader(inputData))
	if err != nil {
		return fmt.Errorf("decoding %s: %w", inputPath, err)
	}
	fmt.Printf("        %s  |  %d frame(s)  |  %dx%d\n",
		fmtBytes(originalSize), len(g.Image), g.Config.Width, g.Config.Height)

	// Decode a separate copy, since optimization modifies g in place.
	var reference *gif.GIF
	if verify {
		reference, err = gif.DecodeAll(bytes.NewReader(inputData))
		if err != nil {
			return fmt.Errorf("decoding %s: %w", inputPath, err)
		}
	}

	fmt.Printf("\n[1/2] Frame optimization\n")

	// Measure against the unoptimized frames written by the same encoder. The input
	// file's size reflects whatever encoder produced it.
	baselineSize, err := stdlibSize(g)
	if err != nil {
		return fmt.Errorf("encoding before frame optimization: %w", err)
	}

	if !optimizeFrames {
		fmt.Printf("      Skipped at this optimization level; the frames are re-encoded as they came in\n")
	} else {
		framesBefore := len(g.Image)
		if err := frames.Optimize(g, animCfg); err != nil {
			return fmt.Errorf("frame optimization: %w", err)
		}
		framesAfter := len(g.Image)
		if dropped := framesBefore - framesAfter; dropped > 0 {
			fmt.Printf("      Frames: %d → %d (%d dropped as redundant)\n", framesBefore, framesAfter, dropped)
		} else {
			fmt.Printf("      Frames: %d (none redundant)\n", framesAfter)
		}
	}

	framesSize, err := stdlibSize(g)
	if err != nil {
		return fmt.Errorf("encoding after frame optimization: %w", err)
	}
	framesDelta := baselineSize - framesSize

	fmt.Printf("      Size (standard LZW):  %s, from %s unoptimized\n",
		fmtBytes(framesSize), fmtBytes(baselineSize))
	if framesDelta >= 0 {
		fmt.Printf("      Saved by frame opt:   %s  (%s)\n",
			fmtBytes(framesDelta), fmtPct(framesDelta, baselineSize))
	} else {
		fmt.Printf("      Delta from frame opt: +%s  (%s — the frames compress worse this way)\n",
			fmtBytes(-framesDelta), fmtPct(-framesDelta, baselineSize))
	}

	workers := resolvedConcurrency(concurrency)
	if workers == 1 {
		fmt.Printf("\n[2/2] LZW optimization (sequential)\n")
	} else {
		fmt.Printf("\n[2/2] LZW optimization (%d workers)\n", workers)
	}

	nFrames := len(g.Image)
	var lastProgress string
	encOpts := gifenc.Options{
		LZW:         &lzwOpts,
		Concurrency: concurrency,
		Progress: func(p gifenc.Progress) {
			pct := 0.0
			if p.BytesTotal > 0 {
				pct = 100.0 * float64(p.BytesDone) / float64(p.BytesTotal)
			}
			s := fmt.Sprintf("\r      Encoding: %d/%d frames  %3.1f%%   ", p.FramesDone, nFrames, pct)
			if s != lastProgress {
				lastProgress = s
				fmt.Print(s)
			}
		},
	}

	var outBuf bytes.Buffer
	if err := gifenc.EncodeAll(&outBuf, g, &encOpts); err != nil {
		return fmt.Errorf("LZW encoding: %w", err)
	}
	fmt.Printf("\r                                        \r") // clear progress line
	finalSize := outBuf.Len()
	lzwDelta := framesSize - finalSize

	fmt.Printf("      Final size:             %s\n", fmtBytes(finalSize))
	if lzwDelta >= 0 {
		fmt.Printf("      Saved by LZW opt:      %s  (%s of intermediate)\n",
			fmtBytes(lzwDelta), fmtPct(lzwDelta, framesSize))
	} else {
		fmt.Printf("      Delta from LZW opt:    +%s  (no LZW improvement achieved)\n",
			fmtBytes(-lzwDelta))
	}

	totalDelta := originalSize - finalSize
	fmt.Printf("\n─────────────────────────────────────────\n")
	fmt.Printf("  Original:    %s\n", fmtBytes(originalSize))
	fmt.Printf("  Final:       %s\n", fmtBytes(finalSize))
	if totalDelta >= 0 {
		fmt.Printf("  Total saved: %s  (%s)\n", fmtBytes(totalDelta), fmtPct(totalDelta, originalSize))
	} else {
		fmt.Printf("  Total delta: +%s  (output is larger than input)\n", fmtBytes(-totalDelta))
	}
	fmt.Printf("    frame opt: %s  (%s of the unoptimized baseline)\n",
		fmtSignedBytes(framesDelta), fmtPct(framesDelta, baselineSize))
	fmt.Printf("    LZW opt:   %s  (%s of intermediate)\n", fmtSignedBytes(lzwDelta), fmtPct(lzwDelta, framesSize))
	fmt.Printf("─────────────────────────────────────────\n")

	if verify {
		out, err := gif.DecodeAll(bytes.NewReader(outBuf.Bytes()))
		if err != nil {
			return fmt.Errorf("verifying: the result did not decode: %w", err)
		}
		res := gifdiff.Compare(reference, out)
		if tol := animCfg.LossyTransparencyTolerance; tol > 0 {
			// Pixels may differ, so check that none is off by more than the tolerance
			// and that structure and timing are unchanged.
			if res.SizeA != res.SizeB || res.StatesA != res.StatesB {
				return fmt.Errorf("verifying: the tolerance changed the animation's structure:\n  %s",
					strings.Join(res.VisualMessages(), "\n  "))
			}
			worst := 0
			for _, d := range res.Diffs {
				if d.DelayA != d.DelayB {
					return fmt.Errorf("verifying: the tolerance changed the timing of state %d: %dcs vs %dcs",
						d.Index, d.DelayA, d.DelayB)
				}
				worst = max(worst, d.MaxDelta)
			}
			if worst > tol {
				return fmt.Errorf("verifying: a pixel is off by %d per channel, over the tolerance of %d",
					worst, tol)
			}
			fmt.Printf("  Verified:    pixels off by up to %d of the %d allowed, structure and timing unchanged\n",
				worst, tol)
		} else if res.HasVisualDiff() {
			return fmt.Errorf("verifying: the result renders differently:\n  %s",
				strings.Join(res.VisualMessages(), "\n  "))
		} else {
			fmt.Printf("  Verified:    no visual differences\n")
		}
	}

	if outPath == "-" {
		if _, err := os.Stdout.Write(outBuf.Bytes()); err != nil {
			return fmt.Errorf("writing to stdout: %w", err)
		}
	} else {
		if err := os.WriteFile(outPath, outBuf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", outPath, err)
		}
		fmt.Printf("Output: %s\n\n", outPath)
	}
	return nil
}

// stdlibSize returns the size of g when written by image/gif, the baseline for
// measuring frame optimization.
func stdlibSize(g *gif.GIF) (int, error) {
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		return 0, err
	}
	return buf.Len(), nil
}

func fmtBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB (%d B)", float64(n)/1024, n)
	default:
		return fmt.Sprintf("%.2f MB (%d B)", float64(n)/(1024*1024), n)
	}
}

func fmtSignedBytes(n int) string {
	if n < 0 {
		return "+" + fmtBytes(-n)
	}
	return "-" + fmtBytes(n)
}

func fmtPct(saved, total int) string {
	if total == 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(saved)/float64(total))
}
