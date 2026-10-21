// Command gifdiff compares two GIF files for visual differences.
//
// Each GIF is composited and consecutive identical frames coalesced, so an optimized
// GIF compares equal to its original as long as it renders the same animation.
//
// Visual differences exit 1. Structural differences that do not change what a viewer
// sees are reported as info only. Exit code 2 means a file could not be read.
package main

import (
	"flag"
	"fmt"
	"image/gif"
	"os"

	"samuelks.com/gifopt/gifdiff"
)

func main() {
	verbose := flag.Bool("v", false, "report animation statistics even when the GIFs match")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: gifdiff [options] <a.gif> <b.gif>\n\n")
		fmt.Fprintf(os.Stderr, "Compares two GIF files for visual differences.\n")
		fmt.Fprintf(os.Stderr, "Frames are composited and coalesced first, so structural changes\n")
		fmt.Fprintf(os.Stderr, "(merged frames, shrunk bounds, rewritten palettes) are not differences.\n\n")
		fmt.Fprintf(os.Stderr, "Exit codes: 0 = identical, 1 = visual difference, 2 = error.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) != 2 {
		flag.Usage()
		os.Exit(2)
	}

	gA, err := loadGIF(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading %s: %v\n", args[0], err)
		os.Exit(2)
	}
	gB, err := loadGIF(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading %s: %v\n", args[1], err)
		os.Exit(2)
	}

	res := gifdiff.Compare(gA, gB)

	for _, m := range res.VisualMessages() {
		fmt.Printf("visual: %s\n", m)
	}
	for _, m := range res.InfoMessages() {
		fmt.Printf("info:   %s\n", m)
	}

	if res.HasVisualDiff() {
		os.Exit(1)
	}

	if *verbose {
		fmt.Printf("info:   %d frame(s) → %d state(s), %dcs, %dx%d\n",
			res.FramesA, res.StatesA, res.DurationA, res.SizeA.X, res.SizeA.Y)
		fmt.Printf("info:   %d frame(s) → %d state(s), %dcs, %dx%d\n",
			res.FramesB, res.StatesB, res.DurationB, res.SizeB.X, res.SizeB.Y)
	}
	fmt.Println("ok: no visual differences found")
}

func loadGIF(path string) (*gif.GIF, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only file; close error carries no useful information
	return gif.DecodeAll(f)
}
