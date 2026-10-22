// Exercise real-screen drawing across a ReadPixels flush. Run with a native
// macOS desktop; success means the cached drawable survived End(false).
package main

import (
	"flag"
	"fmt"
	"image/color"
	"os"

	"github.com/hajimehoshi/ebiten/v2"
)

var emptyPresent = flag.Bool("empty-present", false, "finish readback without submitting another draw")

type game struct {
	frames  int
	updates int
	pixels  []byte
}

func (g *game) Update() error {
	g.updates++
	if g.frames >= 24 {
		fmt.Printf("PASS Metal drawable survives %d readback flushes; empty present=%v\n", g.frames, *emptyPresent)
		return ebiten.Termination
	}
	if g.updates >= 60 {
		return fmt.Errorf("no drawable/readback boundary was exercised")
	}
	return nil
}
func (*game) Layout(_, _ int) (int, int) { return 160, 100 }
func (*game) Draw(screen *ebiten.Image)  { screen.Fill(color.RGBA{R: 70, G: 100, B: 140, A: 255}) }
func (g *game) DrawFinalScreen(screen ebiten.FinalScreen, offscreen *ebiten.Image, geo ebiten.GeoM) {
	for range 24 {
		ebiten.DefaultDrawFinalScreen(screen, offscreen, geo)
		// Modify the offscreen to bypass any CPU readback cache. The real-screen
		// command before it must be flushed without ending/presenting the frame.
		offscreen.Fill(color.RGBA{R: 70, G: 100, B: 140, A: 255})
		offscreen.ReadPixels(g.pixels)
		if g.pixels[0] != 70 || g.pixels[3] != 255 {
			panic("readback content mismatch")
		}
		g.frames++
	}
	if !*emptyPresent {
		ebiten.DefaultDrawFinalScreen(screen, offscreen, geo)
	}
}
func main() {
	flag.Parse()
	ebiten.SetWindowTitle("Water Metal readback regression")
	ebiten.SetWindowSize(160, 100)
	// The regression must exercise actual drawables even when a user's larger
	// window covers the desktop. Keep this tiny unfocused fixture above it.
	ebiten.SetWindowFloating(true)
	ebiten.SetRunnableOnUnfocused(true)
	ebiten.SetFPSMode(ebiten.FPSModeVsyncOffMaximum)
	if err := ebiten.RunGameWithOptions(&game{pixels: make([]byte, 160*100*4)}, &ebiten.RunGameOptions{InitUnfocused: true}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
