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
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/paul/photonforge/engine"
	"github.com/paul/photonforge/provider"
)

const targetFPS = 30

type Window struct {
	app      fyne.App
	win      fyne.Window
	provider provider.ImageProvider
	stacker  *engine.Stacker
	debug    bool

	// Display
	img *canvas.Image

	// Status bar labels
	lblFrames  *widget.Label
	lblMatches *widget.Label
	lblFPS     *widget.Label
	lblElapsed *widget.Label
	lblIntTime *widget.Label
	lblMode    *widget.Label

	// Stretch params (protected by mutex)
	mu    sync.Mutex
	black float64
	gamma float64
	white float64

	// Capture settings
	maxStars    int
	showPreview bool // show latest aligned frame instead of stack

	running   bool
	startTime time.Time
	lastFPS   float64
}

func New(prov provider.ImageProvider, debug bool) *Window {
	return &Window{
		provider: prov,
		debug:    debug,
		black:    0,
		gamma:    1.0,
		white:    255,
		maxStars: 80,
	}
}

func (w *Window) Run() {
	w.stacker = engine.NewStacker(w.debug, 4)

	w.app = app.New()
	w.win = w.app.NewWindow("PhotonForge")
	w.win.SetMaster()

	w.win.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if ev.Name == fyne.KeyEscape {
			w.Stop()
			w.win.Close()
		}
	})

	// Main image display.
	placeholder := image.NewNRGBA(image.Rect(0, 0, 640, 480))
	w.img = canvas.NewImageFromImage(placeholder)
	w.img.FillMode = canvas.ImageFillContain
	w.img.SetMinSize(fyne.NewSize(640, 480))

	// Build UI components.
	toolbar := w.buildToolbar()
	statusBar := w.buildStatusBar()

	content := container.NewBorder(
		toolbar,   // top
		statusBar, // bottom
		nil,       // left
		nil,       // right
		w.img,     // center
	)

	w.win.SetContent(content)
	w.win.Resize(fyne.NewSize(1024, 700))

	go w.captureLoop()

	w.win.ShowAndRun()
	w.stacker.Close()
}

func (w *Window) Stop() {
	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
}

// --- Toolbar ---

func (w *Window) buildToolbar() fyne.CanvasObject {
	forgeBtn := widget.NewButtonWithIcon("New Stack", theme.ViewRefreshIcon(), func() {
		w.stacker.Reset()
		w.startTime = time.Now()
		fyne.Do(func() {
			w.lblFrames.SetText("0")
			w.lblMatches.SetText("0")
			w.lblElapsed.SetText("0s")
			w.lblIntTime.SetText("0s")
		})
	})
	forgeBtn.Importance = widget.HighImportance

	stretchBtn := widget.NewButtonWithIcon("Stretch", theme.ColorChromaticIcon(), func() {
		w.showStretchDialog()
	})

	captureBtn := widget.NewButtonWithIcon("Capture", theme.SettingsIcon(), func() {
		w.showCaptureSettingsDialog()
	})

	searchBtn := widget.NewButtonWithIcon("Search", theme.SearchIcon(), func() {
		w.showSearchDialog()
	})

	debugBtn := widget.NewButtonWithIcon("Debug", theme.VisibilityIcon(), func() {
		w.debug = !w.debug
		w.stacker.Aligner.SetDebug(w.debug)
		status := "OFF"
		if w.debug {
			status = "ON"
		}
		dialog.ShowInformation("Debug Mode", fmt.Sprintf("Star overlay: %s", status), w.win)
	})

	previewBtn := widget.NewButtonWithIcon("Preview", theme.MediaPhotoIcon(), func() {
		w.mu.Lock()
		w.showPreview = !w.showPreview
		mode := "Stack"
		if w.showPreview {
			mode = "Preview"
		}
		w.mu.Unlock()
		fyne.Do(func() {
			w.lblMode.SetText(mode)
		})
	})

	return container.NewHBox(
		forgeBtn,
		widget.NewSeparator(),
		stretchBtn,
		captureBtn,
		searchBtn,
		debugBtn,
		previewBtn,
		layout.NewSpacer(),
		w.buildModeLabel(),
	)
}

func (w *Window) buildModeLabel() fyne.CanvasObject {
	w.lblMode = widget.NewLabel("Stack")
	w.lblMode.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewHBox(
		widget.NewLabel("View:"),
		w.lblMode,
	)
}

// --- Status Bar ---

func (w *Window) buildStatusBar() fyne.CanvasObject {
	w.lblFrames = widget.NewLabel("0")
	w.lblMatches = widget.NewLabel("0")
	w.lblFPS = widget.NewLabel("0.0")
	w.lblElapsed = widget.NewLabel("0s")
	w.lblIntTime = widget.NewLabel("0s")

	makeStatBox := func(label string, value *widget.Label) fyne.CanvasObject {
		title := widget.NewLabel(label)
		title.TextStyle = fyne.TextStyle{Bold: true}
		value.TextStyle = fyne.TextStyle{Monospace: true}
		return container.NewHBox(title, value)
	}

	return container.NewHBox(
		makeStatBox("Frames:", w.lblFrames),
		widget.NewSeparator(),
		makeStatBox("Matches:", w.lblMatches),
		widget.NewSeparator(),
		makeStatBox("FPS:", w.lblFPS),
		widget.NewSeparator(),
		makeStatBox("Elapsed:", w.lblElapsed),
		widget.NewSeparator(),
		makeStatBox("Integration:", w.lblIntTime),
		layout.NewSpacer(),
	)
}

// --- Dialogs ---

func (w *Window) showStretchDialog() {
	w.mu.Lock()
	curBlack := w.black
	curGamma := w.gamma
	curWhite := w.white
	w.mu.Unlock()

	blackLabel := widget.NewLabel(fmt.Sprintf("Black Level: %.0f", curBlack))
	blackSlider := widget.NewSlider(0, 1000)
	blackSlider.Value = curBlack
	blackSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.black = v
		w.mu.Unlock()
		blackLabel.SetText(fmt.Sprintf("Black Level: %.0f", v))
	}

	gammaLabel := widget.NewLabel(fmt.Sprintf("Gamma: %.1f", curGamma))
	gammaSlider := widget.NewSlider(0.1, 5.0)
	gammaSlider.Value = curGamma
	gammaSlider.Step = 0.05
	gammaSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.gamma = v
		w.mu.Unlock()
		gammaLabel.SetText(fmt.Sprintf("Gamma: %.2f", v))
	}

	whiteLabel := widget.NewLabel(fmt.Sprintf("White Level: %.0f", curWhite))
	whiteSlider := widget.NewSlider(1, 100000)
	whiteSlider.Value = curWhite
	whiteSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.white = v
		w.mu.Unlock()
		whiteLabel.SetText(fmt.Sprintf("White Level: %.0f", v))
	}

	autoBtn := widget.NewButton("Auto Stretch", func() {
		count := w.stacker.FrameCount()
		if count > 0 {
			autoWhite := float64(count) * 128
			autoBlack := float64(count) * 3
			w.mu.Lock()
			w.white = autoWhite
			w.black = autoBlack
			w.gamma = 2.0
			w.mu.Unlock()
			whiteSlider.SetValue(autoWhite)
			blackSlider.SetValue(autoBlack)
			gammaSlider.SetValue(2.0)
			whiteLabel.SetText(fmt.Sprintf("White Level: %.0f", autoWhite))
			blackLabel.SetText(fmt.Sprintf("Black Level: %.0f", autoBlack))
			gammaLabel.SetText("Gamma: 2.00")
		}
	})

	content := container.NewVBox(
		widget.NewLabel("Histogram Stretch Controls"),
		widget.NewSeparator(),
		blackLabel, blackSlider,
		gammaLabel, gammaSlider,
		whiteLabel, whiteSlider,
		widget.NewSeparator(),
		autoBtn,
	)

	d := dialog.NewCustom("Stretch Settings", "Close", content, w.win)
	d.Resize(fyne.NewSize(400, 350))
	d.Show()
}

func (w *Window) showCaptureSettingsDialog() {
	maxStarsEntry := widget.NewEntry()
	maxStarsEntry.SetText(fmt.Sprintf("%d", w.maxStars))

	form := []*widget.FormItem{
		widget.NewFormItem("Max Stars", maxStarsEntry),
	}

	dialog.ShowForm("Capture Settings", "Apply", "Cancel", form, func(ok bool) {
		if ok {
			var n int
			fmt.Sscanf(maxStarsEntry.Text, "%d", &n)
			if n >= 10 && n <= 500 {
				w.maxStars = n
			}
		}
	}, w.win)
}

func (w *Window) showSearchDialog() {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("e.g. M42, Sirius, NGC 7000")

	resultLabel := widget.NewLabel("")
	resultLabel.Wrapping = fyne.TextWrapWord

	searchBtn := widget.NewButtonWithIcon("Search", theme.SearchIcon(), func() {
		if entry.Text == "" {
			return
		}
		resultLabel.SetText(fmt.Sprintf("Search for %q — connect a database with -db flag to enable catalog search.", entry.Text))
	})

	content := container.NewVBox(
		widget.NewLabel("Search Star Catalog"),
		widget.NewSeparator(),
		entry,
		searchBtn,
		widget.NewSeparator(),
		resultLabel,
	)

	d := dialog.NewCustom("Object Search", "Close", content, w.win)
	d.Resize(fyne.NewSize(450, 300))
	d.Show()
}

// --- Capture Loop ---

func (w *Window) captureLoop() {
	if err := w.provider.Open(); err != nil {
		fyne.Do(func() {
			w.lblFrames.SetText("ERR")
			dialog.ShowError(fmt.Errorf("Failed to open provider: %w", err), w.win)
		})
		return
	}
	defer w.provider.Close()

	w.mu.Lock()
	w.running = true
	w.mu.Unlock()
	w.startTime = time.Now()

	ticker := time.NewTicker(time.Second / targetFPS)
	defer ticker.Stop()

	frameCounter := 0
	fpsTimer := time.Now()

	for range ticker.C {
		w.mu.Lock()
		if !w.running {
			w.mu.Unlock()
			return
		}
		black := w.black
		gamma := w.gamma
		white := w.white
		showPreview := w.showPreview
		w.mu.Unlock()

		frame := w.provider.Read()
		if frame == nil {
			fyne.Do(func() {
				w.lblFrames.SetText("END")
			})
			return
		}

		w.stacker.Submit(frame)
		frameCounter++

		// Calculate FPS every second.
		if time.Since(fpsTimer) >= time.Second {
			w.lastFPS = float64(frameCounter) / time.Since(fpsTimer).Seconds()
			frameCounter = 0
			fpsTimer = time.Now()
		}

		count := w.stacker.FrameCount()
		matches := w.stacker.LatestMatches()
		elapsed := time.Since(w.startTime)

		// Integration time: frames / capture fps * exposure equivalent.
		intTime := time.Duration(float64(count) / targetFPS * float64(time.Second))

		var displayImg *image.NRGBA
		if showPreview {
			displayImg = w.stacker.GetPreview()
		} else if count > 0 {
			displayImg = w.stacker.GetDisplay(black, gamma, white)
		}

		// Update UI on the Fyne thread.
		img := displayImg
		fps := w.lastFPS
		fyne.Do(func() {
			if img != nil {
				w.img.Image = img
				w.img.Refresh()
			}
			w.lblFrames.SetText(fmt.Sprintf("%d", count))
			w.lblMatches.SetText(fmt.Sprintf("%d", matches))
			w.lblFPS.SetText(fmt.Sprintf("%.1f", fps))
			w.lblElapsed.SetText(formatDuration(elapsed))
			w.lblIntTime.SetText(formatDuration(intTime))
		})
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%02dm", h, m)
}
