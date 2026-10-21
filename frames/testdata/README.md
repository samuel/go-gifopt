Corpus
======

Real-world GIFs for `TestOptimizeIsLosslessCorpus`. Each is optimized, re-encoded,
decoded again and compared against the original under every config in the gate's
matrix, so every file here costs its time several times over. The set is therefore
kept minimal: one file per structural quirk that the synthetic fixtures in
`internal/giftest` cannot supply, and for each quirk the cheapest file that has it.

`fileCases` also picks up any other `.gif` dropped into this directory, so a larger
local corpus adds coverage without being committed.

Why each file is here
---------------------

| File | Shape | What it tests |
| --- | --- | --- |
| `test_4frame_encoder_test.gif` | 4 frames, 120x80, 32-color global table plus local tables of 16, 32 and 64 entries, disposal 0 | Local color tables of differing sizes, and the only file small enough to run through the exhaustive LZW settings. |
| `Deal With It.gif` | 6 frames, 500x500, 64-color global palette, 5 partial frames | A palette with room to spare, so transparency needs no repurposed index. Trips the unsound `DisposalBackground` selection under `no-compress-check`. |
| `You Win This Round Frank.gif` | 83 frames, 300x149, 80 partial, 82 carrying transparency, 21 distinct palettes | The heaviest exercise of frame rebounding: the difference rectangle moves every frame and the input's own transparency is a candidate throughout. |
| `spiderman116_spideydancing1.gif` | 185 frames, 176x176, 32-color palette, 184 partial | The only input using `DisposalPrevious` (17 frames of it), which is the one disposal that makes the reference rendering roll back. |
| `R2D2 Burrito.gif` | 8 frames, 500x281, no global color table, 8 local 256-color tables, 404 distinct colors | No global palette at all, and too many colors to unify into one, so `matchImagePalettes` must fail and every frame keep a local table. |
| `Stop Acting So Stupid.gif` | 15 full-canvas frames, 264x200, 15 distinct 256-color palettes, no transparent entry anywhere, 1725 distinct colors | A full palette with no free slot: transparency can only be expressed by repurposing an index. The only file that activates `LossyForceTransparentColor`, and the only one with no input transparency to inherit. |

What is deliberately not here
-----------------------------

A GIF with one shared 256-color global palette, full frames, existing transparency
and `DisposalPrevious` unused is the common case, and the files above already cover
every branch it reaches. Adding more of them costs gate time and repository size for
no new coverage.
