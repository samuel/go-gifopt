package frames_test

// This file is the optimizer's correctness gate: every GIF is optimized,
// re-encoded, decoded again and compared against the original on what a viewer
// sees.
//
// Cases with a blocked marker are known failures, and the marker names the cause.
// They are reported as skips, and the test fails if such a case starts passing, so
// the marker gets removed.

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"samuelks.com/gifopt/frames"
	"samuelks.com/gifopt/gifdiff"
	"samuelks.com/gifopt/gifenc"
	"samuelks.com/gifopt/internal/giftest"
)

// allConfigs is the blocked-marker key that applies to every config in the matrix.
// There are no markers left. Adding one should be a deliberate decision, not a way
// to silence a failure.
const allConfigs = "*"

// visualCase is one GIF to run the gate against.
type visualCase struct {
	name string
	why  string
	load func(t *testing.T) *gif.GIF
	// blocked maps a config name to the bug that must be fixed before that config
	// can pass. The key allConfigs applies to every config; an absent entry means
	// the case is expected to pass.
	blocked map[string]string
}

// blockedBy returns what blocks this case under the named config.
func (c visualCase) blockedBy(config string) string {
	if r, ok := c.blocked[config]; ok {
		return r
	}
	return c.blocked[allConfigs]
}

// maxLossyDiffFraction bounds how much of the canvas a lossy setting may change
// before it counts as a bug rather than a tradeoff.
const maxLossyDiffFraction = 0.01

// configs is the matrix of optimizer settings every case runs through. All but the
// two named Lossy must be lossless.
var configs = []struct {
	name string
	cfg  frames.Config
	// lossy marks a setting allowed to change pixels. Visual differences are then
	// bounded by maxLossyDiffFraction rather than failing outright.
	lossy bool
	// tolerance, when set, bounds differences by magnitude instead of by count: no
	// pixel may be off by more than this in any channel. This matches what
	// LossyTransparencyTolerance guarantees; it does not limit how many pixels
	// change.
	tolerance int
}{
	{name: "default", cfg: frames.Config{}},
	{name: "no-transparency", cfg: frames.Config{DisableTransparencyBetweenFrames: true}},
	{name: "no-compress-check", cfg: frames.Config{DisableFrameCompressibilityCheck: true}},
	{name: "no-disposal-selection", cfg: frames.Config{DisableDisposalSelection: true}},
	// Only activates when every palette entry is in use, and then remaps the
	// least-used color's pixels to their nearest neighbor.
	{name: "force-transparent", cfg: frames.Config{LossyForceTransparentColor: true}, lossy: true},
	// Pixels come out up to the tolerance wrong. What must hold is that the error
	// stays bounded by it, never accumulates, and leaves structure and timing
	// untouched.
	{name: "lossy-tolerance", cfg: frames.Config{LossyTransparencyTolerance: 4}, tolerance: 4},
	{name: "all-off", cfg: frames.Config{
		DisableTransparencyBetweenFrames: true,
		DisableFrameCompressibilityCheck: true,
		DisableDisposalSelection:         true,
	}},
}

// syntheticCases returns the in-process fixtures plus their known-failure markers.
func syntheticCases() []visualCase {
	// blocked maps a fixture name to the configs it cannot pass under yet.
	blocked := map[string]map[string]string{}

	var cases []visualCase
	for _, f := range giftest.Fixtures() {
		build := f.GIF
		cases = append(cases, visualCase{
			name:    f.Name,
			why:     f.Why,
			load:    func(*testing.T) *gif.GIF { return build() },
			blocked: blocked[f.Name],
		})
	}
	return cases
}

// corpusBlocked records the known failures for real GIFs. It is empty: every
// committed GIF passes under every config in the matrix.
var corpusBlocked = map[string]map[string]string{}

// corpusFiles are the committed real-world GIFs, each chosen for a quirk of a real
// encoder the synthetic fixtures cannot supply, and each the cheapest file that
// supplies it. Any other GIF dropped into testdata is picked up too, so a larger
// local corpus adds coverage without being required.
var corpusFiles = []string{
	// 4 frames, 120x80: a 32-color global palette plus 3 local palettes of
	// differing sizes, partial frames and existing transparency. Small enough for
	// the exhaustive LZW settings.
	"test_4frame_encoder_test.gif",
	// 6 frames, 500x500, 64-color palette. Exercises DisposalBackground selection
	// under no-compress-check.
	"Deal With It.gif",
	// 83 frames, 300x149, almost all partial frames with transparency: the
	// heaviest test of frame bounds in the corpus.
	"You Win This Round Frank.gif",
	// 185 frames, 176x176, and the only GIF here whose input uses DisposalPrevious.
	"spiderman116_spideydancing1.gif",
	// 8 frames, 500x281, no global palette at all: every frame carries a local
	// color table, and their colors do not fit one palette.
	"R2D2 Burrito.gif",
	// 15 full-canvas frames, 264x200, every one with a distinct 256-color palette
	// and no transparent entry anywhere: the optimizer has to repurpose an index to
	// express transparency, and this is the only file that activates
	// LossyForceTransparentColor.
	"Stop Acting So Stupid.gif",
}

// fileCases returns a case per real GIF found in testdata.
func fileCases(t *testing.T) []visualCase {
	t.Helper()
	seen := map[string]bool{}
	var cases []visualCase

	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		path := filepath.Join("testdata", name)
		cases = append(cases, visualCase{
			name:    name,
			why:     "real-world GIF",
			blocked: corpusBlocked[name],
			load: func(t *testing.T) *gif.GIF {
				t.Helper()
				f, err := os.Open(path)
				if os.IsNotExist(err) {
					t.Skipf("%s not present", path)
				}
				if err != nil {
					t.Fatalf("open %s: %v", path, err)
				}
				defer f.Close() //nolint:errcheck // read-only test fixture; a close error cannot affect the test
				g, err := gif.DecodeAll(f)
				if err != nil {
					t.Fatalf("decode %s: %v", path, err)
				}
				return g
			},
		})
	}

	for _, n := range corpusFiles {
		add(n)
	}
	// Opportunistically include any other GIF sitting in testdata.
	entries, err := os.ReadDir("testdata")
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".gif") {
				add(e.Name())
			}
		}
	}
	return cases
}

// TestOptimizeIsLossless is the gate: optimize, re-encode, and verify the
// animation still renders identically.
func TestOptimizeIsLossless(t *testing.T) {
	for _, c := range syntheticCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Log(c.why)
			for _, cfg := range configs {
				t.Run(cfg.name, func(t *testing.T) {
					runGate(t, c, cfg.name, cfg.cfg, cfg.lossy, cfg.tolerance, encodeGifenc)
				})
			}
		})
	}
}

// TestOptimizeIsLosslessCorpus runs the gate against real GIFs.
//
// Real frames are large and the DP sweep is quadratic in the frame size, so this uses
// a coarse alignment: the LZW encoder still runs end to end on real pixel data,
// without the exhaustive block search. In -short mode the stdlib encoder is used
// instead, which still exercises all of the frame-level optimization.
func TestOptimizeIsLosslessCorpus(t *testing.T) {
	encode := encodeGifencCoarse
	if testing.Short() {
		encode = encodeStdlib
	}
	for _, c := range fileCases(t) {
		t.Run(c.name, func(t *testing.T) {
			for _, cfg := range configs {
				t.Run(cfg.name, func(t *testing.T) {
					runGate(t, c, cfg.name, cfg.cfg, cfg.lossy, cfg.tolerance, encode)
				})
			}
		})
	}
}

// TestEncodersAreLossless checks the encoders on their own, so a gate failure
// can be attributed to the optimizer rather than to encoding.
func TestEncodersAreLossless(t *testing.T) {
	encoders := []struct {
		name   string
		encode func(*gif.GIF) ([]byte, error)
	}{
		{"stdlib", encodeStdlib},
		{"gifenc", encodeGifenc},
	}
	for _, f := range giftest.Fixtures() {
		for _, enc := range encoders {
			t.Run(f.Name+"/"+enc.name, func(t *testing.T) {
				orig := f.GIF()
				data, err := enc.encode(f.GIF())
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				out, err := gif.DecodeAll(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				res := gifdiff.Compare(orig, out)
				if res.HasVisualDiff() {
					t.Errorf("encoding changed the animation:\n  %s",
						strings.Join(res.VisualMessages(), "\n  "))
				}
			})
		}
	}
}

func encodeStdlib(g *gif.GIF) ([]byte, error) {
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeGifenc(g *gif.GIF) ([]byte, error) {
	var buf bytes.Buffer
	if err := gifenc.EncodeAll(&buf, g, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// coarseAlignment keeps the DP sweep to a handful of candidate block starts per
// frame. Correctness does not depend on the alignment, so this only costs
// compression ratio.
const coarseAlignment = 1 << 16

func encodeGifencCoarse(g *gif.GIF) ([]byte, error) {
	opts := gifenc.DefaultLZWOptions()
	opts.Alignment = coarseAlignment
	var buf bytes.Buffer
	if err := gifenc.EncodeAll(&buf, g, &gifenc.Options{LZW: &opts}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// runGate optimizes a GIF, re-encodes it, and reports every way the result differs
// from the original. Failures become skips for cases with a blockedBy marker.
func runGate(t *testing.T, c visualCase, configName string, cfg frames.Config, lossy bool, tolerance int, encode func(*gif.GIF) ([]byte, error)) {
	t.Helper()

	reference := c.load(t)
	subject := c.load(t)

	refFrames := len(reference.Image)
	refDuration := gifdiff.Duration(reference)
	refW, refH := reference.Config.Width, reference.Config.Height
	canvasPixels := max(refW*refH, 1)

	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if err := frames.Optimize(subject, cfg); err != nil {
		fail("Optimize returned an error: %v", err)
	} else {
		problems = append(problems, checkContract(subject, refDuration, refW, refH)...)

		data, err := encode(subject)
		if err != nil {
			fail("re-encoding the optimized GIF failed: %v", err)
		} else {
			out, err := gif.DecodeAll(bytes.NewReader(data))
			if err != nil {
				fail("the optimized GIF did not decode: %v", err)
			} else {
				res := gifdiff.Compare(reference, out)
				switch {
				case tolerance > 0:
					// Structure and timing must be untouched: the tolerance decides
					// which pixels are painted, nothing else.
					if res.SizeA != res.SizeB || res.StatesA != res.StatesB {
						for _, m := range res.VisualMessages() {
							fail("structural change under a tolerance: %s", m)
						}
					}
					worst := 0
					for _, d := range res.Diffs {
						if d.DelayA != d.DelayB {
							fail("tolerance changed timing: state %d shown for %dcs vs %dcs",
								d.Index, d.DelayA, d.DelayB)
						}
						worst = max(worst, d.MaxDelta)
					}
					if worst > tolerance {
						fail("a pixel is off by %d per channel, over the tolerance of %d",
							worst, tolerance)
					} else if worst > 0 {
						t.Logf("pixels off by up to %d of the %d allowed", worst, tolerance)
					}
				case !lossy:
					for _, m := range res.VisualMessages() {
						fail("visual difference: %s", m)
					}
				default:
					// A lossy setting may change pixels, but only a few, and must
					// leave the structure untouched.
					if res.SizeA != res.SizeB || res.StatesA != res.StatesB {
						for _, m := range res.VisualMessages() {
							fail("structural change under a lossy setting: %s", m)
						}
					}
					worst := 0
					for _, d := range res.Diffs {
						if d.DelayA != d.DelayB {
							fail("lossy setting changed timing: state %d shown for %dcs vs %dcs",
								d.Index, d.DelayA, d.DelayB)
						}
						worst = max(worst, d.DiffPixels)
					}
					if frac := float64(worst) / float64(canvasPixels); frac > maxLossyDiffFraction {
						fail("lossy setting changed %d of %d pixels (%.2f%%), over the %.2f%% bound",
							worst, canvasPixels, frac*100, maxLossyDiffFraction*100)
					} else if worst > 0 {
						t.Logf("lossy setting changed up to %d of %d pixels (%.3f%%) — within bounds",
							worst, canvasPixels, frac*100)
					}
				}
			}
		}
	}

	blockedBy := c.blockedBy(configName)
	switch {
	case blockedBy != "" && len(problems) > 0:
		t.Skipf("known failure — %s\n  %s", blockedBy, strings.Join(problems, "\n  "))
	case blockedBy != "":
		t.Fatalf("this case now passes under config %q; remove its blocked marker (%s)",
			configName, blockedBy)
	case len(problems) > 0:
		t.Fatalf("%d frame(s) in, optimizer produced a different animation:\n  %s",
			refFrames, strings.Join(problems, "\n  "))
	}
}

// checkContract verifies the structural promises Optimize makes to its caller,
// independent of how the result renders.
func checkContract(g *gif.GIF, wantDuration, wantW, wantH int) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if len(g.Image) == 0 {
		add("no frames left after optimization")
		return problems
	}
	if len(g.Delay) != len(g.Image) {
		add("len(Delay) = %d but len(Image) = %d; encoders reject this",
			len(g.Delay), len(g.Image))
	}
	if g.Disposal != nil && len(g.Disposal) != len(g.Image) {
		add("len(Disposal) = %d but len(Image) = %d; encoders reject this",
			len(g.Disposal), len(g.Image))
	}
	if got := gifdiff.Duration(g); got != wantDuration {
		add("total duration changed: got %dcs, want %dcs", got, wantDuration)
	}
	if g.Config.Width < wantW || g.Config.Height < wantH {
		add("logical screen shrank: got %dx%d, want at least %dx%d",
			g.Config.Width, g.Config.Height, wantW, wantH)
	}

	canvas := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	for i, im := range g.Image {
		if im.Rect.Empty() {
			add("frame %d is empty; it should have been removed", i)
		}
		if !im.Rect.In(canvas) {
			add("frame %d bounds %v fall outside the canvas %v; encoders reject this",
				i, im.Rect, canvas)
		}
		if len(im.Palette) == 0 {
			add("frame %d has an empty palette; encoders reject this", i)
		}
	}
	return problems
}
