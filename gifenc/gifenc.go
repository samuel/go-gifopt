// Package gifenc is a GIF encoder with the same interface as image/gif that searches
// for a smaller LZW coding of each frame.
package gifenc

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"io"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"

	"samuelks.com/gifopt/gifenc/internal/lzwopt"
	"samuelks.com/gifopt/internal/gifpix"
)

// Options controls EncodeAll and Encode. A nil *Options uses the defaults.
type Options struct {
	// LZW controls the compression search. nil uses DefaultLZWOptions.
	LZW *LZWOptions

	// Concurrency is how many LZW searches run at once. Frames are compressed in
	// parallel, and a frame borrows workers that would otherwise sit idle, as when
	// fewer frames are left than workers. 0 or negative defaults to runtime.NumCPU().
	// Set to 1 to disable parallelism. The output is the same whatever its value.
	Concurrency int

	// Progress is an optional callback invoked during encoding. It is always called
	// serially, never concurrently with itself.
	Progress func(Progress)

	// NumColors, Quantizer and Drawer are used only by Encode, to convert an image
	// that is not already paletted; they match image/gif.Options.
	//
	// NumColors is the maximum number of colors used in the image, from 1 to 256.
	NumColors int
	// Quantizer is used to produce a palette with size NumColors.
	// palette.Plan9 is used in place of a nil Quantizer.
	Quantizer draw.Quantizer
	// Drawer is used to convert the source image to the desired palette.
	// draw.FloydSteinberg is used in place of a nil Drawer.
	Drawer draw.Drawer
}

// GIF format constants.
const (
	fColorTable = 1 << 7
	gcLabel     = 0xF9
	gcBlockSize = 0x04
	sExtension  = 0x21
	sImageDesc  = 0x2C
	sTrailer    = 0x3B
)

func log2(x int) int {
	if x < 2 {
		return 0
	}
	return bits.Len(uint(x-1)) - 1
}

func leUint16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
}

type encoder struct {
	w   *bufio.Writer
	err error
	g   gif.GIF

	globalCT         int
	globalColorTable [3 * 256]byte
	buf              [256]byte
}

func (e *encoder) write(p []byte) {
	if e.err != nil {
		return
	}
	_, e.err = e.w.Write(p)
}

func (e *encoder) writeByte(b byte) {
	if e.err != nil {
		return
	}
	e.err = e.w.WriteByte(b)
}

func encodeColorTable(dst []byte, p color.Palette, size int) (int, error) {
	if uint(size) >= 8 {
		return 0, errors.New("gifenc: cannot encode color table with more than 256 entries")
	}
	for i, c := range p {
		if c == nil {
			return 0, errors.New("gifenc: cannot encode color table with nil entries")
		}
		var r, g, b uint8
		if rgba, ok := c.(color.RGBA); ok {
			r, g, b = rgba.R, rgba.G, rgba.B
		} else {
			rr, gg, bb, _ := c.RGBA()
			r, g, b = uint8(rr>>8), uint8(gg>>8), uint8(bb>>8)
		}
		dst[3*i+0] = r
		dst[3*i+1] = g
		dst[3*i+2] = b
	}
	n := 1 << (size + 1)
	if n > len(p) {
		clear(dst[3*len(p) : 3*n])
	}
	return 3 * n, nil
}

func (e *encoder) writeHeader() {
	if e.err != nil {
		return
	}
	_, e.err = io.WriteString(e.w, "GIF89a")
	if e.err != nil {
		return
	}

	leUint16(e.buf[0:2], uint16(e.g.Config.Width))
	leUint16(e.buf[2:4], uint16(e.g.Config.Height))
	e.write(e.buf[:4])

	if p, ok := e.g.Config.ColorModel.(color.Palette); ok && len(p) > 0 {
		paddedSize := log2(len(p))
		e.buf[0] = fColorTable | uint8(paddedSize)
		e.buf[1] = e.g.BackgroundIndex
		e.buf[2] = 0x00 // Pixel Aspect Ratio
		e.write(e.buf[:3])
		var err error
		e.globalCT, err = encodeColorTable(e.globalColorTable[:], p, paddedSize)
		if err != nil && e.err == nil {
			e.err = err
			return
		}
		e.write(e.globalColorTable[:e.globalCT])
	} else {
		e.buf[0] = 0x00
		e.buf[1] = 0x00
		e.buf[2] = 0x00
		e.write(e.buf[:3])
	}

	// NETSCAPE looping extension.
	if len(e.g.Image) > 1 && e.g.LoopCount >= 0 {
		e.buf[0] = 0x21 // Extension Introducer
		e.buf[1] = 0xff // Application Label
		e.buf[2] = 0x0b // Block Size
		e.write(e.buf[:3])
		_, err := io.WriteString(e.w, "NETSCAPE2.0")
		if err != nil && e.err == nil {
			e.err = err
			return
		}
		e.buf[0] = 0x03
		e.buf[1] = 0x01
		leUint16(e.buf[2:4], uint16(e.g.LoopCount))
		e.buf[4] = 0x00
		e.write(e.buf[:5])
	}
}

// preparedFrame holds the pre-serialized header bytes and pixel data for one frame.
type preparedFrame struct {
	// header is everything written before the compressed image data: the Graphic
	// Control Extension, Image Descriptor, optional local color table, and the LZW
	// minimum code size byte.
	header   []byte
	pix      []byte // pixel data to LZW-compress
	litWidth int
}

// validateImageBlock reports whether pm can be encoded as a frame of g. It is cheap,
// so EncodeAll runs it on every frame before writing anything.
func validateImageBlock(pm *image.Paletted, g *gif.GIF) error {
	if len(pm.Palette) == 0 {
		return errors.New("gifenc: cannot encode image block with empty palette")
	}
	b := pm.Bounds()
	if b.Min.X < 0 || b.Max.X >= 1<<16 || b.Min.Y < 0 || b.Max.Y >= 1<<16 {
		return errors.New("gifenc: image block is too large to encode")
	}
	if !b.In(image.Rectangle{Max: image.Point{X: g.Config.Width, Y: g.Config.Height}}) {
		return errors.New("gifenc: image block is out of bounds")
	}
	return nil
}

// prepareImageBlock serializes all per-frame metadata up to and including the LZW
// minimum code size byte, and collects the pixel data. It is safe to call
// concurrently once writeHeader has run, since the global color table is then fixed.
func prepareImageBlock(pm *image.Paletted, delay int, disposal byte, g *gif.GIF, globalColorTable []byte, globalCTLen int) (preparedFrame, error) {
	if err := validateImageBlock(pm, g); err != nil {
		return preparedFrame{}, err
	}
	b := pm.Bounds()

	transparentIndex := -1
	for i, c := range pm.Palette {
		if c == nil {
			return preparedFrame{}, errors.New("gifenc: cannot encode color table with nil entries")
		}
		if _, _, _, a := c.RGBA(); a == 0 {
			transparentIndex = i
			break
		}
	}

	var buf [256]byte
	var header []byte

	if delay > 0 || disposal != 0 || transparentIndex != -1 {
		buf[0] = sExtension
		buf[1] = gcLabel
		buf[2] = gcBlockSize
		if transparentIndex != -1 {
			buf[3] = 0x01 | disposal<<2
		} else {
			buf[3] = 0x00 | disposal<<2
		}
		leUint16(buf[4:6], uint16(delay))
		if transparentIndex != -1 {
			buf[6] = uint8(transparentIndex)
		} else {
			buf[6] = 0x00
		}
		buf[7] = 0x00
		header = append(header, buf[:8]...)
	}

	buf[0] = sImageDesc
	leUint16(buf[1:3], uint16(b.Min.X))
	leUint16(buf[3:5], uint16(b.Min.Y))
	leUint16(buf[5:7], uint16(b.Dx()))
	leUint16(buf[7:9], uint16(b.Dy()))
	header = append(header, buf[:9]...)

	paddedSize := log2(len(pm.Palette))
	if gp, ok := g.Config.ColorModel.(color.Palette); ok && len(pm.Palette) <= len(gp) && &gp[0] == &pm.Palette[0] {
		// Frame uses the global palette directly; no local color table needed.
		header = append(header, 0)
	} else {
		var localColorTable [3 * 256]byte
		ct, err := encodeColorTable(localColorTable[:], pm.Palette, paddedSize)
		if err != nil {
			return preparedFrame{}, err
		}
		// Reuse the global color table if it matches (ignoring the transparent slot).
		useGlobal := false
		if ct <= globalCTLen {
			localLen := 3 * len(pm.Palette)
			if transparentIndex >= 0 {
				trOff := 3 * transparentIndex
				useGlobal = bytes.Equal(globalColorTable[:trOff], localColorTable[:trOff]) &&
					bytes.Equal(globalColorTable[trOff+3:localLen], localColorTable[trOff+3:localLen])
			} else {
				useGlobal = bytes.Equal(globalColorTable[:localLen], localColorTable[:localLen])
			}
		}
		if useGlobal {
			header = append(header, 0)
		} else {
			header = append(header, fColorTable|uint8(paddedSize))
			header = append(header, localColorTable[:ct]...)
		}
	}

	// The optimizer's size estimates use the same gifpix functions, so both code
	// pixels the same way.
	litWidth := gifpix.LitWidth(pm)
	header = append(header, uint8(litWidth))
	pix := gifpix.Pix(pm)

	return preparedFrame{header: header, pix: pix, litWidth: litWidth}, nil
}

// LZWOptions is an alias for lzwopt.Options, re-exported so callers can pass LZW
// options without importing the internal package.
type LZWOptions = lzwopt.Options

// DefaultLZWOptions returns the default LZW encoding options.
func DefaultLZWOptions() LZWOptions {
	return lzwopt.DefaultOptions()
}

// Progress is passed to Options.Progress during encoding. BytesDone and BytesTotal are
// aggregate pixel-byte counts across all frames; FramesDone counts fully compressed
// frames.
type Progress struct {
	BytesDone, BytesTotal   int
	FramesDone, FramesTotal int
}

// EncodeAll writes the images in g to w in GIF format using optimized LZW compression.
// Frames are compressed in parallel and written in order.
func EncodeAll(w io.Writer, g *gif.GIF, o *Options) error {
	var opts Options
	if o != nil {
		opts = *o
	}
	lzwOpts := DefaultLZWOptions()
	if opts.LZW != nil {
		lzwOpts = *opts.LZW
	}

	if len(g.Image) == 0 {
		return errors.New("gifenc: must provide at least one image")
	}
	if len(g.Image) != len(g.Delay) {
		return errors.New("gifenc: mismatched image and delay lengths")
	}
	if g.Disposal != nil && len(g.Image) != len(g.Disposal) {
		return errors.New("gifenc: mismatched image and disposal lengths")
	}

	eg := *g
	if eg.Config == (image.Config{}) {
		p := g.Image[0].Bounds().Max
		eg.Config.Width = p.X
		eg.Config.Height = p.Y
	} else if eg.Config.ColorModel != nil {
		if _, ok := eg.Config.ColorModel.(color.Palette); !ok {
			return errors.New("gifenc: GIF color model must be a color.Palette")
		}
	}

	var bw *bufio.Writer
	if bww, ok := w.(*bufio.Writer); ok {
		bw = bww
	} else {
		bw = bufio.NewWriter(w)
	}

	e := &encoder{w: bw, g: eg}
	e.writeHeader()
	if e.err != nil {
		return e.err
	}

	// Validate every frame up front so a GIF that cannot be encoded fails before
	// anything is written. Frames are prepared later by the worker that compresses
	// them, so a long animation is never held in memory all at once.
	nFrames := len(g.Image)
	bytesTotal := 0
	for _, pm := range g.Image {
		if err := validateImageBlock(pm, &eg); err != nil {
			return err
		}
		b := pm.Bounds()
		bytesTotal += b.Dx() * b.Dy()
	}

	// Compress frames in parallel.
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}

	type compressResult struct {
		header     []byte
		compressed []byte
		err        error
	}
	results := make([]chan compressResult, nFrames)
	for i := range results {
		results[i] = make(chan compressResult, 1)
	}

	// slots bounds how far ahead of the writer the workers may run, and so how many
	// frames are in memory. A slot is taken before a frame is claimed, so the frames
	// in progress are always the next ones the writer needs.
	slots := make(chan struct{}, concurrency+2)
	for range cap(slots) {
		slots <- struct{}{}
	}
	// abort releases workers blocked on a slot when the writer gives up early.
	abort := make(chan struct{})
	var abortOnce sync.Once
	stopWorkers := func() { abortOnce.Do(func() { close(abort) }) }

	var (
		globalBytesDone  atomic.Int64
		globalFramesDone atomic.Int32
		progressMu       sync.Mutex
	)

	callProgress := func() {
		if opts.Progress == nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		opts.Progress(Progress{
			BytesDone:   int(globalBytesDone.Load()),
			BytesTotal:  bytesTotal,
			FramesDone:  int(globalFramesDone.Load()),
			FramesTotal: nFrames,
		})
	}

	// Frames share one budget of LZW workers, so a frame can borrow idle ones when
	// there are fewer frames left than workers.
	var lzw *lzwopt.Workers
	if concurrency > 1 {
		lzw = lzwopt.NewWorkers(concurrency)
	}

	work := make(chan int, nFrames)
	for i := range nFrames {
		work <- i
	}
	close(work)

	var wg sync.WaitGroup
	for range concurrency {
		wg.Go(func() {
			for {
				select {
				case <-abort:
					return
				case <-slots:
				}
				i, ok := <-work
				if !ok {
					return
				}

				disposal := uint8(0)
				if g.Disposal != nil {
					disposal = g.Disposal[i]
				}
				fd, err := prepareImageBlock(g.Image[i], g.Delay[i], disposal, &eg, e.globalColorTable[:], e.globalCT)
				if err != nil {
					results[i] <- compressResult{err: err}
					continue
				}

				frameOpts := lzwOpts
				if opts.Progress != nil {
					var prevDone int
					frameOpts.Progress = func(done, _ int) {
						delta := done - prevDone
						prevDone = done
						globalBytesDone.Add(int64(delta))
						callProgress()
					}
				}
				compressed, err := lzw.Encode(fd.pix, fd.litWidth, frameOpts)
				globalFramesDone.Add(1)
				callProgress() // report frame completion with updated FramesDone
				results[i] <- compressResult{fd.header, compressed, err}
			}
		})
	}

	// Write frames in order, releasing a slot per frame so the workers can continue.
	for i := range nFrames {
		res := <-results[i]
		if res.err != nil {
			stopWorkers()
			wg.Wait()
			return res.err
		}
		e.write(res.header)
		e.write(res.compressed)
		if e.err != nil {
			stopWorkers()
			wg.Wait()
			return e.err
		}
		slots <- struct{}{}
	}
	stopWorkers()
	wg.Wait()

	e.writeByte(sTrailer)
	if e.err != nil {
		return e.err
	}
	return bw.Flush()
}

// Encode writes the image m to w in GIF format using optimized LZW compression.
//
// Unless m is already paletted with at most NumColors colors, it is converted using
// the Quantizer and Drawer in o.
func Encode(w io.Writer, m image.Image, o *Options) error {
	b := m.Bounds()
	if b.Dx() >= 1<<16 || b.Dy() >= 1<<16 {
		return errors.New("gifenc: image is too large to encode")
	}

	var opts Options
	if o != nil {
		opts = *o
	}
	if opts.NumColors < 1 || 256 < opts.NumColors {
		opts.NumColors = 256
	}
	if opts.Drawer == nil {
		opts.Drawer = draw.FloydSteinberg
	}

	pm, _ := m.(*image.Paletted)
	if pm == nil {
		if cp, ok := m.ColorModel().(color.Palette); ok {
			pm = image.NewPaletted(b, cp)
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					pm.Set(x, y, cp.Convert(m.At(x, y)))
				}
			}
		}
	}
	if pm == nil || len(pm.Palette) > opts.NumColors {
		pm = image.NewPaletted(b, palette.Plan9[:opts.NumColors])
		if opts.Quantizer != nil {
			pm.Palette = opts.Quantizer.Quantize(make(color.Palette, 0, opts.NumColors), m)
		}
		opts.Drawer.Draw(pm, b, m, b.Min)
	}

	if pm.Rect.Min != (image.Point{}) {
		dup := *pm
		dup.Rect = dup.Rect.Sub(dup.Rect.Min)
		pm = &dup
	}

	return EncodeAll(w, &gif.GIF{
		Image: []*image.Paletted{pm},
		Delay: []int{0},
		Config: image.Config{
			ColorModel: pm.Palette,
			Width:      b.Dx(),
			Height:     b.Dy(),
		},
	}, &opts)
}
