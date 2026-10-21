package gifpix

import (
	"image"
	"image/color"
	"testing"
)

// palette returns an n-entry palette. The colors do not matter here; only how many
// entries there are.
func palette(n int) color.Palette {
	p := make(color.Palette, n)
	for i := range p {
		p[i] = color.RGBA{R: uint8(i), A: 0xFF}
	}
	return p
}

func TestLitWidth(t *testing.T) {
	tests := []struct {
		name       string
		palette    int
		pix        []byte
		want       int
		emptyRect  bool
		wantReason string
	}{
		{name: "two colors", palette: 2, pix: []byte{0, 1}, want: 2,
			wantReason: "the format's minimum"},
		{name: "index 3 needs two bits", palette: 4, pix: []byte{0, 3}, want: 2},
		{name: "index 4 needs three", palette: 8, pix: []byte{0, 4}, want: 3},
		{name: "large palette, low indices", palette: 256, pix: []byte{0, 1, 2}, want: 2,
			wantReason: "a frame using only low entries of a big palette codes narrow"},
		{name: "large palette, high index", palette: 256, pix: []byte{0, 255}, want: 8},
		{name: "palette bounds the width", palette: 16, pix: []byte{15, 15}, want: 4},
		{name: "empty frame falls back to the palette", palette: 16, emptyRect: true, want: 4,
			wantReason: "no pixels to narrow it down with"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := image.Rect(0, 0, len(tc.pix), 1)
			if tc.emptyRect {
				r = image.Rect(0, 0, 0, 0)
			}
			im := image.NewPaletted(r, palette(tc.palette))
			copy(im.Pix, tc.pix)
			if got := LitWidth(im); got != tc.want {
				t.Errorf("got %d, want %d %s", got, tc.want, tc.wantReason)
			}
		})
	}
}

func TestPix(t *testing.T) {
	full := image.NewPaletted(image.Rect(0, 0, 4, 3), palette(8))
	for i := range full.Pix {
		full.Pix[i] = byte(i % 8)
	}

	t.Run("contiguous frame is not copied", func(t *testing.T) {
		got := Pix(full)
		if len(got) != 12 {
			t.Fatalf("length: got %d, want 12", len(got))
		}
		if &got[0] != &full.Pix[0] {
			t.Error("a frame whose rows are already contiguous should be returned as is")
		}
	})

	t.Run("sub-image row padding is skipped", func(t *testing.T) {
		sub := full.SubImage(image.Rect(1, 1, 3, 3)).(*image.Paletted)
		got := Pix(sub)
		want := []byte{
			full.Pix[full.PixOffset(1, 1)], full.Pix[full.PixOffset(2, 1)],
			full.Pix[full.PixOffset(1, 2)], full.Pix[full.PixOffset(2, 2)],
		}
		if len(got) != len(want) {
			t.Fatalf("length: got %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("byte %d: got %d, want %d", i, got[i], want[i])
			}
		}
	})

	t.Run("empty frame", func(t *testing.T) {
		if got := Pix(image.NewPaletted(image.Rect(0, 0, 0, 0), palette(4))); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

// TestLitWidthIgnoresSubImagePadding checks that the padding between a sub-image's
// rows does not count toward the largest index.
func TestLitWidthIgnoresSubImagePadding(t *testing.T) {
	full := image.NewPaletted(image.Rect(0, 0, 8, 2), palette(256))
	for i := range full.Pix {
		full.Pix[i] = 200 // outside the sub-image, and needing 8 bits
	}
	sub := full.SubImage(image.Rect(0, 0, 2, 2)).(*image.Paletted)
	for y := range 2 {
		for x := range 2 {
			sub.Pix[sub.PixOffset(x, y)] = 3
		}
	}
	if got := LitWidth(sub); got != 2 {
		t.Errorf("got %d, want 2: only the pixels inside the bounds count", got)
	}
}
