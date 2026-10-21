package gifopt_test

import (
	"bytes"
	"image/gif"
	"reflect"
	"strings"
	"testing"

	"samuelks.com/gifopt"
	"samuelks.com/gifopt/frames"
	"samuelks.com/gifopt/gifdiff"
	"samuelks.com/gifopt/gifenc"
	"samuelks.com/gifopt/internal/giftest"
)

func TestOptimizeIsLossless(t *testing.T) {
	for _, f := range giftest.Fixtures() {
		for level := gifopt.LevelDefault; level <= gifopt.LevelThorough; level++ {
			t.Run(f.Name, func(t *testing.T) {
				var in bytes.Buffer
				if err := gif.EncodeAll(&in, f.GIF()); err != nil {
					t.Fatal(err)
				}
				orig, err := gif.DecodeAll(bytes.NewReader(in.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := gifopt.Optimize(&out, bytes.NewReader(in.Bytes()), &gifopt.Options{Level: level}); err != nil {
					t.Fatalf("level %d: %v", level, err)
				}
				got, err := gif.DecodeAll(&out)
				if err != nil {
					t.Fatalf("level %d: decoding the result: %v", level, err)
				}
				if res := gifdiff.Compare(orig, got); res.HasVisualDiff() {
					t.Errorf("level %d changed the animation:\n  %s",
						level, strings.Join(res.VisualMessages(), "\n  "))
				}
			})
		}
	}
}

func TestOptimizeIsTheTwoSteps(t *testing.T) {
	// The default must be exactly frames.Optimize then gifenc.EncodeAll, each with its
	// own defaults, so a caller can switch to the sub-packages without changing the
	// output.
	for _, f := range giftest.Fixtures() {
		t.Run(f.Name, func(t *testing.T) {
			var got bytes.Buffer
			if err := gifopt.OptimizeGIF(&got, f.GIF(), nil); err != nil {
				t.Fatal(err)
			}
			g := f.GIF()
			if err := frames.Optimize(g, frames.Config{}); err != nil {
				t.Fatal(err)
			}
			var want bytes.Buffer
			if err := gifenc.EncodeAll(&want, g, nil); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Errorf("OptimizeGIF wrote %d bytes, the two steps %d", got.Len(), want.Len())
			}
		})
	}
}

func TestLevels(t *testing.T) {
	lzw := func(t *testing.T, level gifopt.Level) gifenc.LZWOptions {
		t.Helper()
		opts, err := gifopt.Options{Level: level}.EncoderOptions()
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		return *opts.LZW
	}

	t.Run("levels are lossless", func(t *testing.T) {
		// The lossy settings are never part of a level: choosing how hard to work must
		// not quietly change what the animation looks like.
		for level := gifopt.LevelDefault; level <= gifopt.LevelThorough; level++ {
			if cfg := (gifopt.Options{Level: level}).FramesConfig(); cfg != (frames.Config{}) {
				t.Errorf("level %d changes the frame settings: %+v", level, cfg)
			}
		}
	})

	t.Run("search breadth increases with the level", func(t *testing.T) {
		for level := gifopt.LevelFast; level < gifopt.LevelThorough; level++ {
			a, b := lzw(t, level).AlignmentPositions, lzw(t, level+1).AlignmentPositions
			if a <= 0 {
				t.Fatalf("level %d leaves the search breadth unset", level)
			}
			if a >= b {
				t.Errorf("level %d searches %d positions, not fewer than level %d's %d",
					level, a, level+1, b)
			}
		}
	})

	t.Run("the default is balanced, which is gifenc's default", func(t *testing.T) {
		// DeepEqual rather than ==: LZWOptions carries a Progress func, which makes it
		// uncomparable. Both are nil here, which DeepEqual accepts.
		want := gifenc.DefaultLZWOptions()
		for _, level := range []gifopt.Level{gifopt.LevelDefault, gifopt.LevelBalanced} {
			if got := lzw(t, level); !reflect.DeepEqual(got, want) {
				t.Errorf("level %d:\n got %+v\nwant %+v", level, got, want)
			}
		}
	})

	t.Run("unknown levels are rejected", func(t *testing.T) {
		for _, level := range []gifopt.Level{-1, gifopt.LevelThorough + 1} {
			if _, err := (gifopt.Options{Level: level}).EncoderOptions(); err == nil {
				t.Errorf("level %d was accepted", level)
			}
		}
	})
}
