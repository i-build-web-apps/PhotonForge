package ui

import (
	"fmt"
	"image"
	"image/color"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"gocv.io/x/gocv"

	"github.com/paul/photonforge/engine"
	"github.com/paul/photonforge/provider"
)

const targetFPS = 30

// Window is the main PhotonForge UI.
type Window struct {
	app      fyne.App
	win      fyne.Window
	provider provider.ImageProvider
	stacker  *engine.Stacker
	debug    bool

	img    *canvas.Image
	status *widget.Label

	// Stretch parameters (atomic-ish via mutex).
	mu    sync.Mutex
	black float64
	gamma float64
	white float64

	running bool
}

func New(prov provider.ImageProvider, debug bool) *Window {
	return &Window{
		provider: prov,
		debug:    debug,
		black:    0,
		gamma:    1.0,
		white:    255,
	}
}

// Run opens the window and starts the capture loop. Blocks until closed.
func (w *Window) Run() {
	w.stacker = engine.NewStacker(w.debug, 2)

	w.app = app.New()
	w.win = w.app.NewWindow("PhotonForge")

	w.win.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if ev.Name == fyne.KeyEscape {
			w.Stop()
			w.win.Close()
		}
	})

	// Placeholder image — 640x480 black.
	placeholder := image.NewNRGBA(image.Rect(0, 0, 640, 480))
	w.img = canvas.NewImageFromImage(placeholder)
	w.img.FillMode = canvas.ImageFillContain
	w.img.SetMinSize(fyne.NewSize(640, 480))

	w.status = widget.NewLabel("Frames: 0 | Matches: 0")

	// --- Controls ---

	forgeBtn := widget.NewButton("Forge (New Stack)", func() {
		w.stacker.Reset()
		w.status.SetText("Stack reset — Forging…")
	})

	// Black level slider: 0–255
	blackLabel := widget.NewLabel("Black: 0")
	blackSlider := widget.NewSlider(0, 255)
	blackSlider.Value = 0
	blackSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.black = v
		w.mu.Unlock()
		blackLabel.SetText(fmt.Sprintf("Black: %.0f", v))
	}

	// Gamma slider: 0.1–5.0
	gammaLabel := widget.NewLabel("Gamma: 1.0")
	gammaSlider := widget.NewSlider(0.1, 5.0)
	gammaSlider.Value = 1.0
	gammaSlider.Step = 0.1
	gammaSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.gamma = v
		w.mu.Unlock()
		gammaLabel.SetText(fmt.Sprintf("Gamma: %.1f", v))
	}

	// White level slider: 1–10000 (accumulator values grow with frame count)
	whiteLabel := widget.NewLabel("White: 255")
	whiteSlider := widget.NewSlider(1, 10000)
	whiteSlider.Value = 255
	whiteSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.white = v
		w.mu.Unlock()
		whiteLabel.SetText(fmt.Sprintf("White: %.0f", v))
	}

	controls := container.NewVBox(
		forgeBtn,
		widget.NewSeparator(),
		blackLabel, blackSlider,
		gammaLabel, gammaSlider,
		whiteLabel, whiteSlider,
	)

	sidebar := container.New(layout.NewVBoxLayout(), controls)
	toolbar := container.NewHBox(w.status)
	content := container.NewBorder(nil, toolbar, sidebar, nil, w.img)

	w.win.SetContent(content)
	w.win.Resize(fyne.NewSize(960, 600))

	go w.captureLoop()

	w.win.ShowAndRun()

	// Cleanup after window closes.
	w.stacker.Close()
}

func (w *Window) Stop() {
	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
}

// captureLoop reads frames from the provider and feeds them to the stacker.
func (w *Window) captureLoop() {
	if err := w.provider.Open(); err != nil {
		w.status.SetText("Error: " + err.Error())
		return
	}
	defer w.provider.Close()

	w.mu.Lock()
	w.running = true
	w.mu.Unlock()

	frame := gocv.NewMat()
	defer frame.Close()

	ticker := time.NewTicker(time.Second / targetFPS)
	defer ticker.Stop()

	for range ticker.C {
		w.mu.Lock()
		if !w.running {
			w.mu.Unlock()
			return
		}
		black := w.black
		gamma := w.gamma
		white := w.white
		w.mu.Unlock()

		if !w.provider.Read(&frame) {
			w.status.SetText("Feed ended")
			return
		}
		if frame.Empty() {
			continue
		}

		// Submit frame to the stacker's background worker.
		w.stacker.Submit(frame)

		// Get the stretched display image from the accumulator.
		count := w.stacker.FrameCount()
		matches := w.stacker.LatestMatches()

		var displayMat gocv.Mat
		if count > 0 {
			displayMat = w.stacker.GetDisplay(black, gamma, white)
		} else {
			displayMat = gocv.NewMat()
		}

		if !displayMat.Empty() {
			goImg := matToImage(displayMat)
			w.img.Image = goImg
			w.img.Refresh()
		}
		displayMat.Close()

		w.status.SetText(fmt.Sprintf("Frames: %d | Matches: %d", count, matches))
	}
}

// matToImage converts a GoCV BGR Mat to a Go image.NRGBA for Fyne display.
func matToImage(mat gocv.Mat) *image.NRGBA {
	rows := mat.Rows()
	cols := mat.Cols()
	channels := mat.Channels()

	img := image.NewNRGBA(image.Rect(0, 0, cols, rows))
	data, _ := mat.DataPtrUint8()

	switch channels {
	case 1: // Grayscale
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				v := data[y*cols+x]
				img.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
			}
		}
	case 3: // BGR → RGBA
		stride := cols * 3
		for y := 0; y < rows; y++ {
			rowStart := y * stride
			for x := 0; x < cols; x++ {
				px := rowStart + x*3
				b := data[px]
				g := data[px+1]
				r := data[px+2]
				img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
			}
		}
	case 4: // BGRA → RGBA
		stride := cols * 4
		for y := 0; y < rows; y++ {
			rowStart := y * stride
			for x := 0; x < cols; x++ {
				px := rowStart + x*4
				b := data[px]
				g := data[px+1]
				r := data[px+2]
				a := data[px+3]
				img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: a})
			}
		}
	}
	return img
}
