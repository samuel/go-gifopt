package gifenc_test

import (
	"bytes"
	"image"
	"image/color"
	stdgif "image/gif"
	"testing"

	"samuelks.com/gifopt/gifenc"
)

func makeTestGIF() *stdgif.GIF {
	pal := color.Palette{
		color.RGBA{0, 0, 0, 255},
		color.RGBA{255, 0, 0, 255},
		color.RGBA{0, 255, 0, 255},
		color.RGBA{0, 0, 255, 255},
	}
	frame := func() *image.Paletted {
		img := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
		for i := range img.Pix {
			img.Pix[i] = byte(i % len(pal))
		}
		return img
	}
	return &stdgif.GIF{
		Image: []*image.Paletted{frame(), frame()},
		Delay: []int{10, 10},
	}
}

func TestEncodeAll_roundtrip(t *testing.T) {
	g := makeTestGIF()
	var buf bytes.Buffer
	if err := gifenc.EncodeAll(&buf, g, nil); err != nil {
		t.Fatalf("EncodeAll: %v", err)
	}
	out, err := stdgif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(out.Image) != len(g.Image) {
		t.Fatalf("frame count: got %d, want %d", len(out.Image), len(g.Image))
	}
	for fi, wantFrame := range g.Image {
		gotFrame := out.Image[fi]
		if !gotFrame.Rect.Eq(wantFrame.Rect) {
			t.Errorf("frame %d rect: got %v, want %v", fi, gotFrame.Rect, wantFrame.Rect)
		}
		if len(gotFrame.Pix) != len(wantFrame.Pix) {
			t.Fatalf("frame %d pix len: got %d, want %d", fi, len(gotFrame.Pix), len(wantFrame.Pix))
		}
		for i, p := range wantFrame.Pix {
			if gotFrame.Pix[i] != p {
				t.Errorf("frame %d pix[%d]: got %d, want %d", fi, i, gotFrame.Pix[i], p)
				break
			}
		}
	}
}

func TestEncode_singleFrame(t *testing.T) {
	pal := color.Palette{
		color.RGBA{0, 0, 0, 255},
		color.RGBA{255, 255, 255, 255},
	}
	img := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	for i := range img.Pix {
		img.Pix[i] = byte(i % 2)
	}

	var buf bytes.Buffer
	if err := gifenc.Encode(&buf, img, nil); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	out, err := stdgif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(out.Image) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(out.Image))
	}
}

func TestEncodeAll_matchesStdlib(t *testing.T) {
	// Output must be decodable to the same pixels as stdlib output.
	g := makeTestGIF()

	var stdBuf bytes.Buffer
	stdGIF := &stdgif.GIF{
		Image: g.Image,
		Delay: g.Delay,
	}
	if err := stdgif.EncodeAll(&stdBuf, stdGIF); err != nil {
		t.Fatalf("stdlib EncodeAll: %v", err)
	}
	var optBuf bytes.Buffer
	if err := gifenc.EncodeAll(&optBuf, g, nil); err != nil {
		t.Fatalf("gifenc EncodeAll: %v", err)
	}

	stdOut, _ := stdgif.DecodeAll(bytes.NewReader(stdBuf.Bytes()))
	optOut, _ := stdgif.DecodeAll(bytes.NewReader(optBuf.Bytes()))

	for fi := range stdOut.Image {
		sf := stdOut.Image[fi]
		of := optOut.Image[fi]
		if !bytes.Equal(sf.Pix, of.Pix) {
			t.Errorf("frame %d pixels differ between stdlib and gifenc", fi)
		}
	}
}
