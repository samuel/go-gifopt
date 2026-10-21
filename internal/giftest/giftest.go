// Package giftest provides synthetic GIF fixtures for tests.
//
// They are tiny and generated in process rather than committed as files: they target
// structural cases the real-world corpus in frames/testdata/ does not cover, and they
// keep the suite fast enough to run the whole config matrix against every one.
package giftest

import (
	"image"
	"image/color"
	"image/gif"
)

// Fixture is a named synthetic GIF together with the case it exercises.
type Fixture struct {
	// Name identifies the fixture in test output.
	Name string
	// Why describes the structural case the fixture covers.
	Why string
	// GIF builds a fresh GIF. Optimize works in place, so every call must return new
	// images.
	GIF func() *gif.GIF
}

const (
	fw = 16 // fixture canvas width
	fh = 12 // fixture canvas height
)

// basePalette returns a small opaque palette with a transparent entry last.
func basePalette() color.Palette {
	return color.Palette{
		color.RGBA{R: 0x20, G: 0x20, B: 0x20, A: 0xFF},
		color.RGBA{R: 0xE0, G: 0x10, B: 0x10, A: 0xFF},
		color.RGBA{R: 0x10, G: 0xE0, B: 0x10, A: 0xFF},
		color.RGBA{R: 0x10, G: 0x10, B: 0xE0, A: 0xFF},
		color.RGBA{},
	}
}

// filled returns a paletted image covering r, every pixel set to idx.
func filled(r image.Rectangle, pal color.Palette, idx byte) *image.Paletted {
	im := image.NewPaletted(r, pal)
	for i := range im.Pix {
		im.Pix[i] = idx
	}
	return im
}

// rectIn draws a solid rectangle of idx into im.
func rectIn(im *image.Paletted, r image.Rectangle, idx byte) {
	r = r.Intersect(im.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			im.Pix[im.PixOffset(x, y)] = idx
		}
	}
}

// animation assembles a GIF from frames, delays and disposal methods.
//
// A nil disposal slice is filled in with DisposalNone, matching what gif.DecodeAll
// produces. Fixtures exercising the nil case set the field back to nil themselves.
func animation(images []*image.Paletted, delays []int, disposal []byte, global color.Palette) *gif.GIF {
	if disposal == nil {
		disposal = make([]byte, len(images))
		for i := range disposal {
			disposal[i] = gif.DisposalNone
		}
	}
	g := &gif.GIF{
		Image:     images,
		Delay:     delays,
		Disposal:  disposal,
		LoopCount: 0,
		Config: image.Config{
			ColorModel: global,
			Width:      fw,
			Height:     fh,
		},
	}
	if global == nil {
		g.Config.ColorModel = nil
	}
	return g
}

// Fixtures returns every synthetic fixture.
func Fixtures() []Fixture {
	return []Fixture{
		{
			Name: "full-frames-opaque",
			Why:  "full-canvas opaque frames, shared global palette, no transparency",
			GIF: func() *gif.GIF {
				pal := basePalette()
				var ims []*image.Paletted
				for i := range 3 {
					im := filled(image.Rect(0, 0, fw, fh), pal, 0)
					rectIn(im, image.Rect(2+i*3, 2, 6+i*3, 8), byte(1+i))
					ims = append(ims, im)
				}
				return animation(ims, []int{10, 10, 10}, nil, pal)
			},
		},
		{
			Name: "partial-frames-transparency",
			Why:  "sub-rect frames that already use a transparent palette index",
			GIF: func() *gif.GIF {
				pal := basePalette()
				first := filled(image.Rect(0, 0, fw, fh), pal, 0)
				second := filled(image.Rect(4, 3, 12, 9), pal, 4) // all transparent
				rectIn(second, image.Rect(5, 4, 9, 7), 2)
				third := filled(image.Rect(6, 2, 14, 10), pal, 4)
				rectIn(third, image.Rect(7, 3, 11, 8), 3)
				return animation([]*image.Paletted{first, second, third},
					[]int{8, 8, 8}, nil, pal)
			},
		},
		{
			Name: "input-disposal-background",
			Why:  "input frames using DisposalBackground; the canvas has transparent holes punched in it that no later frame repaints",
			GIF: func() *gif.GIF {
				pal := basePalette()
				first := filled(image.Rect(0, 0, fw, fh), pal, 1)
				second := filled(image.Rect(4, 4, 8, 8), pal, 2)
				third := filled(image.Rect(2, 2, 6, 6), pal, 3)
				return animation([]*image.Paletted{first, second, third},
					[]int{10, 10, 10},
					[]byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone},
					pal)
			},
		},
		{
			Name: "input-disposal-previous",
			Why:  "input frames using DisposalPrevious; the canvas is rolled back after a frame",
			GIF: func() *gif.GIF {
				pal := basePalette()
				first := filled(image.Rect(0, 0, fw, fh), pal, 1)
				second := filled(image.Rect(4, 4, 10, 9), pal, 2)
				third := filled(image.Rect(1, 1, 7, 6), pal, 3)
				return animation([]*image.Paletted{first, second, third},
					[]int{10, 10, 10},
					[]byte{gif.DisposalNone, gif.DisposalPrevious, gif.DisposalNone},
					pal)
			},
		},
		{
			Name: "all-frames-identical",
			Why:  "every frame renders the same; the optimizer must collapse to one frame and sum the delays",
			GIF: func() *gif.GIF {
				pal := basePalette()
				var ims []*image.Paletted
				for range 4 {
					im := filled(image.Rect(0, 0, fw, fh), pal, 0)
					rectIn(im, image.Rect(3, 3, 9, 9), 2)
					ims = append(ims, im)
				}
				return animation(ims, []int{5, 5, 5, 5}, nil, pal)
			},
		},
		{
			Name: "single-frame",
			Why:  "one frame only; nothing to diff against",
			GIF: func() *gif.GIF {
				pal := basePalette()
				im := filled(image.Rect(0, 0, fw, fh), pal, 3)
				rectIn(im, image.Rect(4, 4, 10, 8), 1)
				return animation([]*image.Paletted{im}, []int{0}, nil, pal)
			},
		},
		{
			Name: "full-256-palette",
			Why:  "all 256 palette entries used, so no free slot exists for a transparent index",
			GIF: func() *gif.GIF {
				pal := make(color.Palette, 256)
				for i := range pal {
					pal[i] = color.RGBA{R: uint8(i), G: uint8(255 - i), B: uint8(i * 7), A: 0xFF}
				}
				// 16x16 frames so every one of the 256 indices is used.
				const n = 16
				var ims []*image.Paletted
				for f := range 3 {
					im := image.NewPaletted(image.Rect(0, 0, n, n), pal)
					for i := range im.Pix {
						im.Pix[i] = byte((i + f*17) % 256)
					}
					ims = append(ims, im)
				}
				g := animation(ims, []int{10, 10, 10}, nil, pal)
				g.Config.Width, g.Config.Height = n, n
				return g
			},
		},
		{
			Name: "local-palettes",
			Why:  "no global palette; each frame carries its own local color table",
			GIF: func() *gif.GIF {
				var ims []*image.Paletted
				for i := range 3 {
					pal := color.Palette{
						color.RGBA{R: uint8(0x30 + i*0x40), G: 0x20, B: 0x20, A: 0xFF},
						color.RGBA{R: 0x10, G: uint8(0x40 + i*0x30), B: 0x90, A: 0xFF},
						color.RGBA{},
					}
					im := filled(image.Rect(0, 0, fw, fh), pal, 0)
					rectIn(im, image.Rect(1+i*2, 2, 7+i*2, 9), 1)
					ims = append(ims, im)
				}
				return animation(ims, []int{10, 10, 10}, nil, nil)
			},
		},
		{
			Name: "zero-delays",
			Why:  "every delay is zero, so no state has a nonzero duration",
			GIF: func() *gif.GIF {
				pal := basePalette()
				var ims []*image.Paletted
				for i := range 3 {
					im := filled(image.Rect(0, 0, fw, fh), pal, 0)
					rectIn(im, image.Rect(1+i*4, 1, 5+i*4, 7), 3)
					ims = append(ims, im)
				}
				return animation(ims, []int{0, 0, 0}, nil, pal)
			},
		},
		{
			Name: "nil-disposal-slice",
			Why:  "a hand-built GIF that leaves Disposal nil, as callers constructing frames themselves do",
			GIF: func() *gif.GIF {
				pal := basePalette()
				var ims []*image.Paletted
				for i := range 3 {
					im := filled(image.Rect(0, 0, fw, fh), pal, 0)
					rectIn(im, image.Rect(2, 2+i*2, 10, 6+i*2), 2)
					ims = append(ims, im)
				}
				g := animation(ims, []int{10, 10, 10}, nil, pal)
				g.Disposal = nil
				return g
			},
		},
		{
			Name: "canvas-larger-than-frames",
			Why:  "the logical screen is bigger than any frame, so the uncovered margin must survive optimization",
			GIF: func() *gif.GIF {
				pal := basePalette()
				var ims []*image.Paletted
				for i := range 3 {
					im := filled(image.Rect(0, 0, 8, 6), pal, 0)
					rectIn(im, image.Rect(1+i, 1, 4+i, 4), byte(1+i))
					ims = append(ims, im)
				}
				// Frames cover 8x6 of a 16x12 logical screen.
				return animation(ims, []int{10, 10, 10}, nil, pal)
			},
		},
	}
}
