gifopt
======

Makes GIFs smaller without changing what they look like.

```go
import "samuelks.com/gifopt"

err := gifopt.Optimize(output, input, nil) // io.Writer, io.Reader, *gifopt.Options
```

`Options` sets the optimization `Level` (fast, balanced or thorough, all lossless),
enables the lossy settings, and controls concurrency and progress reporting.
`OptimizeGIF` does the same for a `*gif.GIF` that is already decoded.

Optimization happens in two independent steps, and each can be used on its own:

- **Frame optimization** rewrites the frames so each one stores only what changed:
  smaller rectangles, transparency where the previous frame already has the right
  pixels, one shared color palette, and a disposal method per frame.
- **LZW optimization** compresses the pixel data better than a standard GIF encoder
  by searching for good places to restart the compression dictionary.

`gifopt.Optimize` runs exactly these two steps, each with its own defaults. To use a
setting that `Options` doesn't expose, call the steps directly. `Options.FramesConfig`
and `Options.EncoderOptions` return the settings a level uses, which you can start
from:

```go
import (
    "image/gif"

    "samuelks.com/gifopt/frames"
    "samuelks.com/gifopt/gifenc"
)

g, err := gif.DecodeAll(input)          // any *gif.GIF, decoded or built by hand
err = frames.Optimize(g, frames.Config{})
err = gifenc.EncodeAll(output, g, nil)  // like gif.EncodeAll; nil uses the defaults
```

Or from the command line:

```
go run ./cmd/gifopt -verify animation.gif     # writes animation_opt.gif
go run ./cmd/gifopt -O 3 animation.gif        # slower, about 1% smaller
go run ./cmd/gifdiff a.gif b.gif               # do these two render the same?
```

The lossless guarantee
----------------------

The output of `gifopt.Optimize` always looks exactly like the input. Every other
behavior of the library can change; this one cannot.

The guarantee is checked, not assumed, in two places:

- **At run time.** Before the optimizer accepts a new frame, it draws the frame over
  the current canvas and compares the result to a rendering of the input. If a change
  can't be expressed exactly, because a color is missing from the frame's palette or
  no palette slot is free to mark a pixel transparent, the optimizer leaves the frames
  as they were instead of approximating them.
- **In the tests.** `frames/visual_test.go` optimizes every test fixture and every GIF
  in `frames/testdata/` under six configurations. Each result is encoded, decoded, and
  compared to the input by what it renders. Both encoders are also tested on their own, so
  a failure points to either the optimizer or the encoder.

Two settings can change pixels. Both are off by default, and both have "Lossy" in
their names:

- `frames.Config.LossyForceTransparentColor`: when a frame's palette is full, remaps
  its least-used color so a slot is free for transparency. A few pixels come out a
  nearby color.
- `frames.Config.LossyTransparencyTolerance`: lets a frame skip a pixel when the canvas
  is already within N steps per channel of the color the frame wants. A pixel can be
  off by up to N. The error does not build up over frames, because each frame is
  compared to what the output actually shows, not to the previous frame. The tests
  verify this bound.

Speed and size
--------------

The defaults aim for a small file in a reasonable time. From the command line, `-O`
is the main setting. Each level is a measured trade-off between time and size, and no
level changes what the animation looks like:

| Level | Use | Size compared to `-O 3` | Time |
| --- | --- | --- | --- |
| `-O 0` | Re-encode only, no frame optimization. Useful as a baseline, not for real use. | +51% | 105x faster |
| `-O 1` | Fast. | +12.5% | 18x faster |
| `-O 2` | **Balanced, the default.** | +0.8% | 6x faster |
| `-O 3` | Thorough: the smallest output this encoder produces. | — | — |

These figures are totals over the test corpus. Per file, level 2 is 0.10% to 0.34%
larger than level 3, and level 1 is 1.2% larger on a cartoon and 13% larger on 145 kB
video frames. A coarser search loses more on larger frames, while the time it saves
does not grow with frame size.

Any flag given explicitly overrides the level. In the library, `Options.Level` accepts
levels 1 to 3 as `LevelFast`, `LevelBalanced` and `LevelThorough`; level 0 is the same
as skipping `frames.Optimize`. The finer-grained settings:

| Setting | Effect |
| --- | --- |
| `gifenc.LZWOptions.AlignmentPositions` | How many candidate dictionary restart points the encoder searches per frame. This is the main trade-off between time and size, and it is what `-O` sets. Encoding time is roughly proportional to it. The default of 256 is where returns start to fall off: raising it to 2048 makes the output 0.77% smaller but takes 6x as long, and the size saved per extra second of encoding drops tenfold across that range. |
| `gifenc.LZWOptions.Alignment` | The same setting, expressed as a fixed number of bytes. Prefer `AlignmentPositions`: 512 bytes gives 139 candidates in a 71 kB video frame but only one in a 4 kB cartoon frame, so a byte count can't set a consistent search effort across an animation. |
| `frames.Config.DisableFrameCompressibilityCheck` | The optimizer builds several transparency candidates per frame and compresses each one to find the smallest. Disabling this check saves about 12% of the time but makes output up to 2.8% larger, so it is rarely worth it. |
| `gifenc.Options.Concurrency` | How many frames are LZW-compressed in parallel. Defaults to `runtime.NumCPU()`. Frames are still written in order. |
| `gifenc.LZWOptions.NonGreedy`, `SmartGreedy` | One-step lookahead matching, and an extra greedy pass that keeps it in check. Both are on by default and should be set together. Non-greedy matching saves 0.4–0.9%, but on its own it can compress *worse* than a plain greedy encoder; the greedy pass prevents that. To go faster, turn both off: it is three times faster than leaving them on and costs a measured 0.4%. This is what `-O 1` does. |
| `frames.Config.LossyTransparencyTolerance` | The only lossy setting worth using, and only on GIFs made from video. On flat, cartoon-like animations it has no effect, because their palettes are too coarse for a small tolerance to match anything. Over the corpus, a tolerance of 4 saves about 1% overall (up to 20% on one video-derived file) and 8 saves 16% overall (up to 48%). The change at 4 is invisible; at 8 it is hard to see but no longer negligible. |

For reference, on a local corpus of 18 GIFs (29.4 MB in total, mostly files from the
web that were already optimized), the full pipeline writes 28.6 MB. Writing the same
optimized frames with `image/gif` produces 29.4 MB.

Packages
--------

| Package | What it does |
| --- | --- |
| `gifopt` | The entry point. `Optimize(w, r, opts)` runs the frame optimizer and the encoder in one call, at a chosen `Level`. |
| `frames` | The frame optimizer. `Optimize(g, cfg)` rewrites frames in place; `Scale(g, n)` enlarges every frame by an integer factor. |
| `gifenc` | A GIF encoder with optimized LZW. `EncodeAll(w, g, opts)` and `Encode(w, m, opts)` mirror `image/gif`; `Options` adds the LZW search settings, concurrency and progress reporting. |
| `gifdiff` | Compares two GIFs by what they render, not how they are structured, and merges consecutive frames that look identical. The optimizer's tests use it to check the output. |
| `cmd/gifopt` | Runs the optimizer and the encoder on one or more files and reports how much each step saved. `-verify` checks the result with gifdiff. |
| `cmd/gifdiff` | Reports visual differences between two GIFs. |

Rendering semantics
-------------------

The GIF specification leaves disposal behavior unclear in several places. This library
defines it in one package, `internal/gifcanvas`, and both the optimizer and the diff
tool render through it, so the tool that checks the optimizer always agrees with it on
what a GIF looks like:

- The canvas starts fully transparent, and `GIF.BackgroundIndex` is ignored. This
  matches what browsers do.
- A frame's transparent pixels leave the canvas unchanged. There is no alpha blending;
  each GIF pixel is either transparent or not.
- `DisposalBackground` clears the frame's rectangle to transparent.
- `DisposalPrevious` restores the canvas to how it was before the frame was drawn.

Input GIFs may use any of these disposal methods. The output only uses `DisposalNone`
and `DisposalBackground`.

Attribution
-----------

The LZW encoder in `gifenc` uses techniques from [flexiGIF](https://create.stephan-brumme.com/flexigif-lossless-gif-lzw-optimization/)
by Stephan Brumme. flexiGIF combines two techniques, and its documentation credits both
to earlier work:

- **Flexible dictionary reset.** The GIF89a specification lets an encoder keep using a
  full dictionary instead of clearing it immediately. flexiGIF searches for the best
  places to clear it. Nayuki's [GIF optimizer](https://www.nayuki.io/page/gif-optimizer-java)
  described and implemented this approach earlier.
- **Non-greedy matching.** One-step lookahead, which sometimes takes a shorter match so
  the next one can be longer. It is standard in LZ77 compressors such as gzip.

The frame optimizer in `frames` incorporates some techniques from
[gifsicle](https://github.com/kohler/gifsicle) by Eddie Kohler, in particular the
difference bounds and the two transparency heuristics in its `src/opttemplate.c`.
