package main

import (
	"reflect"
	"testing"

	"samuelks.com/gifopt"
)

func TestSettingsForLevel(t *testing.T) {
	t.Run("level 0 skips frame optimization", func(t *testing.T) {
		optimize, lzw, err := settingsForLevel(levelRepack)
		if err != nil {
			t.Fatal(err)
		}
		if optimize {
			t.Error("level 0 must not run frame optimization")
		}
		_, fast, err := settingsForLevel(int(gifopt.LevelFast))
		if err != nil {
			t.Fatal(err)
		}
		if lzw.AlignmentPositions >= fast.AlignmentPositions {
			t.Errorf("level 0 searches %d positions, not fewer than level 1's %d",
				lzw.AlignmentPositions, fast.AlignmentPositions)
		}
	})

	t.Run("levels 1 to 3 are the library's", func(t *testing.T) {
		for level := levelRepack + 1; level <= levelThorough; level++ {
			optimize, lzw, err := settingsForLevel(level)
			if err != nil {
				t.Fatalf("level %d: %v", level, err)
			}
			if !optimize {
				t.Errorf("level %d must run frame optimization", level)
			}
			want, err := gifopt.Options{Level: gifopt.Level(level)}.EncoderOptions()
			if err != nil {
				t.Fatal(err)
			}
			// DeepEqual rather than ==: LZWOptions carries a Progress func, which makes
			// it uncomparable. Both are nil here, which DeepEqual accepts.
			if !reflect.DeepEqual(lzw, *want.LZW) {
				t.Errorf("level %d differs from gifopt.Level(%d):\n got %+v\nwant %+v",
					level, level, lzw, *want.LZW)
			}
		}
	})

	t.Run("the default is the library default", func(t *testing.T) {
		// So that running with no flags matches gifopt.Optimize.
		if levelDefault != int(gifopt.LevelBalanced) {
			t.Errorf("default level %d, want %d", levelDefault, gifopt.LevelBalanced)
		}
	})

	t.Run("unknown levels are rejected", func(t *testing.T) {
		for _, level := range []int{-1, levelThorough + 1, 99} {
			if _, _, err := settingsForLevel(level); err == nil {
				t.Errorf("level %d was accepted", level)
			}
		}
	})
}
