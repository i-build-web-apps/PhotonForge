package ui

import (
	"fmt"
	"image"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

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

	mu      sync.Mutex
	black   float64
	gamma   float64
	white   float64
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
	w.stacker = engine.NewStacker(w.debug, 4)

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

	blackLabel := widget.NewLabel("Black: 0")
	blackSlider := widget.NewSlider(0, 255)
	blackSlider.Value = 0
	blackSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.black = v
		w.mu.Unlock()
		blackLabel.SetText(fmt.Sprintf("Black: %.0f", v))
	}

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

	whiteLabel := widget.NewLabel("White: 255")
	whiteSlider := widget.NewSlider(1, 50000)
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

	w.stacker.Close()
}

func (w *Window) Stop() {
	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
}

func (w *Window) captureLoop() {
	if err := w.provider.Open(); err != nil {
		w.status.SetText("Error: " + err.Error())
		return
	}
	defer w.provider.Close()

	w.mu.Lock()
	w.running = true
	w.mu.Unlock()

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

		frame := w.provider.Read()
		if frame == nil {
			w.status.SetText("Feed ended")
			return
		}

		w.stacker.Submit(frame)

		count := w.stacker.FrameCount()
		matches := w.stacker.LatestMatches()

		var displayImg *image.NRGBA
		if count > 0 {
			displayImg = w.stacker.GetDisplay(black, gamma, white)
		}

		if displayImg != nil {
			w.img.Image = displayImg
			w.img.Refresh()
		}

		w.status.SetText(fmt.Sprintf("Frames: %d | Matches: %d", count, matches))
	}
}
