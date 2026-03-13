package ui

import (
	"fmt"
	"image"
	"image/color"
	imagepng "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	"golang.org/x/image/tiff"

	"github.com/paul/photonforge/db"
	"github.com/paul/photonforge/engine"
	"github.com/paul/photonforge/provider"
)

const targetFPS = 30

// CaptureConfig holds all settings needed to create an ImageProvider.
// autoPreset defines a named automatic histogram configuration.
type autoPreset struct {
	Name string
	// Apply computes B/G/W from accumulator statistics.
	// median, p95, maxVal are in per-frame space (0–255).
	Apply func(median, p95, maxVal float64) (black, gamma, white float64)
}

var autoPresets = []autoPreset{
	{
		Name: "Starfield",
		Apply: func(median, p95, _ float64) (float64, float64, float64) {
			// Clip noise floor just above background; moderate gamma lifts faint stars.
			black := median * 1.2
			if black > 100 {
				black = 100
			}
			white := p95
			if white <= black {
				white = black + 1
			}
			if white > 500 {
				white = 500
			}
			return black, 0.45, white
		},
	},
	{
		Name: "Nebula",
		Apply: func(median, p95, _ float64) (float64, float64, float64) {
			// Aggressive stretch to reveal faint nebulosity — low black, strong gamma.
			black := median * 0.8
			if black > 60 {
				black = 60
			}
			white := p95 * 1.5
			if white <= black {
				white = black + 1
			}
			if white > 500 {
				white = 500
			}
			return black, 0.30, white
		},
	},
	{
		Name: "Planetary",
		Apply: func(median, p95, maxVal float64) (float64, float64, float64) {
			// High contrast for bright objects — tight range, mild gamma.
			black := median * 1.5
			if black > 120 {
				black = 120
			}
			white := (p95 + maxVal) / 2
			if white <= black {
				white = black + 1
			}
			if white > 500 {
				white = 500
			}
			return black, 0.60, white
		},
	},
	{
		Name: "Full Range",
		Apply: func(_, _, maxVal float64) (float64, float64, float64) {
			// Linear stretch across the entire data range.
			white := maxVal
			if white < 1 {
				white = 255
			}
			if white > 500 {
				white = 500
			}
			return 0, 1.0, white
		},
	},
	{
		Name: "Reset",
		Apply: func(_, _, _ float64) (float64, float64, float64) {
			return 0, 1.0, 255
		},
	},
}

type CaptureConfig struct {
	Mode    string // "webcam" or "test"
	Device  string // camera device index ("0", "1", ...)
	Width   int
	Height  int
	FPS     int
	TestDir string
	Jitter  bool
}

// CreateProvider builds an ImageProvider from the current config.
func (c *CaptureConfig) CreateProvider() provider.ImageProvider {
	switch c.Mode {
	case "test":
		return provider.NewDirectoryProvider(c.TestDir, c.Jitter, true)
	default:
		return provider.DefaultWebcamProvider(c.Device, c.Width, c.Height, c.FPS)
	}
}

type Window struct {
	app    fyne.App
	win    fyne.Window
	config *CaptureConfig

	store   *db.Store
	stacker *engine.Stacker
	debug   bool

	// Display
	img *canvas.Image

	// Footer section widgets (updated each frame)
	lblStack     *widget.Label
	stackBar     *crimsonProgressBar
	lblHistogram *widget.Label
	lblAlignment *widget.Label
	lblFPS       *widget.Label
	lblElapsed   *widget.Label
	lblIntTime   *widget.Label
	lblMode      *widget.Label
	lblStarfield *widget.Label

	// Stretch params (protected by mu)
	mu    sync.Mutex
	black float64
	gamma float64
	white float64

	// Capture settings
	maxStars    int
	showPreview bool

	// Zoom & pan (protected by mu)
	zoom       float64 // 1.0 = fit-to-window, 2.0 = 200%, etc.
	panX       float64 // pan offset in image-pixel coords (center of view)
	panY       float64
	imgW       int // last known source image dimensions
	imgH       int
	smoothZoom bool // bilinear upscaling when zoomed

	// Minimap overlay (visible when zoomed)
	minimap      *canvas.Image
	minimapPanel *fyne.Container

	// Data log
	logEntries        []string
	logList           *widget.List
	logPanel          *fyne.Container
	logVisible        bool
	logShowTimestamps bool
	logWidthLayout    *fixedWidthLayout
	logExpandedWidth  float32
	logDragHandle     *dragHandle
	logDetailText     *canvas.Text
	logDetailPanel    *fyne.Container

	// Auto histogram presets
	autoPresetIdx int
	autoPresetLbl *widget.Label

	// Action toast — brief text shown at bottom of preview
	toastLabel *canvas.Text
	toastTimer *time.Timer

	// Solve overlay — fading object labels after plate solve
	solveOverlay *canvas.Text
	solvePanel   *fyne.Container
	solveFade    *time.Ticker

	// Solve annotations — drawn on the image
	solveResult    *engine.SolveResult
	solveLabels    []solveLabel
	solveLabelFade float64 // 1.0 = full, fades to 0

	// Highlight overlay — rotating red square on a selected star
	highlightActive bool
	highlightX      float64
	highlightY      float64
	highlightAngle  float64

	// Capture lifecycle
	running        bool
	captureDone    chan struct{}
	activeProvider provider.ImageProvider
	startTime      time.Time
	lastFPS        float64
}

// FyneWindow returns the underlying Fyne window for automation.
func (w *Window) FyneWindow() fyne.Window {
	return w.win
}

// AppState returns a snapshot of application state for automation queries.
func (w *Window) AppState() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := map[string]any{
		"running":       w.running,
		"black":         w.black,
		"gamma":         w.gamma,
		"white":         w.white,
		"zoom":          w.zoom,
		"debug":         w.debug,
		"mode":          w.config.Mode,
		"fps":           w.lastFPS,
		"maxStars":      w.maxStars,
		"showPreview":   w.showPreview,
	}
	if w.stacker != nil {
		state["frameCount"] = w.stacker.FrameCount()
		state["stackedFrames"] = w.stacker.StackedFrames()
		state["droppedFrames"] = w.stacker.DroppedFrames()
		state["detectedStars"] = w.stacker.LatestDetected()
		state["matchedStars"] = w.stacker.LatestMatches()
		state["stackDepth"] = w.stacker.StackDepth()
	}
	return state
}

func New(cfg *CaptureConfig, debug bool, store *db.Store) *Window {
	return &Window{
		store:      store,
		config:     cfg,
		debug:      debug,
		black:      0,
		gamma:      1.0,
		white:            255,
		maxStars:         80,
		zoom:             1.0,
		logVisible:       true,
		logExpandedWidth: 200,
	}
}

func (w *Window) Run() {
	w.stacker = engine.NewStacker(w.debug, 2)
	w.stacker.SetMaxStars(w.maxStars)

	w.app = app.New()
	if iconData, err := os.ReadFile("PhotonForge_Logo.jpg"); err == nil {
		w.app.SetIcon(fyne.NewStaticResource("logo.jpg", iconData))
	}
	w.win = w.app.NewWindow("PhotonForge  ·  Live Stacker")
	w.win.SetMaster()

	w.win.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if ev.Name == fyne.KeyEscape {
			w.stopCapture()
			w.win.Close()
		}
	})

	placeholder := image.NewNRGBA(image.Rect(0, 0, 640, 480))
	w.img = canvas.NewImageFromImage(placeholder)
	w.img.FillMode = canvas.ImageFillContain

	tapOverlay := newTappableOverlay(func(ev *fyne.PointEvent) {
		w.handleImageTap(ev)
	})
	tapOverlay.onScroll = func(ev *fyne.ScrollEvent) {
		w.mu.Lock()
		oldZoom := w.zoom
		if ev.Scrolled.DY > 0 {
			w.zoom = math.Min(w.zoom*1.15, 20.0)
		} else if ev.Scrolled.DY < 0 {
			w.zoom = math.Max(w.zoom/1.15, 1.0)
		}
		// Reset pan when returning to fit-to-window
		if w.zoom == 1.0 {
			w.panX = 0
			w.panY = 0
		}
		newZoom := w.zoom
		w.mu.Unlock()
		if oldZoom != newZoom {
			w.updateViewLabel()
		}
	}
	tapOverlay.onDrag = func(ev *fyne.DragEvent) {
		w.mu.Lock()
		if w.zoom > 1.0 && w.imgW > 0 && w.imgH > 0 {
			// Convert widget-space drag to image-pixel drag
			widgetW := float64(w.img.Size().Width)
			widgetH := float64(w.img.Size().Height)
			imgW := float64(w.imgW)
			imgH := float64(w.imgH)
			scaleX := widgetW / imgW
			scaleY := widgetH / imgH
			scale := math.Min(scaleX, scaleY) * w.zoom
			w.panX -= float64(ev.Dragged.DX) / scale
			w.panY -= float64(ev.Dragged.DY) / scale
			// Clamp pan
			maxPanX := imgW / 2
			maxPanY := imgH / 2
			w.panX = math.Max(-maxPanX, math.Min(maxPanX, w.panX))
			w.panY = math.Max(-maxPanY, math.Min(maxPanY, w.panY))
		}
		w.mu.Unlock()
	}

	sidebar := w.buildSidebar()
	dataLog := w.buildDataLog()
	footer := w.buildFooter()

	// Minimap: shown top-right when zoomed in.
	minimapPlaceholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	w.minimap = canvas.NewImageFromImage(minimapPlaceholder)
	w.minimap.FillMode = canvas.ImageFillContain
	w.minimap.SetMinSize(fyne.NewSize(140, 105))

	smoothCheck := widget.NewCheck("Smooth", func(v bool) {
		w.mu.Lock()
		w.smoothZoom = v
		w.mu.Unlock()
	})

	resetZoomBtn := widget.NewButton("Reset Zoom", func() {
		w.mu.Lock()
		w.zoom = 1.0
		w.panX = 0
		w.panY = 0
		w.mu.Unlock()
		w.updateViewLabel()
	})
	resetZoomBtn.Importance = widget.LowImportance

	minimapInner := container.NewVBox(
		w.minimap,
		container.NewHBox(smoothCheck, layout.NewSpacer(), resetZoomBtn),
	)
	minimapBg := canvas.NewRectangle(color.NRGBA{R: 0, G: 0, B: 0, A: 180})
	w.minimapPanel = container.NewStack(minimapBg, pad(minimapInner, 4, 4, 4, 4))
	w.minimapPanel.Hide()

	// Solve overlay — fades out over 10 seconds showing identified objects.
	w.solveOverlay = canvas.NewText("", color.NRGBA{R: 220, G: 60, B: 60, A: 255})
	w.solveOverlay.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	w.solveOverlay.TextSize = 14
	w.solveOverlay.Alignment = fyne.TextAlignCenter
	w.solveOverlay.Hide()
	solveContainer := container.NewCenter(w.solveOverlay)
	solveBg := canvas.NewRectangle(color.NRGBA{R: 0, G: 0, B: 0, A: 140})
	w.solvePanel = container.NewStack(solveBg, pad(solveContainer, 8, 16, 8, 16))
	solveAnchor := container.NewBorder(nil, nil, nil, nil, container.NewCenter(w.solvePanel))
	w.solvePanel.Hide()

	// Action toast label — shown briefly at bottom of preview on sidebar clicks.
	w.toastLabel = canvas.NewText("", color.NRGBA{R: 220, G: 60, B: 60, A: 255})
	w.toastLabel.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	w.toastLabel.TextSize = 13
	w.toastLabel.Alignment = fyne.TextAlignCenter
	w.toastLabel.Hide()
	toastContainer := container.NewVBox(layout.NewSpacer(), container.NewCenter(w.toastLabel), layout.NewSpacer())
	toastAnchor := container.NewBorder(nil, container.New(&fixedHeightLayout{height: 36}, toastContainer), nil, nil, nil)

	// Log detail bar — shown at the bottom of the preview when a log entry is clicked.
	w.logDetailText = canvas.NewText("", color.NRGBA{R: 200, G: 200, B: 200, A: 255})
	w.logDetailText.TextStyle = fyne.TextStyle{Monospace: true}
	w.logDetailText.TextSize = 11
	detailBg := canvas.NewRectangle(color.NRGBA{R: 20, G: 2, B: 2, A: 220})
	w.logDetailPanel = container.NewStack(detailBg,
		container.New(&fixedHeightLayout{height: 24}, pad(w.logDetailText, 4, 8, 4, 8)))
	w.logDetailPanel.Hide()
	detailAnchor := container.NewBorder(nil, w.logDetailPanel, nil, nil, nil)

	// Overlay: sidebar and datalog float over the image.
	// Footer is placed outside the stack so it is never clipped by the image.
	imgOverlay := container.NewBorder(
		nil, nil,
		withTransparentBg(dataLog),
		withSidebarBg(sidebar),
		nil,
	)

	// Minimap floats at top-right over the image, independent of the border layout.
	minimapAnchor := container.NewBorder(
		container.NewHBox(layout.NewSpacer(), w.minimapPanel),
		nil, nil, nil, nil,
	)

	imageArea := container.NewStack(w.img, tapOverlay, imgOverlay, minimapAnchor, solveAnchor, toastAnchor, detailAnchor)

	// Top-level border: footer anchored at the bottom, image area fills the rest.
	content := container.NewBorder(
		nil,
		withFooterBg(footer),
		nil, nil,
		imageArea,
	)

	w.win.SetContent(content)
	w.win.Resize(fyne.NewSize(1280, 820))

	w.addLog("PhotonForge started")
	depth := w.stacker.StackDepth()
	if depth > 0 {
		w.addLog(fmt.Sprintf("Rolling stack: %d frames", depth))
	} else {
		w.addLog("Stack mode: unlimited")
	}

	go w.ramMonitor()

	w.startCapture()
	w.win.ShowAndRun()
	w.stopCapture()
	w.stacker.Close()
}

// ---------------------------------------------------------------------------
// Capture lifecycle
// ---------------------------------------------------------------------------

func (w *Window) startCapture() {
	w.mu.Lock()
	w.running = true
	w.captureDone = make(chan struct{})
	w.mu.Unlock()
	w.startTime = time.Now()
	go w.captureLoop()
}

func (w *Window) stopCapture() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	done := w.captureDone
	prov := w.activeProvider
	w.mu.Unlock()
	if prov != nil {
		prov.Close()
	}
	if done != nil {
		<-done
	}
}

func (w *Window) restartCapture() {
	// Run in a goroutine to avoid blocking the UI thread —
	// stopCapture waits on captureDone which needs the capture
	// goroutine to finish, which may need fyne.Do callbacks to drain.
	go func() {
		w.stopCapture()
		w.stacker.Reset()
		w.startCapture()
		w.addLog("Capture restarted")
	}()
}

// ---------------------------------------------------------------------------
// Night-vision palette
//
// Stratified crimson tints — preserves dark adaptation.
// Background < card < header < accent < primary-text.
// ---------------------------------------------------------------------------

var (
	// Semi-transparent panel backgrounds (starfield bleeds through)
	colorPanelBg   = color.NRGBA{R: 20, G: 1, B: 1, A: 222}
	colorSidebarBg = color.NRGBA{R: 16, G: 1, B: 1, A: 228}
	colorFooterBg  = color.NRGBA{R: 18, G: 1, B: 1, A: 235}

	// Section card (opaque — sits above panel bg)
	colorCardBg      = color.NRGBA{R: 36, G: 3, B: 3, A: 255}
	colorCardTopRule = color.NRGBA{R: 120, G: 18, B: 18, A: 255}

	// Console header
	colorLogHeaderBg = color.NRGBA{R: 42, G: 4, B: 4, A: 245}

	// Dividers
	colorDivider     = color.NRGBA{R: 85, G: 10, B: 10, A: 200}
	colorDividerFine = color.NRGBA{R: 55, G: 5, B: 5, A: 160}

	// Text hierarchy
	colorSectionLbl = color.NRGBA{R: 130, G: 28, B: 28, A: 255} // dim section label
	colorValueText  = color.NRGBA{R: 215, G: 85, B: 85, A: 255} // primary readout
	colorTimestamp  = color.NRGBA{R: 95, G: 22, B: 22, A: 255}  // console timestamp

	// Console alternating row stripe
	colorLogOddRow = color.NRGBA{R: 38, G: 3, B: 3, A: 90}

	// Custom progress bar
	colorBarBg       = color.NRGBA{R: 48, G: 4, B: 4, A: 220}
	colorBarFill     = color.NRGBA{R: 155, G: 22, B: 22, A: 255}
	colorBarFillFull = color.NRGBA{R: 195, G: 38, B: 38, A: 255}
)

func withTransparentBg(obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewStack(canvas.NewRectangle(colorPanelBg), obj)
}
func withFooterBg(obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewStack(canvas.NewRectangle(colorFooterBg), obj)
}
func withSidebarBg(obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewStack(canvas.NewRectangle(colorSidebarBg), obj)
}

// ---------------------------------------------------------------------------
// Layout primitives
// ---------------------------------------------------------------------------

type fixedWidthLayout struct{ width float32 }

func (f *fixedWidthLayout) MinSize(_ []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(f.width, 0)
}
func (f *fixedWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Resize(fyne.NewSize(f.width, size.Height))
		o.Move(fyne.NewPos(0, 0))
	}
}

type fixedSizeLayout struct{ size fyne.Size }

func (f *fixedSizeLayout) MinSize(_ []fyne.CanvasObject) fyne.Size { return f.size }
func (f *fixedSizeLayout) Layout(objects []fyne.CanvasObject, _ fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(f.size)
	}
}

type fixedHeightLayout struct{ height float32 }

func (f *fixedHeightLayout) MinSize(_ []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, f.height)
}
func (f *fixedHeightLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(fyne.NewSize(size.Width, f.height))
	}
}

// dragHandle is a thin vertical bar the user can drag to resize a panel.
type dragHandle struct {
	widget.BaseWidget
	onDrag func(dx float32)
}

func newDragHandle(onDrag func(dx float32)) *dragHandle {
	h := &dragHandle{onDrag: onDrag}
	h.ExtendBaseWidget(h)
	return h
}

func (h *dragHandle) CreateRenderer() fyne.WidgetRenderer {
	bar := canvas.NewRectangle(color.NRGBA{R: 120, G: 20, B: 20, A: 180})
	bar.SetMinSize(fyne.NewSize(4, 0))
	return &dragHandleRenderer{bar: bar}
}

func (h *dragHandle) Dragged(ev *fyne.DragEvent) {
	if h.onDrag != nil {
		h.onDrag(ev.Dragged.DX)
	}
}

func (h *dragHandle) DragEnd() {}

func (h *dragHandle) MinSize() fyne.Size {
	return fyne.NewSize(4, 0)
}

type dragHandleRenderer struct {
	bar *canvas.Rectangle
}

func (r *dragHandleRenderer) Layout(size fyne.Size) { r.bar.Resize(size) }
func (r *dragHandleRenderer) MinSize() fyne.Size    { return fyne.NewSize(4, 0) }
func (r *dragHandleRenderer) Refresh()              {}
func (r *dragHandleRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bar}
}
func (r *dragHandleRenderer) Destroy() {}

// insetLayout adds asymmetric padding around a single child.
type insetLayout struct{ top, right, bottom, left float32 }

func (p *insetLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) == 0 {
		return fyne.NewSize(p.left+p.right, p.top+p.bottom)
	}
	inner := objects[0].MinSize()
	return fyne.NewSize(inner.Width+p.left+p.right, inner.Height+p.top+p.bottom)
}
func (p *insetLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(p.left, p.top))
		o.Resize(fyne.NewSize(size.Width-p.left-p.right, size.Height-p.top-p.bottom))
	}
}

// pad wraps obj with asymmetric inset padding.
func pad(obj fyne.CanvasObject, top, right, bottom, left float32) fyne.CanvasObject {
	return container.New(&insetLayout{top, right, bottom, left}, obj)
}

// hRule returns a 1 px horizontal divider line.
func hRule() fyne.CanvasObject {
	r := canvas.NewRectangle(colorDivider)
	r.SetMinSize(fyne.NewSize(0, 1))
	return r
}

// ---------------------------------------------------------------------------
// Custom crimson progress bar
// ---------------------------------------------------------------------------

type crimsonProgressBar struct {
	widget.BaseWidget
	value float64
}

func newCrimsonProgressBar() *crimsonProgressBar {
	b := &crimsonProgressBar{}
	b.ExtendBaseWidget(b)
	return b
}

func (b *crimsonProgressBar) SetValue(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	b.value = v
	b.Refresh()
}

func (b *crimsonProgressBar) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(colorBarBg)
	fill := canvas.NewRectangle(colorBarFill)
	return &crimsonBarRenderer{bar: b, bg: bg, fill: fill}
}

type crimsonBarRenderer struct {
	bar  *crimsonProgressBar
	bg   *canvas.Rectangle
	fill *canvas.Rectangle
}

func (r *crimsonBarRenderer) MinSize() fyne.Size { return fyne.NewSize(60, 5) }
func (r *crimsonBarRenderer) Destroy()           {}
func (r *crimsonBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.fill}
}
func (r *crimsonBarRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)
	fillW := size.Width * float32(r.bar.value)
	r.fill.Move(fyne.NewPos(0, 0))
	r.fill.Resize(fyne.NewSize(fillW, size.Height))
	if r.bar.value >= 0.999 {
		r.fill.FillColor = colorBarFillFull
	} else {
		r.fill.FillColor = colorBarFill
	}
}
func (r *crimsonBarRenderer) Refresh() {
	r.Layout(r.bar.Size())
	canvas.Refresh(r.bar)
}

// ---------------------------------------------------------------------------
// Styled text helpers
// ---------------------------------------------------------------------------

// sectionTitle returns the dim crimson label used as card/section headings.
func sectionTitle(text string) *canvas.Text {
	t := canvas.NewText(text, colorSectionLbl)
	t.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	t.TextSize = 9
	return t
}

// valueLabel returns a monospaced label for primary readout values.
func valueLabel(text string) *widget.Label {
	lbl := widget.NewLabel(text)
	lbl.TextStyle = fyne.TextStyle{Monospace: true}
	return lbl
}

// ---------------------------------------------------------------------------
// Data log (left panel)
// ---------------------------------------------------------------------------

type solveLabel struct {
	Name string
	RA   float64
	Dec  float64
	Type string
}

// projectToPixel converts RA/Dec to image pixel coordinates using the solve result.
func (w *Window) projectToPixel(ra, dec float64) (float64, float64, bool) {
	w.mu.Lock()
	sr := w.solveResult
	imgW := w.imgW
	imgH := w.imgH
	w.mu.Unlock()

	if sr == nil || imgW == 0 || imgH == 0 {
		return 0, 0, false
	}

	ra0 := sr.CenterRA * math.Pi / 180
	dec0 := sr.CenterDec * math.Pi / 180
	rar := ra * math.Pi / 180
	decr := dec * math.Pi / 180

	sinDec0 := math.Sin(dec0)
	cosDec0 := math.Cos(dec0)
	sinDec := math.Sin(decr)
	cosDec := math.Cos(decr)
	dra := rar - ra0

	cosC := sinDec0*sinDec + cosDec0*cosDec*math.Cos(dra)
	if cosC < 0.1 {
		return 0, 0, false
	}

	x := (cosDec * math.Sin(dra)) / cosC
	y := (cosDec0*sinDec - sinDec0*cosDec*math.Cos(dra)) / cosC

	fovRad := sr.FOVDeg * math.Pi / 180
	scale := float64(imgW) / fovRad

	px := float64(imgW)/2 + x*scale
	py := float64(imgH)/2 - y*scale

	if px < 0 || px >= float64(imgW) || py < 0 || py >= float64(imgH) {
		return 0, 0, false
	}
	return px, py, true
}

// pixelToRADec converts image pixel coordinates back to RA/Dec using the solve result.
func (w *Window) pixelToRADec(px, py float64) (ra, dec float64, ok bool) {
	w.mu.Lock()
	sr := w.solveResult
	imgW := w.imgW
	imgH := w.imgH
	w.mu.Unlock()

	if sr == nil || imgW == 0 || imgH == 0 {
		return 0, 0, false
	}

	fovRad := sr.FOVDeg * math.Pi / 180
	scale := float64(imgW) / fovRad

	// Tangent plane coordinates (inverse of projectToPixel)
	x := (px - float64(imgW)/2) / scale
	y := (float64(imgH)/2 - py) / scale

	ra0 := sr.CenterRA * math.Pi / 180
	dec0 := sr.CenterDec * math.Pi / 180
	sinDec0 := math.Sin(dec0)
	cosDec0 := math.Cos(dec0)

	rho := math.Sqrt(x*x + y*y)
	if rho < 1e-12 {
		return sr.CenterRA, sr.CenterDec, true
	}
	c := math.Atan(rho)
	sinC := math.Sin(c)
	cosC := math.Cos(c)

	decR := math.Asin(cosC*sinDec0 + y*sinC*cosDec0/rho)
	raR := ra0 + math.Atan2(x*sinC, rho*cosDec0*cosC-y*sinDec0*sinC)

	ra = raR * 180 / math.Pi
	dec = decR * 180 / math.Pi
	for ra < 0 {
		ra += 360
	}
	for ra >= 360 {
		ra -= 360
	}
	return ra, dec, true
}

// drawSolveAnnotations draws arrows pointing to identified objects with white text labels.
func drawSolveAnnotations(img *image.NRGBA, labels []solveLabel, projectFn func(ra, dec float64) (float64, float64, bool), alpha uint8) {
	if len(labels) == 0 || alpha == 0 {
		return
	}

	textColor := color.NRGBA{R: 255, G: 255, B: 255, A: alpha}
	arrowColor := color.NRGBA{R: 255, G: 255, B: 255, A: alpha}
	dotColor := color.NRGBA{R: 255, G: 100, B: 100, A: alpha}

	for i, lbl := range labels {
		px, py, ok := projectFn(lbl.RA, lbl.Dec)
		if !ok {
			continue
		}

		ix, iy := int(px), int(py)

		// Draw small red dot at the object position
		b := img.Bounds()
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				if dx*dx+dy*dy <= 4 {
					nx, ny := ix+dx, iy+dy
					if nx >= b.Min.X && nx < b.Max.X && ny >= b.Min.Y && ny < b.Max.Y {
						img.SetNRGBA(nx, ny, dotColor)
					}
				}
			}
		}

		// Alternate label placement to avoid overlap:
		// even items go upper-right, odd items go lower-left
		var labelX, labelY, arrowEndX, arrowEndY int
		if i%2 == 0 {
			labelX = ix + 30
			labelY = iy - 25
			arrowEndX = ix + 4
			arrowEndY = iy - 4
		} else {
			labelX = ix + 30
			labelY = iy + 20
			arrowEndX = ix + 4
			arrowEndY = iy + 4
		}

		// Arrow line from label area to object
		drawArrowWithHead(img, labelX-2, labelY+7, arrowEndX, arrowEndY, arrowColor)

		// White text label (2x scale for readability)
		drawScaledText(img, labelX, labelY-7, lbl.Name, textColor, 2)
	}
}

// drawArrowWithHead draws a line with a small triangular arrowhead at the tip.
func drawArrowWithHead(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	b := img.Bounds()
	dx := float64(x1 - x0)
	dy := float64(y1 - y0)
	length := math.Sqrt(dx*dx + dy*dy)
	if length < 3 {
		return
	}

	// Draw shaft
	steps := int(length)
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		x := int(float64(x0)*(1-t) + float64(x1)*t)
		y := int(float64(y0)*(1-t) + float64(y1)*t)
		if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
			img.SetNRGBA(x, y, c)
		}
	}

	// Arrowhead — two short lines angled 30° from the shaft
	ux := dx / length
	uy := dy / length
	headLen := 6.0
	angle := 0.45 // ~26 degrees
	cosA := math.Cos(angle)
	sinA := math.Sin(angle)
	for _, side := range []float64{1, -1} {
		hx := -ux*cosA - uy*sinA*side
		hy := -uy*cosA + ux*sinA*side
		for s := 0; s < int(headLen); s++ {
			t := float64(s) / headLen
			hpx := int(float64(x1) + hx*headLen*t)
			hpy := int(float64(y1) + hy*headLen*t)
			if hpx >= b.Min.X && hpx < b.Max.X && hpy >= b.Min.Y && hpy < b.Max.Y {
				img.SetNRGBA(hpx, hpy, c)
			}
		}
	}
}

// drawArrowLine draws a line with a small arrowhead at the end.
func drawArrowLine(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	b := img.Bounds()
	dx := float64(x1 - x0)
	dy := float64(y1 - y0)
	length := math.Sqrt(dx*dx + dy*dy)
	if length < 2 {
		return
	}
	steps := int(length)
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		x := int(float64(x0)*(1-t) + float64(x1)*t)
		y := int(float64(y0)*(1-t) + float64(y1)*t)
		if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
			img.SetNRGBA(x, y, c)
		}
	}
}

// drawSimpleText draws text using the bitmap font scaled by the given factor.
// scale=1 gives 5x7 pixels per character, scale=2 gives 10x14, etc.
func drawSimpleText(img *image.NRGBA, x, y int, text string, c color.NRGBA) {
	drawScaledText(img, x, y, text, c, 1)
}

func drawScaledText(img *image.NRGBA, x, y int, text string, c color.NRGBA, scale int) {
	if scale < 1 {
		scale = 1
	}
	b := img.Bounds()
	cx := x
	for _, ch := range text {
		if ch == ' ' {
			cx += 4 * scale
			continue
		}
		for dy := 0; dy < 7; dy++ {
			for dx := 0; dx < 5; dx++ {
				if charPixel(ch, dx, dy) {
					for sy := 0; sy < scale; sy++ {
						for sx := 0; sx < scale; sx++ {
							px, py := cx+dx*scale+sx, y+dy*scale+sy
							if px >= b.Min.X && px < b.Max.X && py >= b.Min.Y && py < b.Max.Y {
								img.SetNRGBA(px, py, c)
							}
						}
					}
				}
			}
		}
		cx += 6 * scale
	}
}

// charPixel returns true if the given pixel should be set for a character.
// Minimal 5x7 bitmap font for uppercase letters, digits, and common symbols.
func charPixel(ch rune, x, y int) bool {
	fonts := map[rune][7]uint8{
		'A': {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
		'B': {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
		'C': {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
		'D': {0x1E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1E},
		'E': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
		'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
		'G': {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F},
		'H': {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
		'I': {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
		'J': {0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C},
		'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
		'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
		'M': {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11},
		'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
		'O': {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
		'P': {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
		'Q': {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
		'R': {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
		'S': {0x0E, 0x11, 0x10, 0x0E, 0x01, 0x11, 0x0E},
		'T': {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
		'U': {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
		'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
		'W': {0x11, 0x11, 0x11, 0x15, 0x15, 0x15, 0x0A},
		'X': {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
		'Y': {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04},
		'Z': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
		'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
		'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
		'2': {0x0E, 0x11, 0x01, 0x06, 0x08, 0x10, 0x1F},
		'3': {0x0E, 0x11, 0x01, 0x06, 0x01, 0x11, 0x0E},
		'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
		'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
		'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
		'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
		'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
		'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
		'-': {0x00, 0x00, 0x00, 0x1F, 0x00, 0x00, 0x00},
		'+': {0x00, 0x04, 0x04, 0x1F, 0x04, 0x04, 0x00},
		'.': {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04},
		'°': {0x06, 0x09, 0x06, 0x00, 0x00, 0x00, 0x00},
		'\'': {0x04, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00},
	}

	// Convert lowercase to uppercase
	if ch >= 'a' && ch <= 'z' {
		ch = ch - 'a' + 'A'
	}

	bitmap, ok := fonts[ch]
	if !ok {
		return false
	}
	if y < 0 || y >= 7 || x < 0 || x >= 5 {
		return false
	}
	return bitmap[y]&(1<<uint(4-x)) != 0
}

// showSolveResult displays identified objects centered on the preview, fading out over 10 seconds.
func (w *Window) showSolveResult(text string) {
	if w.solveFade != nil {
		w.solveFade.Stop()
	}
	alpha := uint8(255)
	fyne.Do(func() {
		w.solveOverlay.Text = text
		w.solveOverlay.Color = color.NRGBA{R: 220, G: 60, B: 60, A: alpha}
		w.solveOverlay.Show()
		w.solveOverlay.Refresh()
		w.solvePanel.Show()
	})

	w.solveFade = time.NewTicker(100 * time.Millisecond)
	go func() {
		steps := 100 // 10 seconds at 100ms intervals
		for i := 0; i < steps; i++ {
			<-w.solveFade.C
			remaining := float64(steps-i) / float64(steps)
			a := uint8(remaining * 255)
			fyne.Do(func() {
				w.solveOverlay.Color = color.NRGBA{R: 220, G: 60, B: 60, A: a}
				w.solveOverlay.Refresh()
			})
		}
		w.solveFade.Stop()
		fyne.Do(func() {
			w.solveOverlay.Hide()
			w.solvePanel.Hide()
		})
	}()
}

// showToast briefly displays text at the bottom of the preview area.
func (w *Window) showToast(msg string) {
	fyne.Do(func() {
		w.toastLabel.Text = msg
		w.toastLabel.Show()
		w.toastLabel.Refresh()
	})
	if w.toastTimer != nil {
		w.toastTimer.Stop()
	}
	w.toastTimer = time.AfterFunc(2*time.Second, func() {
		fyne.Do(func() {
			w.toastLabel.Hide()
		})
	})
}

func (w *Window) addLog(msg string) {
	ts := time.Now().Format("15:04:05")
	line := ts + "  " + msg

	w.mu.Lock()
	w.logEntries = append(w.logEntries, line)
	w.mu.Unlock()

	fyne.Do(func() {
		w.logList.Refresh()
		w.logList.ScrollToBottom()
	})
}

func (w *Window) buildDataLog() *fyne.Container {
	w.logList = widget.NewList(
		func() int {
			w.mu.Lock()
			defer w.mu.Unlock()
			return len(w.logEntries)
		},
		// Template row: alternating stripe bg + timestamp column + message column.
		func() fyne.CanvasObject {
			rowBg := canvas.NewRectangle(color.Transparent)

			ts := canvas.NewText("00:00:00", colorTimestamp)
			ts.TextStyle = fyne.TextStyle{Monospace: true}
			ts.TextSize = 10

			msg := canvas.NewText("", colorValueText)
			msg.TextStyle = fyne.TextStyle{Monospace: true}
			msg.TextSize = 10

			// Fixed 62px timestamp column; message fills the rest.
			tsCol := container.New(&fixedWidthLayout{width: 62},
				pad(ts, 2, 2, 2, 6))
			msgCol := pad(msg, 2, 4, 2, 0)

			row := container.NewBorder(nil, nil, tsCol, nil, msgCol)
			return container.NewStack(rowBg, row)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if id >= len(w.logEntries) {
				return
			}
			line := w.logEntries[id]

			stack := obj.(*fyne.Container)
			rowBg := stack.Objects[0].(*canvas.Rectangle)
			row := stack.Objects[1].(*fyne.Container)

			// Alternating row stripe.
			if id%2 == 0 {
				rowBg.FillColor = color.Transparent
			} else {
				rowBg.FillColor = colorLogOddRow
			}
			rowBg.Refresh()

			// container.NewBorder layout: Objects[0]=centre, Objects[1]=left
			tsCol := row.Objects[1].(*fyne.Container)
			msgCol := row.Objects[0]

			var tsText, msgText string
			if len(line) >= 8 {
				tsText = line[:8]
				if len(line) > 10 {
					msgText = line[10:]
				}
			} else {
				msgText = line
			}

			// Timestamps: remove column entirely when unchecked, show with dedup when checked.
			showTS := w.logShowTimestamps
			if showTS {
				tsCol.Show()
				// Collapse duplicate timestamps — only show when different from previous row.
				if id > 0 && id < len(w.logEntries) {
					prev := w.logEntries[id-1]
					if len(prev) >= 8 && prev[:8] == tsText {
						tsText = ""
					}
				}
			} else {
				tsCol.Hide()
				tsText = ""
			}

			// Navigate into the inset wrapper to reach the canvas.Text.
			if inner, ok := tsCol.Objects[0].(*fyne.Container); ok {
				if t, ok2 := inner.Objects[0].(*canvas.Text); ok2 {
					t.Text = tsText
					t.Refresh()
				}
			}
			if inner, ok := msgCol.(*fyne.Container); ok {
				if t, ok2 := inner.Objects[0].(*canvas.Text); ok2 {
					t.Text = msgText
					t.Refresh()
				}
			}
		},
	)

	// When a log entry is tapped, show its full text in the detail bar.
	w.logList.OnSelected = func(id widget.ListItemID) {
		w.mu.Lock()
		if id >= len(w.logEntries) {
			w.mu.Unlock()
			return
		}
		line := w.logEntries[id]
		w.mu.Unlock()

		w.logDetailText.Text = line
		w.logDetailText.Refresh()
		w.logDetailPanel.Show()
	}

	// Header bar.
	consoleTitle := canvas.NewText("CONSOLE", color.NRGBA{R: 175, G: 48, B: 48, A: 255})
	consoleTitle.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	consoleTitle.TextSize = 10

	tsCheck := widget.NewCheck("ts", func(checked bool) {
		w.mu.Lock()
		w.logShowTimestamps = checked
		w.mu.Unlock()
		w.logList.Refresh()
	})
	tsCheck.Checked = false

	toggleBtn := widget.NewButton("—", nil)
	toggleBtn.Importance = widget.LowImportance
	toggleBtn.OnTapped = func() {
		w.logVisible = !w.logVisible
		if w.logVisible {
			toggleBtn.SetText("—")
			w.logList.Show()
			if w.logDragHandle != nil {
				w.logDragHandle.Show()
			}
			w.logWidthLayout.width = w.logExpandedWidth
		} else {
			toggleBtn.SetText("+")
			w.logList.Hide()
			if w.logDragHandle != nil {
				w.logDragHandle.Hide()
			}
			w.logExpandedWidth = w.logWidthLayout.width
			w.logWidthLayout.width = 80
		}
		w.logPanel.Refresh()
	}

	headerContent := container.NewBorder(nil, nil,
		pad(consoleTitle, 4, 8, 4, 8),
		container.NewHBox(tsCheck, toggleBtn),
	)
	header := container.NewStack(
		canvas.NewRectangle(colorLogHeaderBg),
		container.New(&fixedHeightLayout{height: 26}, headerContent),
	)

	w.logPanel = container.NewBorder(
		container.NewVBox(header, hRule()),
		nil, nil, nil,
		w.logList,
	)

	w.logWidthLayout = &fixedWidthLayout{width: w.logExpandedWidth}

	// Drag handle on the right edge for resizing.
	w.logDragHandle = newDragHandle(func(dx float32) {
		newW := w.logWidthLayout.width + dx
		if newW < 120 {
			newW = 120
		}
		if newW > 500 {
			newW = 500
		}
		w.logWidthLayout.width = newW
		w.logExpandedWidth = newW
		w.logPanel.Refresh()
	})

	logWithHandle := container.NewBorder(nil, nil, nil, w.logDragHandle, w.logPanel)
	return container.New(w.logWidthLayout, logWithHandle)
}

// ---------------------------------------------------------------------------
// Sidebar (right panel)
// ---------------------------------------------------------------------------

func (w *Window) buildSidebar() fyne.CanvasObject {
	// makeItem builds a vertically-stacked icon-button + text-label cell.
	// Each cell has a fixed size to prevent VBox compression/overlap.
	makeItem := func(icon fyne.Resource, label string, action func()) fyne.CanvasObject {
		btn := widget.NewButtonWithIcon("", icon, action)
		btn.Importance = widget.LowImportance

		lbl := canvas.NewText(label, colorValueText)
		lbl.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
		lbl.TextSize = 9
		lbl.Alignment = fyne.TextAlignCenter

		cell := container.NewVBox(
			container.NewCenter(
				container.New(&fixedSizeLayout{size: fyne.NewSize(44, 36)}, btn),
			),
			container.NewCenter(lbl),
		)
		// Fixed size per cell prevents VBox from compressing/overlapping items.
		return container.New(&fixedSizeLayout{size: fyne.NewSize(72, 56)}, cell)
	}

	// groupSep returns a thin horizontal rule between button groups.
	groupSep := func() fyne.CanvasObject {
		r := canvas.NewRectangle(colorDividerFine)
		r.SetMinSize(fyne.NewSize(0, 1))
		spacer := func() fyne.CanvasObject {
			s := canvas.NewRectangle(color.Transparent)
			s.SetMinSize(fyne.NewSize(0, 4))
			return s
		}
		return container.NewVBox(spacer(), r, spacer())
	}

	// Group 1 — session control
	resetItem := makeItem(theme.ViewRefreshIcon(), "RESET", func() {
		w.stacker.Reset()
		w.startTime = time.Now()
		w.addLog("Stack reset — new session")
		w.showToast("Stack Reset")
	})
	importItem := makeItem(theme.FolderOpenIcon(), "IMPORT", func() {
		w.showToast("Import Image...")
		w.showImportDialog()
	})
	configItem := makeItem(theme.SettingsIcon(), "CONFIG", func() {
		w.showToast("Configuration")
		w.showConfigDialog()
	})

	// Group 2 — image processing
	alignItem := makeItem(theme.MediaFastForwardIcon(), "ALIGN", func() {
		w.showToast("Alignment Settings")
		w.showAlignmentDialog()
	})

	// Group 3 — catalog & solve
	catalogItem := makeItem(theme.SearchIcon(), "CATALOG", func() {
		w.showObjectsDialogNew()
	})
	solveItem := makeItem(theme.NavigateNextIcon(), "SOLVE", func() {
		w.showToast("Solving Starfield...")
		go w.runPlateSolve()
	})

	// Group 4 — save
	saveItem := makeItem(theme.DocumentSaveIcon(), "SAVE", func() {
		w.showToast("Saving...")
		go w.quickSave()
	})
	exportItem := makeItem(theme.FolderIcon(), "EXPORT", func() {
		w.showToast("Export Options")
		w.showSaveDialog()
	})

	// Group 5 — view / debug
	debugItem := makeItem(theme.VisibilityIcon(), "DEBUG", func() {
		w.debug = !w.debug
		w.stacker.Aligner.SetDebug(w.debug)
		status := "OFF"
		if w.debug {
			status = "ON"
		}
		w.addLog(fmt.Sprintf("Debug overlay: %s", status))
		w.showToast(fmt.Sprintf("Debug: %s", status))
	})
	previewItem := makeItem(theme.MediaPhotoIcon(), "PREVIEW", func() {
		w.mu.Lock()
		w.showPreview = !w.showPreview
		viewMode := "Stack"
		if w.showPreview {
			viewMode = "Preview"
		}
		w.mu.Unlock()
		w.updateViewLabel()
		w.addLog(fmt.Sprintf("View mode: %s", viewMode))
		w.showToast(fmt.Sprintf("View: %s", viewMode))
	})

	// 2px accent line on the left edge of the sidebar.
	accentLine := canvas.NewRectangle(colorDivider)
	accentLine.SetMinSize(fyne.NewSize(2, 0))

	buttons := container.NewVBox(
		pad(resetItem, 8, 0, 6, 0),
		pad(importItem, 6, 0, 6, 0),
		pad(configItem, 6, 0, 6, 0),
		pad(groupSep(), 6, 8, 6, 8),
		pad(alignItem, 6, 0, 6, 0),
		pad(catalogItem, 6, 0, 6, 0),
		pad(solveItem, 6, 0, 6, 0),
		pad(groupSep(), 6, 8, 6, 8),
		pad(saveItem, 6, 0, 6, 0),
		pad(exportItem, 6, 0, 6, 0),
		pad(groupSep(), 6, 8, 6, 8),
		pad(debugItem, 6, 0, 6, 0),
		pad(previewItem, 6, 0, 8, 0),
		layout.NewSpacer(),
	)

	return container.NewBorder(nil, nil, accentLine, nil, buttons)
}

// ---------------------------------------------------------------------------
// Footer (bottom panel)
// ---------------------------------------------------------------------------

// footerCard builds a fixed-width data card:
// a 2 px crimson top rule, a section-title header row, and one or more value rows.
func footerCard(width float32, header fyne.CanvasObject, rows ...fyne.CanvasObject) fyne.CanvasObject {
	topRule := canvas.NewRectangle(colorCardTopRule)
	topRule.SetMinSize(fyne.NewSize(0, 2))

	children := []fyne.CanvasObject{topRule, pad(header, 1, 6, 0, 6)}
	for _, r := range rows {
		children = append(children, pad(r, 0, 6, 0, 6))
	}

	inner := container.New(&tightVBoxLayout{}, children...)
	card := container.NewStack(canvas.NewRectangle(colorCardBg), inner)
	return container.New(&fixedWidthLayout{width: width}, card)
}

// tightVBoxLayout is like VBox but with zero spacing between children.
type tightVBoxLayout struct{}

func (l *tightVBoxLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		h += ms.Height
	}
	return fyne.NewSize(w, h)
}

func (l *tightVBoxLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var y float32
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		ms := o.MinSize()
		o.Move(fyne.NewPos(0, y))
		o.Resize(fyne.NewSize(size.Width, ms.Height))
		y += ms.Height
	}
}

// cardHeader builds the header row for a footer card (title + optional settings button).
func cardHeader(title string, onSettings func()) fyne.CanvasObject {
	lbl := sectionTitle(title)
	if onSettings == nil {
		return lbl
	}
	btn := widget.NewButtonWithIcon("", theme.SettingsIcon(), onSettings)
	btn.Importance = widget.LowImportance
	tinyBtn := container.New(&fixedSizeLayout{size: fyne.NewSize(18, 16)}, btn)
	return container.NewBorder(nil, nil, lbl, tinyBtn)
}

// showCompactPopup shows a small popup near the bottom of the window instead
// of a centered dialog. Used for footer card settings.
func (w *Window) showCompactPopup(title string, content fyne.CanvasObject, width float32) {
	popContent := container.NewVBox(
		sectionTitle(title),
		hRule(),
		content,
	)
	pop := widget.NewPopUp(
		container.NewStack(
			canvas.NewRectangle(colorCardBg),
			pad(popContent, 8, 12, 8, 12),
		),
		w.win.Canvas(),
	)
	pop.Resize(fyne.NewSize(width, pop.MinSize().Height))
	// Position near bottom-center of window.
	winSize := w.win.Canvas().Size()
	popX := (winSize.Width - width) / 2
	popY := winSize.Height - pop.MinSize().Height - 100
	if popY < 50 {
		popY = 50
	}
	pop.Move(fyne.NewPos(popX, popY))
	pop.Show()
}

func (w *Window) buildFooter() fyne.CanvasObject {
	// STACK card
	w.lblStack = valueLabel("0/60")
	w.stackBar = newCrimsonProgressBar()
	barWrap := container.New(&fixedHeightLayout{height: 5}, w.stackBar)
	stackCard := footerCard(148,
		cardHeader("STACK", func() { w.showStackDepthPopup() }),
		w.lblStack,
		barWrap,
	)

	// HISTOGRAM card with cycling auto-stretch presets
	w.lblHistogram = valueLabel("B:0  G:1.0  W:255")
	w.autoPresetLbl = widget.NewLabel(autoPresets[0].Name)
	w.autoPresetLbl.Alignment = fyne.TextAlignCenter
	w.autoPresetLbl.TextStyle = fyne.TextStyle{Italic: true}

	applyPreset := func() {
		stacked := w.stacker.StackedFrames()
		if stacked == 0 {
			return
		}
		preset := autoPresets[w.autoPresetIdx]
		median, p95, maxVal, _ := w.stacker.AccumulatorStats()
		newBlack, newGamma, newWhite := preset.Apply(median, p95, maxVal)
		w.mu.Lock()
		w.black = newBlack
		w.gamma = newGamma
		w.white = newWhite
		w.mu.Unlock()
		w.addLog(fmt.Sprintf("Auto [%s] — B:%.0f G:%.2f W:%.0f", preset.Name, newBlack, newGamma, newWhite))
	}

	prevBtn := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		w.autoPresetIdx--
		if w.autoPresetIdx < 0 {
			w.autoPresetIdx = len(autoPresets) - 1
		}
		w.autoPresetLbl.SetText(autoPresets[w.autoPresetIdx].Name)
		applyPreset()
	})
	prevBtn.Importance = widget.LowImportance

	nextBtn := widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() {
		w.autoPresetIdx = (w.autoPresetIdx + 1) % len(autoPresets)
		w.autoPresetLbl.SetText(autoPresets[w.autoPresetIdx].Name)
		applyPreset()
	})
	nextBtn.Importance = widget.LowImportance

	applyBtn := widget.NewButtonWithIcon("Auto", theme.MediaReplayIcon(), func() {
		applyPreset()
	})
	applyBtn.Importance = widget.LowImportance

	presetRow := container.NewHBox(prevBtn, w.autoPresetLbl, nextBtn)
	histCard := footerCard(172,
		cardHeader("HISTOGRAM", func() { w.showHistogramPopup() }),
		w.lblHistogram,
		container.NewVBox(presetRow, applyBtn),
	)

	// ALIGNMENT card
	w.lblAlignment = valueLabel("0 det  0 match")
	alignCard := footerCard(162,
		cardHeader("ALIGNMENT", func() { w.showAlignmentPopup() }),
		w.lblAlignment,
	)

	// CAPTURE card (three readout lines)
	w.lblFPS = valueLabel("0.0 fps")
	w.lblElapsed = valueLabel("0s elapsed")
	w.lblIntTime = valueLabel("0s integration")
	captureCard := footerCard(164,
		cardHeader("CAPTURE", nil),
		w.lblFPS,
		w.lblElapsed,
		w.lblIntTime,
	)

	// STARFIELD card — detection status + locked target
	w.lblStarfield = valueLabel("No stars")
	w.lblStarfield.Wrapping = fyne.TextWrapWord
	starfieldCard := footerCard(180,
		cardHeader("STARFIELD", nil),
		w.lblStarfield,
	)

	// VIEW card
	w.lblMode = valueLabel("Average")
	viewCard := footerCard(130,
		cardHeader("VIEW", func() { w.showViewPopup() }),
		w.lblMode,
	)

	// 1 px top rule spanning the full footer width.
	topRule := canvas.NewRectangle(colorDivider)
	topRule.SetMinSize(fyne.NewSize(0, 1))

	// Cards grouped with consistent spacing, centered in the footer.
	cardRow := container.NewHBox(
		pad(stackCard, 0, 4, 0, 4),
		pad(histCard, 0, 4, 0, 4),
		pad(alignCard, 0, 4, 0, 4),
		pad(starfieldCard, 0, 4, 0, 4),
		pad(captureCard, 0, 4, 0, 4),
		pad(viewCard, 0, 4, 0, 4),
	)
	cards := container.NewHBox(layout.NewSpacer(), cardRow, layout.NewSpacer())

	footer := container.NewVBox(topRule, pad(cards, 2, 4, 2, 0))
	return container.New(&fixedHeightLayout{height: 88}, footer)
}

// ---------------------------------------------------------------------------
// Dialogs
// ---------------------------------------------------------------------------

func (w *Window) showConfigDialog() {
	devices := provider.DefaultListVideoDevices()

	modeOptions := []string{"webcam", "test"}
	modeSelect := widget.NewSelect(modeOptions, nil)
	modeSelect.Selected = w.config.Mode

	var cameraOptions []string
	cameraMap := map[string]string{} // label → device identifier (UID on macOS, Index elsewhere)
	for _, d := range devices {
		label := fmt.Sprintf("[%s] %s", d.Index, d.Name)
		cameraOptions = append(cameraOptions, label)
		// Prefer UID if available (native macOS), fall back to Index.
		if d.UID != "" {
			cameraMap[label] = d.UID
		} else {
			cameraMap[label] = d.Index
		}
	}
	if len(cameraOptions) == 0 {
		cameraOptions = []string{"(no cameras detected)"}
	}

	cameraSelect := widget.NewSelect(cameraOptions, nil)
	for _, opt := range cameraOptions {
		if idx, ok := cameraMap[opt]; ok && idx == w.config.Device {
			cameraSelect.Selected = opt
			break
		}
	}
	if cameraSelect.Selected == "" && len(cameraOptions) > 0 {
		cameraSelect.Selected = cameraOptions[0]
	}

	resOptions := []string{
		"640x480", "800x600", "1024x768", "1280x720", "1280x960", "1920x1080",
	}
	curRes := fmt.Sprintf("%dx%d", w.config.Width, w.config.Height)
	resSelect := widget.NewSelect(resOptions, nil)
	resSelect.Selected = curRes
	if resSelect.Selected == "" {
		resSelect.Selected = resOptions[0]
	}

	fpsEntry := widget.NewEntry()
	fpsEntry.SetText(fmt.Sprintf("%d", w.config.FPS))

	testDirEntry := widget.NewEntry()
	testDirEntry.SetText(w.config.TestDir)

	cameraRow := container.NewVBox(
		widget.NewLabel("Camera"), cameraSelect,
		widget.NewLabel("Resolution"), resSelect,
		widget.NewLabel("FPS"), fpsEntry,
	)
	testRow := container.NewVBox(
		widget.NewLabel("Image Directory"), testDirEntry,
	)

	updateVisibility := func(mode string) {
		if mode == "test" {
			cameraRow.Hide()
			testRow.Show()
		} else {
			cameraRow.Show()
			testRow.Hide()
		}
	}
	updateVisibility(modeSelect.Selected)
	modeSelect.OnChanged = updateVisibility

	ffmpegStatus := widget.NewLabel("")
	ffmpegStatus.TextStyle = fyne.TextStyle{Italic: true}
	if len(devices) > 0 {
		ffmpegStatus.SetText(fmt.Sprintf("ffmpeg: OK — %d camera(s) found", len(devices)))
	} else {
		ffmpegStatus.SetText("ffmpeg: not found or no cameras (use test mode)")
	}

	// --- Alignment settings ---
	maxStarsEntry := widget.NewEntry()
	maxStarsEntry.SetText(fmt.Sprintf("%d", w.maxStars))

	minMatchEntry := widget.NewEntry()
	minMatchEntry.SetText("3")

	alignSection := container.NewVBox(
		widget.NewLabel("Max Stars (detection limit)"), maxStarsEntry,
		widget.NewLabel("Min Match Threshold (quality gate)"), minMatchEntry,
	)

	// --- Calibration placeholders ---
	darkFrameEntry := widget.NewEntry()
	darkFrameEntry.SetPlaceHolder("Path to dark frame master (future)")
	darkFrameEntry.Disable()

	flatFrameEntry := widget.NewEntry()
	flatFrameEntry.SetPlaceHolder("Path to flat frame master (future)")
	flatFrameEntry.Disable()

	biasFrameEntry := widget.NewEntry()
	biasFrameEntry.SetPlaceHolder("Path to bias frame master (future)")
	biasFrameEntry.Disable()

	calibSection := container.NewVBox(
		widget.NewLabel("Calibration Frames"),
		darkFrameEntry, flatFrameEntry, biasFrameEntry,
	)

	// --- Save settings ---
	saveDirEntry := widget.NewEntry()
	home, _ := os.UserHomeDir()
	saveDirEntry.SetText(filepath.Join(home, "Desktop"))
	saveDirEntry.SetPlaceHolder("Default save directory")

	saveSection := container.NewVBox(
		widget.NewLabel("Save Directory"), saveDirEntry,
	)

	// --- Plate solving placeholder ---
	plateSolveCheck := widget.NewCheck("Enable plate solving (future)", nil)
	plateSolveCheck.Disable()

	// --- Assemble tabs ---
	tabs := container.NewAppTabs(
		container.NewTabItem("Capture", container.NewVBox(
			widget.NewLabel("Source Mode"), modeSelect,
			widget.NewSeparator(),
			cameraRow, testRow,
			widget.NewSeparator(),
			ffmpegStatus,
		)),
		container.NewTabItem("Alignment", container.NewVBox(
			alignSection,
			widget.NewSeparator(),
			widget.NewLabel("Higher max stars = more accurate but slower.\nMin match < 3 may stack misaligned frames."),
		)),
		container.NewTabItem("Calibration", container.NewVBox(
			calibSection,
			widget.NewSeparator(),
			widget.NewLabel("Dark/flat/bias subtraction coming soon."),
		)),
		container.NewTabItem("Output", container.NewVBox(
			saveSection,
			widget.NewSeparator(),
			plateSolveCheck,
		)),
	)

	d := dialog.NewCustomConfirm("Configuration", "Apply", "Cancel", tabs, func(apply bool) {
		if !apply {
			return
		}
		w.config.Mode = modeSelect.Selected
		if w.config.Mode == "webcam" {
			if sel, ok := cameraMap[cameraSelect.Selected]; ok {
				w.config.Device = sel
			}
			var rw, rh int
			fmt.Sscanf(resSelect.Selected, "%dx%d", &rw, &rh)
			if rw > 0 && rh > 0 {
				w.config.Width = rw
				w.config.Height = rh
			}
			var fps int
			fmt.Sscanf(fpsEntry.Text, "%d", &fps)
			if fps > 0 && fps <= 120 {
				w.config.FPS = fps
			}
			w.addLog(fmt.Sprintf("Config: webcam %s %dx%d@%dfps",
				w.config.Device, w.config.Width, w.config.Height, w.config.FPS))
		} else {
			w.config.TestDir = testDirEntry.Text
			w.addLog(fmt.Sprintf("Config: test mode dir=%s", w.config.TestDir))
		}

		// Apply alignment settings
		var ms int
		fmt.Sscanf(maxStarsEntry.Text, "%d", &ms)
		if ms >= 10 && ms <= 500 {
			w.maxStars = ms
			w.stacker.SetMaxStars(ms)
		}
		var mt int
		fmt.Sscanf(minMatchEntry.Text, "%d", &mt)
		if mt >= 1 && mt <= 20 {
			w.stacker.SetMinMatchThreshold(mt)
			w.addLog(fmt.Sprintf("Quality gate: min %d matches", mt))
		}

		w.restartCapture()
	}, w.win)
	d.Resize(fyne.NewSize(480, 480))
	d.Show()
}

func (w *Window) showStackDepthDialog() {
	depth := w.stacker.StackDepth()
	unlimited := depth == 0
	displayDepth := depth
	if unlimited {
		displayDepth = 60
	}

	// --- Mode selector ---
	modeNames := []string{"Average", "Maximum", "Median", "Sigma Clip"}
	modeValues := []engine.StackMode{engine.StackAverage, engine.StackMaximum, engine.StackMedian, engine.StackSigmaClip}
	currentMode := w.stacker.StackMode()
	currentModeName := currentMode.String()

	modeDescriptions := map[string]string{
		"Average":    "Sum all frames and divide by count.\nBest general-purpose mode — SNR improves with √N frames.",
		"Maximum":    "Keep the brightest pixel across all frames.\nReveals faint objects, good for star trails.",
		"Median":     "Per-pixel median value. Robust outlier rejection —\nremoves satellites, planes, and hot pixels.",
		"Sigma Clip": "Reject pixels >Nσ from the mean, then average.\nGold standard for deep-sky — removes transients while\npreserving faint signal.",
	}

	modeInfo := widget.NewLabel(modeDescriptions[currentModeName])
	modeInfo.Wrapping = fyne.TextWrapWord

	// Sigma slider — only visible for Sigma Clip mode.
	sigmaVal := w.stacker.SigmaClipVal()
	sigmaLabel := widget.NewLabel(fmt.Sprintf("Sigma: %.1f", sigmaVal))
	sigmaSlider := widget.NewSlider(0.5, 5.0)
	sigmaSlider.Value = sigmaVal
	sigmaSlider.Step = 0.1
	sigmaSlider.OnChanged = func(v float64) {
		w.stacker.SetSigmaClipVal(v)
		sigmaLabel.SetText(fmt.Sprintf("Sigma: %.1f", v))
	}
	sigmaRow := container.NewBorder(nil, nil, sigmaLabel, nil, sigmaSlider)
	if currentMode != engine.StackSigmaClip {
		sigmaRow.Hide()
	}

	modeSelect := widget.NewSelect(modeNames, func(selected string) {
		for i, name := range modeNames {
			if name == selected {
				w.stacker.SetStackMode(modeValues[i])
				modeInfo.SetText(modeDescriptions[selected])
				if modeValues[i] == engine.StackSigmaClip {
					sigmaRow.Show()
				} else {
					sigmaRow.Hide()
				}
				w.addLog(fmt.Sprintf("Stack mode: %s", selected))
				break
			}
		}
	})
	modeSelect.Selected = currentModeName

	// --- Depth controls ---
	depthLabel := widget.NewLabel(w.stackDepthText(depth))
	depthSlider := widget.NewSlider(10, 300)
	depthSlider.Value = float64(displayDepth)
	depthSlider.Step = 5

	unlimitedCheck := widget.NewCheck("Unlimited (accumulate forever)", nil)
	unlimitedCheck.Checked = unlimited
	if unlimited {
		depthSlider.Disable()
	}

	depthSlider.OnChanged = func(v float64) {
		n := int(v)
		w.stacker.SetStackDepth(n)
		depthLabel.SetText(w.stackDepthText(n))
		w.addLog(fmt.Sprintf("Stack depth: %d frames", n))
	}
	unlimitedCheck.OnChanged = func(checked bool) {
		if checked {
			depthSlider.Disable()
			w.stacker.SetStackDepth(0)
			depthLabel.SetText(w.stackDepthText(0))
			w.addLog("Stack depth: unlimited")
		} else {
			depthSlider.Enable()
			n := int(depthSlider.Value)
			w.stacker.SetStackDepth(n)
			depthLabel.SetText(w.stackDepthText(n))
			w.addLog(fmt.Sprintf("Stack depth: %d frames", n))
		}
	}

	// --- Display/stretch mode ---
	stretchNames := []string{"Linear", "Auto STF", "Asinh"}
	stretchValues := []engine.StretchMode{engine.StretchLinear, engine.StretchAutoSTF, engine.StretchAsinh}
	stretchDescriptions := map[string]string{
		"Linear":   "Manual Black/Gamma/White — full control via the histogram sliders.",
		"Auto STF": "Automatic Screen Transfer Function — analyses the image histogram\nto set optimal black point and midtone. Works on single loaded images.",
		"Asinh":    "Arcsinh stretch — compresses bright stars while revealing faint\nnebulosity. Great for loaded deep-sky images. Adjust β for intensity.",
	}
	currentStretch := w.stacker.StretchMode()

	stretchInfo := widget.NewLabel(stretchDescriptions[currentStretch.String()])
	stretchInfo.Wrapping = fyne.TextWrapWord

	betaVal := w.stacker.AsinhBeta()
	betaLabel := widget.NewLabel(fmt.Sprintf("Beta: %.0f", betaVal))
	betaSlider := widget.NewSlider(1, 500)
	betaSlider.Value = betaVal
	betaSlider.Step = 1
	betaSlider.OnChanged = func(v float64) {
		w.stacker.SetAsinhBeta(v)
		betaLabel.SetText(fmt.Sprintf("Beta: %.0f", v))
	}
	betaRow := container.NewBorder(nil, nil, betaLabel, nil, betaSlider)
	if currentStretch != engine.StretchAsinh {
		betaRow.Hide()
	}

	stretchSelect := widget.NewSelect(stretchNames, func(selected string) {
		for i, name := range stretchNames {
			if name == selected {
				w.stacker.SetStretchMode(stretchValues[i])
				stretchInfo.SetText(stretchDescriptions[selected])
				if stretchValues[i] == engine.StretchAsinh {
					betaRow.Show()
				} else {
					betaRow.Hide()
				}
				w.addLog(fmt.Sprintf("Display: %s", selected))
				break
			}
		}
	})
	stretchSelect.Selected = currentStretch.String()

	content := container.NewVBox(
		widget.NewLabel("Combine Mode"),
		modeSelect, modeInfo, sigmaRow,
		widget.NewSeparator(),
		widget.NewLabel("Display Mode"),
		stretchSelect, stretchInfo, betaRow,
		widget.NewSeparator(),
		widget.NewLabel("Rolling Stack Depth"),
		depthLabel, depthSlider, unlimitedCheck,
	)
	d := dialog.NewCustom("Stack Settings", "Close", content, w.win)
	d.Resize(fyne.NewSize(440, 540))
	d.Show()
}

func (w *Window) stackDepthText(depth int) string {
	if depth == 0 {
		return "Depth: Unlimited"
	}
	return fmt.Sprintf("Depth: %d frames (%.1fs integration)", depth, float64(depth)/targetFPS)
}

func (w *Window) showAlignmentDialog() {
	maxStarsEntry := widget.NewEntry()
	maxStarsEntry.SetText(fmt.Sprintf("%d", w.maxStars))

	infoLabel := widget.NewLabel("")
	infoLabel.Wrapping = fyne.TextWrapWord
	detected := w.stacker.LatestDetected()
	matches := w.stacker.LatestMatches()
	if detected > 0 {
		ratio := float64(matches) / float64(detected) * 100
		quality := "Poor"
		if ratio > 70 {
			quality = "Good"
		} else if ratio > 40 {
			quality = "Fair"
		}
		infoLabel.SetText(fmt.Sprintf(
			"Quality: %s (%.0f%% match rate)\nDetected: %d stars\nMatched: %d stars\n\nTip: If quality is poor, adjust focus until stars are tight points.",
			quality, ratio, detected, matches))
	} else {
		infoLabel.SetText("No frames processed yet.")
	}

	content := container.NewVBox(
		widget.NewLabel("Alignment Quality"),
		widget.NewSeparator(),
		infoLabel,
		widget.NewSeparator(),
		container.NewHBox(widget.NewLabel("Max Stars"), maxStarsEntry),
	)

	d := dialog.NewCustomConfirm("Alignment Settings", "Apply", "Cancel", content, func(apply bool) {
		if !apply {
			return
		}
		var n int
		fmt.Sscanf(maxStarsEntry.Text, "%d", &n)
		if n >= 10 && n <= 500 {
			w.maxStars = n
			w.stacker.SetMaxStars(n)
			w.addLog(fmt.Sprintf("Max stars set to %d", n))
		}
	}, w.win)
	d.Resize(fyne.NewSize(400, 350))
	d.Show()
}

func (w *Window) showImportDialog() {
	d := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil || reader == nil {
			return
		}
		reader.Close()
		filePath := reader.URI().Path()
		go w.importSingleImage(filePath)
	}, w.win)
	d.SetFilter(&imageFilter{})
	d.Resize(fyne.NewSize(600, 400))
	d.Show()
}

func (w *Window) importSingleImage(path string) {
	img, err := provider.LoadImage(path)
	if err != nil {
		w.addLog(fmt.Sprintf("Import error: %v", err))
		return
	}

	// Stop any active camera capture.
	w.stopCapture()

	// Set the image directly (bypasses async worker — no stacking race).
	w.stacker.Reset()
	w.stacker.SetImage(img)

	w.addLog(fmt.Sprintf("Imported: %s (%dx%d)", filepath.Base(path), img.Bounds().Dx(), img.Bounds().Dy()))

	// Start a display-only refresh loop so stretch/zoom/footers all work.
	w.startStaticDisplay()

	// Auto-solve the starfield against the catalog.
	go w.runPlateSolve()
}

// startStaticDisplay runs a lightweight refresh loop for imported/static images.
// It applies stretch, zoom, minimap, and updates all footer labels without
// reading from a provider.
func (w *Window) startStaticDisplay() {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.captureDone = make(chan struct{})
	w.startTime = time.Now()
	w.mu.Unlock()

	go func() {
		myDone := w.captureDone
		defer func() {
			if myDone != nil {
				close(myDone)
			}
		}()

		ticker := time.NewTicker(time.Second / 10) // 10 fps refresh for static
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
			curZoom := w.zoom
			w.mu.Unlock()

			stackedFrames := w.stacker.StackedFrames()
			if stackedFrames == 0 {
				continue
			}

			detected := w.stacker.LatestDetected()
			matches := w.stacker.LatestMatches()

			displayImg := w.stacker.GetDisplay(black, gamma, white)

			// Minimap before crop
			var minimapImg *image.NRGBA
			if displayImg != nil && curZoom > 1.0 {
				minimapImg = w.buildMinimapImage(displayImg)
			}
			if displayImg != nil {
				displayImg = w.applyViewport(displayImg)
			}

			// Star highlight overlay
			w.mu.Lock()
			hlActive := w.highlightActive
			hlX, hlY := w.highlightX, w.highlightY
			w.highlightAngle += 0.05
			hlAngle := w.highlightAngle
			w.mu.Unlock()
			if hlActive && displayImg != nil {
				drawRotatedSquare(displayImg, hlX, hlY, 24, hlAngle,
					color.NRGBA{R: 255, G: 0, B: 0, A: 255})
			}

			// Solve annotations
			w.mu.Lock()
			sLabels := w.solveLabels
			sFade := w.solveLabelFade
			w.mu.Unlock()
			if displayImg != nil && len(sLabels) > 0 && sFade > 0 {
				drawSolveAnnotations(displayImg, sLabels, w.projectToPixel, uint8(sFade*255))
			}

			stackText := fmt.Sprintf("%d (imported)", stackedFrames)
			histText := fmt.Sprintf("B:%.0f  G:%.1f  W:%.0f", black, gamma, white)
			alignText := fmt.Sprintf("%d det  %d match", detected, matches)

			var starfieldText string
			if detected == 0 {
				starfieldText = "No stars detected"
			} else {
				ratio := float64(matches) / float64(detected) * 100
				quality := "Poor"
				if ratio > 70 {
					quality = "Good"
				} else if ratio > 40 {
					quality = "Fair"
				}
				starfieldText = fmt.Sprintf("%s — %d stars", quality, detected)
			}
			if targetState.locked && targetState.object != nil {
				starfieldText += fmt.Sprintf("\nTarget: %s", targetState.objectName)
			}

			img := displayImg
			mmap := minimapImg
			fyne.Do(func() {
				if img != nil {
					w.img.Image = img
					w.img.Refresh()
				}
				if mmap != nil {
					w.minimap.Image = mmap
					w.minimap.Refresh()
					w.minimapPanel.Show()
				} else {
					w.minimapPanel.Hide()
				}
				w.lblStack.SetText(stackText)
				w.lblHistogram.SetText(histText)
				w.lblAlignment.SetText(alignText)
				w.lblStarfield.SetText(starfieldText)
				w.lblFPS.SetText("—")
				w.lblElapsed.SetText("imported")
				w.lblIntTime.SetText("—")
			})
		}
	}()
}

// imageFilter accepts common image file extensions in the file picker.
type imageFilter struct{}

func (f *imageFilter) Matches(uri fyne.URI) bool {
	ext := strings.ToLower(uri.Extension())
	switch ext {
	case ".png", ".jpg", ".jpeg", ".tif", ".tiff", ".bmp", ".gif", ".webp":
		return true
	}
	return false
}




// ---------------------------------------------------------------------------
// Compact footer popups (anchored near the bottom, not centered dialogs)
// ---------------------------------------------------------------------------

func (w *Window) showStackDepthPopup() {
	depth := w.stacker.StackDepth()
	displayDepth := depth
	if depth == 0 {
		displayDepth = 60
	}

	hintStyle := fyne.TextStyle{Italic: true, Monospace: true}

	// --- Combine mode ---
	modeNames := []string{"Average", "Maximum", "Median", "Sigma Clip"}
	modeValues := []engine.StackMode{engine.StackAverage, engine.StackMaximum, engine.StackMedian, engine.StackSigmaClip}
	modeHelp := map[string]string{
		"Average":    "Sum frames ÷ N — best general purpose, SNR ∝ √N",
		"Maximum":    "Brightest pixel — reveals faint objects, star trails",
		"Median":     "Middle value — removes satellites & hot pixels",
		"Sigma Clip": "Reject outliers >Nσ then average — deep sky gold standard",
	}
	currentMode := w.stacker.StackMode()

	modeHint := widget.NewLabel(modeHelp[currentMode.String()])
	modeHint.TextStyle = hintStyle
	modeHint.Wrapping = fyne.TextWrapWord

	sigmaVal := w.stacker.SigmaClipVal()
	sigmaLabel := valueLabel(fmt.Sprintf("σ: %.1f", sigmaVal))
	sigmaSlider := widget.NewSlider(0.5, 5.0)
	sigmaSlider.Value = sigmaVal
	sigmaSlider.Step = 0.1
	sigmaSlider.OnChanged = func(v float64) {
		w.stacker.SetSigmaClipVal(v)
		sigmaLabel.SetText(fmt.Sprintf("σ: %.1f", v))
	}
	sigmaRow := container.NewBorder(nil, nil, sigmaLabel, nil, sigmaSlider)
	if currentMode != engine.StackSigmaClip {
		sigmaRow.Hide()
	}

	modeSelect := widget.NewSelect(modeNames, func(selected string) {
		for i, name := range modeNames {
			if name == selected {
				w.stacker.SetStackMode(modeValues[i])
				w.updateViewLabel()
				modeHint.SetText(modeHelp[selected])
				if modeValues[i] == engine.StackSigmaClip {
					sigmaRow.Show()
				} else {
					sigmaRow.Hide()
				}
				w.addLog(fmt.Sprintf("Stack mode: %s", selected))
				break
			}
		}
	})
	modeSelect.Selected = currentMode.String()

	// --- Display/stretch mode ---
	stretchNames := []string{"Linear", "Auto STF", "Asinh"}
	stretchValues := []engine.StretchMode{engine.StretchLinear, engine.StretchAutoSTF, engine.StretchAsinh}
	stretchHelp := map[string]string{
		"Linear":   "Manual Black/Gamma/White — full control via histogram",
		"Auto STF": "Auto stretch — optimal black point & midtone from histogram",
		"Asinh":    "Arcsinh — reveals faint nebulosity, preserves star colours",
	}
	currentStretch := w.stacker.StretchMode()

	stretchHint := widget.NewLabel(stretchHelp[currentStretch.String()])
	stretchHint.TextStyle = hintStyle
	stretchHint.Wrapping = fyne.TextWrapWord

	betaVal := w.stacker.AsinhBeta()
	betaLabel := valueLabel(fmt.Sprintf("β: %.0f", betaVal))
	betaSlider := widget.NewSlider(1, 500)
	betaSlider.Value = betaVal
	betaSlider.Step = 1
	betaSlider.OnChanged = func(v float64) {
		w.stacker.SetAsinhBeta(v)
		betaLabel.SetText(fmt.Sprintf("β: %.0f", v))
	}
	betaRow := container.NewBorder(nil, nil, betaLabel, nil, betaSlider)
	if currentStretch != engine.StretchAsinh {
		betaRow.Hide()
	}

	stretchSelect := widget.NewSelect(stretchNames, func(selected string) {
		for i, name := range stretchNames {
			if name == selected {
				w.stacker.SetStretchMode(stretchValues[i])
				stretchHint.SetText(stretchHelp[selected])
				if stretchValues[i] == engine.StretchAsinh {
					betaRow.Show()
				} else {
					betaRow.Hide()
				}
				w.addLog(fmt.Sprintf("Display: %s", selected))
				break
			}
		}
	})
	stretchSelect.Selected = currentStretch.String()

	// --- Depth controls ---
	depthLabel := valueLabel(w.stackDepthText(depth))
	depthSlider := widget.NewSlider(10, 300)
	depthSlider.Value = float64(displayDepth)
	depthSlider.Step = 5

	unlimitedCheck := widget.NewCheck("Unlimited", nil)
	unlimitedCheck.Checked = depth == 0
	if depth == 0 {
		depthSlider.Disable()
	}

	depthSlider.OnChanged = func(v float64) {
		n := int(v)
		w.stacker.SetStackDepth(n)
		depthLabel.SetText(w.stackDepthText(n))
	}
	unlimitedCheck.OnChanged = func(checked bool) {
		if checked {
			depthSlider.Disable()
			w.stacker.SetStackDepth(0)
			depthLabel.SetText(w.stackDepthText(0))
		} else {
			depthSlider.Enable()
			n := int(depthSlider.Value)
			w.stacker.SetStackDepth(n)
			depthLabel.SetText(w.stackDepthText(n))
		}
	}

	combineHeader := widget.NewLabel("COMBINE")
	combineHeader.TextStyle = fyne.TextStyle{Bold: true}
	displayHeader := widget.NewLabel("DISPLAY")
	displayHeader.TextStyle = fyne.TextStyle{Bold: true}
	depthHeader := widget.NewLabel("DEPTH")
	depthHeader.TextStyle = fyne.TextStyle{Bold: true}

	combineNote := widget.NewLabel("Combine modes apply to live capture stacking only")
	combineNote.TextStyle = fyne.TextStyle{Italic: true}
	combineNote.Wrapping = fyne.TextWrapWord

	content := container.NewVBox(
		combineHeader, modeSelect, combineNote, modeHint, sigmaRow,
		widget.NewSeparator(),
		displayHeader, stretchSelect, stretchHint, betaRow,
		widget.NewSeparator(),
		depthHeader, depthLabel, depthSlider, unlimitedCheck,
	)
	w.showCompactPopup("STACK", content, 340)
}

func (w *Window) showHistogramPopup() {
	w.mu.Lock()
	curBlack := w.black
	curGamma := w.gamma
	curWhite := w.white
	w.mu.Unlock()

	hintStyle := fyne.TextStyle{Italic: true}

	blackLabel := valueLabel(fmt.Sprintf("Black: %.0f", curBlack))
	blackHint := widget.NewLabel("Clip dark noise — raise to hide background glow")
	blackHint.TextStyle = hintStyle
	blackHint.Wrapping = fyne.TextWrapWord
	blackSlider := widget.NewSlider(0, 100)
	blackSlider.Value = curBlack
	blackSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.black = v
		w.mu.Unlock()
		blackLabel.SetText(fmt.Sprintf("Black: %.0f", v))
	}

	gammaLabel := valueLabel(fmt.Sprintf("Gamma: %.2f", curGamma))
	gammaHint := widget.NewLabel("Midtone curve — lower brightens, higher darkens")
	gammaHint.TextStyle = hintStyle
	gammaHint.Wrapping = fyne.TextWrapWord
	gammaSlider := widget.NewSlider(0.1, 5.0)
	gammaSlider.Value = curGamma
	gammaSlider.Step = 0.05
	gammaSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.gamma = v
		w.mu.Unlock()
		gammaLabel.SetText(fmt.Sprintf("Gamma: %.2f", v))
	}

	whiteLabel := valueLabel(fmt.Sprintf("White: %.0f", curWhite))
	whiteHint := widget.NewLabel("Highlight ceiling — lower to boost faint detail")
	whiteHint.TextStyle = hintStyle
	whiteHint.Wrapping = fyne.TextWrapWord
	whiteSlider := widget.NewSlider(1, 500)
	whiteSlider.Value = curWhite
	whiteSlider.OnChanged = func(v float64) {
		w.mu.Lock()
		w.white = v
		w.mu.Unlock()
		whiteLabel.SetText(fmt.Sprintf("White: %.0f", v))
	}

	dialogPresetLbl := widget.NewLabel(autoPresets[w.autoPresetIdx].Name)
	dialogPresetLbl.Alignment = fyne.TextAlignCenter
	dialogPresetLbl.TextStyle = fyne.TextStyle{Italic: true}

	applyDialogPreset := func() {
		stacked := w.stacker.StackedFrames()
		if stacked == 0 {
			return
		}
		preset := autoPresets[w.autoPresetIdx]
		median, p95, maxVal, _ := w.stacker.AccumulatorStats()
		newBlack, newGamma, newWhite := preset.Apply(median, p95, maxVal)
		w.mu.Lock()
		w.black = newBlack
		w.gamma = newGamma
		w.white = newWhite
		w.mu.Unlock()
		blackSlider.SetValue(newBlack)
		gammaSlider.SetValue(newGamma)
		whiteSlider.SetValue(newWhite)
		blackLabel.SetText(fmt.Sprintf("Black: %.0f", newBlack))
		gammaLabel.SetText(fmt.Sprintf("Gamma: %.2f", newGamma))
		whiteLabel.SetText(fmt.Sprintf("White: %.0f", newWhite))
		w.addLog(fmt.Sprintf("Auto [%s] — B:%.0f G:%.2f W:%.0f", preset.Name, newBlack, newGamma, newWhite))
		if w.autoPresetLbl != nil {
			w.autoPresetLbl.SetText(preset.Name)
		}
	}

	prevDialogBtn := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		w.autoPresetIdx--
		if w.autoPresetIdx < 0 {
			w.autoPresetIdx = len(autoPresets) - 1
		}
		dialogPresetLbl.SetText(autoPresets[w.autoPresetIdx].Name)
		applyDialogPreset()
	})
	prevDialogBtn.Importance = widget.LowImportance

	nextDialogBtn := widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() {
		w.autoPresetIdx = (w.autoPresetIdx + 1) % len(autoPresets)
		dialogPresetLbl.SetText(autoPresets[w.autoPresetIdx].Name)
		applyDialogPreset()
	})
	nextDialogBtn.Importance = widget.LowImportance

	autoBtn := widget.NewButton("Apply", func() {
		applyDialogPreset()
	})
	autoPresetRow := container.NewHBox(prevDialogBtn, dialogPresetLbl, nextDialogBtn)

	content := container.NewVBox(
		blackLabel, blackHint, blackSlider,
		gammaLabel, gammaHint, gammaSlider,
		whiteLabel, whiteHint, whiteSlider,
		autoPresetRow, autoBtn,
	)
	w.showCompactPopup("HISTOGRAM", content, 360)
}

func (w *Window) showAlignmentPopup() {
	detected := w.stacker.LatestDetected()
	matches := w.stacker.LatestMatches()

	info := "No frames processed yet."
	if detected > 0 {
		ratio := float64(matches) / float64(detected) * 100
		quality := "Poor"
		if ratio > 70 {
			quality = "Good"
		} else if ratio > 40 {
			quality = "Fair"
		}
		info = fmt.Sprintf("%s (%.0f%%) — %d detected, %d matched", quality, ratio, detected, matches)
	}

	infoLabel := valueLabel(info)
	infoLabel.Wrapping = fyne.TextWrapWord

	content := container.NewVBox(infoLabel)
	w.showCompactPopup("ALIGNMENT", content, 340)
}

func (w *Window) showViewPopup() {
	w.mu.Lock()
	curZoom := w.zoom
	w.mu.Unlock()

	zoomLabel := valueLabel(fmt.Sprintf("Zoom: %.1fx", curZoom))

	resetFillBtn := widget.NewButton("Reset to Fill", func() {
		w.mu.Lock()
		w.zoom = 1.0
		w.panX = 0
		w.panY = 0
		w.mu.Unlock()
		w.updateViewLabel()
		zoomLabel.SetText("Zoom: 1.0x")
	})
	resetFillBtn.Importance = widget.MediumImportance

	resetExactBtn := widget.NewButton("1:1 Pixel", func() {
		w.mu.Lock()
		if w.imgW > 0 {
			widgetW := float64(w.img.Size().Width)
			imgW := float64(w.imgW)
			w.zoom = imgW / widgetW
			if w.zoom < 1.0 {
				w.zoom = 1.0
			}
		}
		w.panX = 0
		w.panY = 0
		z := w.zoom
		w.mu.Unlock()
		w.updateViewLabel()
		zoomLabel.SetText(fmt.Sprintf("Zoom: %.1fx", z))
	})
	resetExactBtn.Importance = widget.MediumImportance

	content := container.NewVBox(
		zoomLabel,
		container.NewGridWithColumns(2, resetFillBtn, resetExactBtn),
	)
	w.showCompactPopup("VIEW", content, 280)
}

// ---------------------------------------------------------------------------
// Tappable overlay (click-to-identify)
// ---------------------------------------------------------------------------

type tappableOverlay struct {
	widget.BaseWidget
	onTap    func(*fyne.PointEvent)
	onScroll func(*fyne.ScrollEvent)
	onDrag   func(*fyne.DragEvent)
}

func newTappableOverlay(onTap func(*fyne.PointEvent)) *tappableOverlay {
	t := &tappableOverlay{onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tappableOverlay) Tapped(ev *fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap(ev)
	}
}

func (t *tappableOverlay) Scrolled(ev *fyne.ScrollEvent) {
	if t.onScroll != nil {
		t.onScroll(ev)
	}
}

func (t *tappableOverlay) Dragged(ev *fyne.DragEvent) {
	if t.onDrag != nil {
		t.onDrag(ev)
	}
}

func (t *tappableOverlay) DragEnd() {}

func (t *tappableOverlay) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

func (w *Window) canvasPosToImagePixel(pos fyne.Position) (int, int, bool) {
	if w.img.Image == nil {
		return 0, 0, false
	}
	imgBounds := w.img.Image.Bounds()
	imgW := float64(imgBounds.Dx())
	imgH := float64(imgBounds.Dy())
	if imgW == 0 || imgH == 0 {
		return 0, 0, false
	}

	widgetW := float64(w.img.Size().Width)
	widgetH := float64(w.img.Size().Height)

	scaleX := widgetW / imgW
	scaleY := widgetH / imgH
	scale := scaleX
	if scaleY < scaleX {
		scale = scaleY
	}

	displayW := imgW * scale
	displayH := imgH * scale
	offsetX := (widgetW - displayW) / 2
	offsetY := (widgetH - displayH) / 2

	px := (float64(pos.X) - offsetX) / scale
	py := (float64(pos.Y) - offsetY) / scale

	if px < 0 || py < 0 || px >= imgW || py >= imgH {
		return 0, 0, false
	}
	return int(px), int(py), true
}

func (w *Window) handleImageTap(ev *fyne.PointEvent) {
	px, py, ok := w.canvasPosToImagePixel(ev.Position)
	if !ok {
		return
	}

	stars := w.stacker.LatestStars()
	if len(stars) == 0 {
		w.addLog(fmt.Sprintf("Tap (%d,%d) — no stars detected", px, py))
		return
	}

	bestDist := 15.0
	bestIdx := -1
	for i, s := range stars {
		dx := s.X - float64(px)
		dy := s.Y - float64(py)
		d := math.Sqrt(dx*dx + dy*dy)
		if d < bestDist {
			bestDist = d
			bestIdx = i
		}
	}

	if bestIdx < 0 {
		w.addLog(fmt.Sprintf("Tap (%d,%d) — no star nearby", px, py))
		w.mu.Lock()
		w.highlightActive = false
		w.mu.Unlock()
		return
	}

	star := stars[bestIdx]
	w.mu.Lock()
	w.highlightActive = true
	w.highlightX = star.X
	w.highlightY = star.Y
	w.highlightAngle = 0
	w.mu.Unlock()

	info := fmt.Sprintf("Star at (%.0f,%.0f) brightness=%.0f", star.X, star.Y, star.Brightness)

	// If we have a solve, convert pixel to RA/Dec and search the catalog
	ra, dec, solved := w.pixelToRADec(star.X, star.Y)
	if solved && w.store != nil {
		// Search a small radius around the position (0.5° cone)
		searchR := 0.5
		cosDec := math.Cos(dec * math.Pi / 180)
		if cosDec < 0.1 {
			cosDec = 0.1
		}
		results, err := w.store.SearchByCoords(
			ra-searchR/cosDec, ra+searchR/cosDec,
			dec-searchR, dec+searchR,
			5,
		)
		if err == nil && len(results) > 0 {
			// Find the closest match
			bestObj := results[0]
			bestObjDist := math.Abs(bestObj.RADeg-ra) + math.Abs(bestObj.DecDeg-dec)
			for _, obj := range results[1:] {
				d := math.Abs(obj.RADeg-ra) + math.Abs(obj.DecDeg-dec)
				if d < bestObjDist {
					bestObj = obj
					bestObjDist = d
				}
			}
			obj := bestObj

			name := obj.Name
			if name == "" {
				name = obj.CatalogID
			}
			info = fmt.Sprintf("%s (mag %.1f)", name, obj.Magnitude)

			// Build detail popup
			raH := obj.RADeg / 15.0
			raMin := (raH - float64(int(raH))) * 60
			decD := int(obj.DecDeg)
			decMin := (obj.DecDeg - float64(decD)) * 60
			if decMin < 0 {
				decMin = -decMin
			}

			details := fmt.Sprintf("Name: %s\nCatalog: %s\nType: %s\nMagnitude: %.2f",
				obj.Name, obj.CatalogID, obj.Type, obj.Magnitude)
			details += fmt.Sprintf("\nRA: %dh %02.1fm\nDec: %+d° %02.1f'",
				int(raH), raMin, decD, decMin)
			if obj.SizeArcMin > 0 {
				details += fmt.Sprintf("\nSize: %.1f arcmin", obj.SizeArcMin)
			}
			if obj.Description != "" {
				details += fmt.Sprintf("\n\n%s", obj.Description)
			}

			title := name
			if obj.Type != "" {
				title = fmt.Sprintf("%s — %s", name, obj.Type)
			}

			go func() {
				dlg := dialog.NewInformation(title, details, w.win)
				dlg.Show()
			}()
		}
	}
	w.addLog(info)
}

// applyViewport crops and scales the source image based on current zoom/pan.
// Returns the cropped image, or the original if zoom == 1.
func (w *Window) applyViewport(src *image.NRGBA) *image.NRGBA {
	w.mu.Lock()
	zoom := w.zoom
	panX := w.panX
	panY := w.panY
	w.imgW = src.Bounds().Dx()
	w.imgH = src.Bounds().Dy()
	w.mu.Unlock()

	if zoom <= 1.0 {
		return src
	}

	srcW := float64(src.Bounds().Dx())
	srcH := float64(src.Bounds().Dy())

	// Viewport size in source pixels
	viewW := srcW / zoom
	viewH := srcH / zoom

	// Center + pan
	cx := srcW/2 + panX
	cy := srcH/2 + panY

	x0 := cx - viewW/2
	y0 := cy - viewH/2

	// Clamp to image bounds
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x0+viewW > srcW {
		x0 = srcW - viewW
	}
	if y0+viewH > srcH {
		y0 = srcH - viewH
	}

	ix0 := int(x0)
	iy0 := int(y0)
	ix1 := ix0 + int(viewW)
	iy1 := iy0 + int(viewH)

	if ix1 > src.Bounds().Dx() {
		ix1 = src.Bounds().Dx()
	}
	if iy1 > src.Bounds().Dy() {
		iy1 = src.Bounds().Dy()
	}

	cropped := image.NewNRGBA(image.Rect(0, 0, ix1-ix0, iy1-iy0))
	for y := iy0; y < iy1; y++ {
		srcOff := y*src.Stride + ix0*4
		dstOff := (y - iy0) * cropped.Stride
		copy(cropped.Pix[dstOff:dstOff+(ix1-ix0)*4], src.Pix[srcOff:srcOff+(ix1-ix0)*4])
	}
	return cropped
}

// buildMinimapImage creates a small thumbnail of the full image with a red
// rectangle showing the current viewport.
func (w *Window) buildMinimapImage(src *image.NRGBA) *image.NRGBA {
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	if srcW == 0 || srcH == 0 {
		return nil
	}

	// Thumbnail size: 140px wide, proportional height.
	thumbW := 140
	thumbH := thumbW * srcH / srcW
	if thumbH < 20 {
		thumbH = 20
	}

	thumb := image.NewNRGBA(image.Rect(0, 0, thumbW, thumbH))
	scaleX := float64(srcW) / float64(thumbW)
	scaleY := float64(srcH) / float64(thumbH)

	// Downsample with nearest-neighbor (fast).
	for y := 0; y < thumbH; y++ {
		sy := int(float64(y) * scaleY)
		if sy >= srcH {
			sy = srcH - 1
		}
		for x := 0; x < thumbW; x++ {
			sx := int(float64(x) * scaleX)
			if sx >= srcW {
				sx = srcW - 1
			}
			off := sy*src.Stride + sx*4
			doff := y*thumb.Stride + x*4
			copy(thumb.Pix[doff:doff+4], src.Pix[off:off+4])
		}
	}

	// Draw viewport rectangle.
	w.mu.Lock()
	zoom := w.zoom
	panX := w.panX
	panY := w.panY
	w.mu.Unlock()

	if zoom > 1.0 {
		viewW := float64(srcW) / zoom
		viewH := float64(srcH) / zoom
		cx := float64(srcW)/2 + panX
		cy := float64(srcH)/2 + panY
		x0 := (cx - viewW/2) / float64(srcW) * float64(thumbW)
		y0 := (cy - viewH/2) / float64(srcH) * float64(thumbH)
		x1 := (cx + viewW/2) / float64(srcW) * float64(thumbW)
		y1 := (cy + viewH/2) / float64(srcH) * float64(thumbH)

		red := color.NRGBA{R: 255, G: 60, B: 60, A: 255}
		drawRect(thumb, int(x0), int(y0), int(x1), int(y1), red)
	}

	return thumb
}

// drawRect draws a 1px outline rectangle on an NRGBA image.
func drawRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	b := img.Bounds()
	for x := x0; x <= x1; x++ {
		if x >= b.Min.X && x < b.Max.X {
			if y0 >= b.Min.Y && y0 < b.Max.Y {
				img.SetNRGBA(x, y0, c)
			}
			if y1 >= b.Min.Y && y1 < b.Max.Y {
				img.SetNRGBA(x, y1, c)
			}
		}
	}
	for y := y0; y <= y1; y++ {
		if y >= b.Min.Y && y < b.Max.Y {
			if x0 >= b.Min.X && x0 < b.Max.X {
				img.SetNRGBA(x0, y, c)
			}
			if x1 >= b.Min.X && x1 < b.Max.X {
				img.SetNRGBA(x1, y, c)
			}
		}
	}
}

func (w *Window) updateViewLabel() {
	w.mu.Lock()
	zoom := w.zoom
	imgW := w.imgW
	w.mu.Unlock()

	mode := w.stacker.StackMode().String()
	if w.showPreview {
		mode = "Preview"
	}

	if zoom <= 1.0 || imgW == 0 {
		fyne.Do(func() { w.lblMode.SetText(mode) })
		return
	}

	cropPct := 100.0 / zoom
	fyne.Do(func() {
		w.lblMode.SetText(fmt.Sprintf("%s\n%.0fx (%.0f%%)", mode, zoom, cropPct))
	})
}

// ---------------------------------------------------------------------------
// Save
// ---------------------------------------------------------------------------

func (w *Window) quickSave() {
	stacked := w.stacker.StackedFrames()
	if stacked == 0 {
		w.addLog("Nothing to save — stack is empty")
		return
	}

	w.mu.Lock()
	img := w.stacker.GetDisplay(w.black, w.gamma, w.white)
	w.mu.Unlock()
	if img == nil {
		return
	}

	ts := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("photonforge_%s.png", ts)
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "Desktop", filename)

	f, err := os.Create(path)
	if err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	defer f.Close()

	if err := imagepng.Encode(f, img); err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	w.addLog(fmt.Sprintf("Saved: %s", path))
}

func (w *Window) showSaveDialog() {
	stacked := w.stacker.StackedFrames()
	if stacked == 0 {
		w.addLog("Nothing to save — stack is empty")
		return
	}

	w.mu.Lock()
	black, gamma, white := w.black, w.gamma, w.white
	w.mu.Unlock()

	formats := []string{"PNG (stretched)", "TIFF 16-bit (stretched)", "TIFF 16-bit (linear)", "PNG sequence"}
	formatSelect := widget.NewSelect(formats, nil)
	formatSelect.Selected = formats[0]

	content := container.NewVBox(
		widget.NewLabel("Export Format"),
		formatSelect,
		widget.NewLabel(fmt.Sprintf("Stack: %d frames", stacked)),
	)

	d := dialog.NewCustomConfirm("Save Stack", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		go func() {
			ts := time.Now().Format("20060102_150405")
			home, _ := os.UserHomeDir()
			desktop := filepath.Join(home, "Desktop")

			switch formatSelect.Selected {
			case "PNG (stretched)":
				img := w.stacker.GetDisplay(black, gamma, white)
				if img == nil {
					return
				}
				path := filepath.Join(desktop, fmt.Sprintf("photonforge_%s.png", ts))
				w.saveImagePNG(path, img)

			case "TIFF 16-bit (stretched)":
				img16 := w.stacker.GetDisplay16(black, gamma, white)
				if img16 == nil {
					return
				}
				path := filepath.Join(desktop, fmt.Sprintf("photonforge_%s.tiff", ts))
				w.saveImageTIFF16(path, img16)

			case "TIFF 16-bit (linear)":
				img16 := w.stacker.GetDisplay16(0, 1.0, 255)
				if img16 == nil {
					return
				}
				path := filepath.Join(desktop, fmt.Sprintf("photonforge_%s_linear.tiff", ts))
				w.saveImageTIFF16(path, img16)

			case "PNG sequence":
				dir := filepath.Join(desktop, fmt.Sprintf("photonforge_%s_seq", ts))
				os.MkdirAll(dir, 0755)
				img := w.stacker.GetDisplay(black, gamma, white)
				if img == nil {
					return
				}
				path := filepath.Join(dir, fmt.Sprintf("frame_%04d.png", stacked))
				w.saveImagePNG(path, img)
				w.addLog(fmt.Sprintf("Sequence saved to %s", dir))
			}
		}()
	}, w.win)
	d.Resize(fyne.NewSize(360, 250))
	d.Show()
}

func (w *Window) saveImagePNG(path string, img *image.NRGBA) {
	f, err := os.Create(path)
	if err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	defer f.Close()
	if err := imagepng.Encode(f, img); err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	w.addLog(fmt.Sprintf("Saved: %s", path))
}

func (w *Window) saveImageTIFF(path string, img *image.NRGBA) {
	f, err := os.Create(path)
	if err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	defer f.Close()
	if err := tiffEncode(f, img); err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	w.addLog(fmt.Sprintf("Saved: %s", path))
}

func (w *Window) saveImageTIFF16(path string, img *image.NRGBA64) {
	f, err := os.Create(path)
	if err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	defer f.Close()
	if err := tiffEncode(f, img); err != nil {
		w.addLog(fmt.Sprintf("Save error: %v", err))
		return
	}
	w.addLog(fmt.Sprintf("Saved 16-bit: %s", path))
}

// tiffEncode writes an image as TIFF with deflate compression.
func tiffEncode(w io.Writer, img image.Image) error {
	return tiff.Encode(w, img, &tiff.Options{Compression: tiff.Deflate})
}

// ---------------------------------------------------------------------------
// Objects of interest dialog
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// Image overlay helpers
// ---------------------------------------------------------------------------

func drawRotatedSquare(img *image.NRGBA, cx, cy float64, size int, angle float64, c color.NRGBA) {
	b := img.Bounds()
	half := float64(size) / 2.0
	cosA := math.Cos(angle)
	sinA := math.Sin(angle)

	corners := [4][2]float64{
		{-half, -half}, {half, -half}, {half, half}, {-half, half},
	}
	var rotated [4][2]float64
	for i, corner := range corners {
		rotated[i][0] = cx + corner[0]*cosA - corner[1]*sinA
		rotated[i][1] = cy + corner[0]*sinA + corner[1]*cosA
	}
	for i := 0; i < 4; i++ {
		x0, y0 := int(rotated[i][0]), int(rotated[i][1])
		x1, y1 := int(rotated[(i+1)%4][0]), int(rotated[(i+1)%4][1])
		dx := math.Abs(float64(x1 - x0))
		dy := math.Abs(float64(y1 - y0))
		steps := int(math.Max(dx, dy))
		if steps == 0 {
			continue
		}
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			x := int(float64(x0)*(1-t) + float64(x1)*t)
			y := int(float64(y0)*(1-t) + float64(y1)*t)
			if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Capture loop
// ---------------------------------------------------------------------------

func (w *Window) captureLoop() {
	// Capture the done channel at entry — if restartCapture() replaces
	// w.captureDone before our defer runs, we must close the original,
	// not the replacement.
	w.mu.Lock()
	myDone := w.captureDone
	w.mu.Unlock()
	defer func() {
		if myDone != nil {
			close(myDone)
		}
	}()

	prov := w.config.CreateProvider()
	if err := prov.Open(); err != nil {
		w.addLog(fmt.Sprintf("Error: %v", err))
		fyne.Do(func() {
			w.lblStack.SetText("ERR")
			dialog.ShowError(fmt.Errorf("failed to open source: %w", err), w.win)
		})
		return
	}
	w.mu.Lock()
	w.activeProvider = prov
	w.mu.Unlock()
	defer func() {
		prov.Close()
		w.mu.Lock()
		w.activeProvider = nil
		w.mu.Unlock()
	}()

	w.addLog(fmt.Sprintf("Capturing: %s", w.sourceLabel()))

	ticker := time.NewTicker(time.Second / targetFPS)
	defer ticker.Stop()

	frameCounter := 0
	fpsTimer := time.Now()
	bufferFull := false

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

		frame := prov.Read()
		if frame == nil {
			w.addLog("Source ended — no more frames")
			fyne.Do(func() { w.lblStack.SetText("END") })
			return
		}

		w.stacker.Submit(frame)
		frameCounter++

		if time.Since(fpsTimer) >= time.Second {
			w.lastFPS = float64(frameCounter) / time.Since(fpsTimer).Seconds()
			frameCounter = 0
			fpsTimer = time.Now()
		}

		stackedFrames := w.stacker.StackedFrames()
		stackDepth := w.stacker.StackDepth()
		matches := w.stacker.LatestMatches()
		detected := w.stacker.LatestDetected()
		elapsed := time.Since(w.startTime)
		intTime := time.Duration(float64(stackedFrames) / targetFPS * float64(time.Second))

		if stackDepth > 0 && stackedFrames >= stackDepth && !bufferFull {
			bufferFull = true
			w.addLog(fmt.Sprintf("Buffer full — live stack running (%d frames)", stackDepth))
		}

		var bufferFill float64
		if stackDepth > 0 {
			bufferFill = float64(stackedFrames) / float64(stackDepth)
		} else if stackedFrames > 0 {
			bufferFill = 1.0
		}

		var displayImg *image.NRGBA
		if showPreview {
			displayImg = w.stacker.GetPreview()
		} else if stackedFrames > 0 {
			displayImg = w.stacker.GetDisplay(black, gamma, white)
		}
		// Generate minimap before cropping, then apply viewport.
		var minimapImg *image.NRGBA
		w.mu.Lock()
		curZoom := w.zoom
		w.mu.Unlock()
		if displayImg != nil && curZoom > 1.0 {
			minimapImg = w.buildMinimapImage(displayImg)
		}
		if displayImg != nil {
			displayImg = w.applyViewport(displayImg)
		}

		w.mu.Lock()
		hlActive := w.highlightActive
		hlX, hlY := w.highlightX, w.highlightY
		w.highlightAngle += 0.05
		hlAngle := w.highlightAngle
		w.mu.Unlock()
		if hlActive && displayImg != nil {
			drawRotatedSquare(displayImg, hlX, hlY, 24, hlAngle,
				color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}

		// Solve annotations
		w.mu.Lock()
		sLabels := w.solveLabels
		sFade := w.solveLabelFade
		w.mu.Unlock()
		if displayImg != nil && len(sLabels) > 0 && sFade > 0 {
			drawSolveAnnotations(displayImg, sLabels, w.projectToPixel, uint8(sFade*255))
		}

		var stackText string
		if stackDepth > 0 {
			stackText = fmt.Sprintf("%d / %d", stackedFrames, stackDepth)
		} else {
			stackText = fmt.Sprintf("%d", stackedFrames)
		}
		histText := fmt.Sprintf("B:%.0f  G:%.1f  W:%.0f", black, gamma, white)
		alignText := fmt.Sprintf("%d det  %d match", detected, matches)
		fpsText := fmt.Sprintf("%.1f fps", w.lastFPS)
		elapsedText := formatDuration(elapsed) + " elapsed"
		intText := formatDuration(intTime) + " integration"

		// Starfield status text.
		var starfieldText string
		if detected == 0 {
			starfieldText = "No stars detected"
		} else {
			ratio := 0.0
			if detected > 0 {
				ratio = float64(matches) / float64(detected) * 100
			}
			quality := "Poor"
			if ratio > 70 {
				quality = "Good"
			} else if ratio > 40 {
				quality = "Fair"
			}
			starfieldText = fmt.Sprintf("%s — %d stars", quality, detected)
		}
		if targetState.locked && targetState.object != nil {
			starfieldText += fmt.Sprintf("\nTarget: %s", targetState.objectName)
		}

		img := displayImg
		fill := bufferFill
		mmap := minimapImg
		fyne.Do(func() {
			if img != nil {
				w.img.Image = img
				w.img.Refresh()
			}
			// Minimap visibility
			if mmap != nil {
				w.minimap.Image = mmap
				w.minimap.Refresh()
				w.minimapPanel.Show()
			} else {
				w.minimapPanel.Hide()
			}
			w.lblStack.SetText(stackText)
			w.stackBar.SetValue(fill)
			w.lblHistogram.SetText(histText)
			w.lblAlignment.SetText(alignText)
			w.lblStarfield.SetText(starfieldText)
			w.lblFPS.SetText(fpsText)
			w.lblElapsed.SetText(elapsedText)
			w.lblIntTime.SetText(intText)
		})
	}
}

// runPlateSolve attempts to identify the star field by matching detected stars
// against the HYG catalog using triangle pattern matching.
func (w *Window) runPlateSolve() {
	stars := w.stacker.LatestStars()
	if len(stars) < 4 {
		w.addLog("Solve: need at least 4 detected stars")
		return
	}

	if w.store == nil {
		w.addLog("Solve: no catalog database loaded")
		return
	}

	w.addLog(fmt.Sprintf("Solving starfield (%d stars detected)...", len(stars)))

	// Fetch brightest catalog stars for matching
	catObjs, err := w.store.BrightStars(12.0, 5000)
	if err != nil {
		w.addLog(fmt.Sprintf("Solve error: %v", err))
		return
	}
	if len(catObjs) < 10 {
		w.addLog(fmt.Sprintf("Solve: catalog too small (%d stars) — run ingest first", len(catObjs)))
		return
	}

	w.addLog(fmt.Sprintf("Solve: %d catalog stars loaded, building sky index...", len(catObjs)))

	// Convert to solver format
	catalog := make([]engine.CatalogStar, len(catObjs))
	for i, o := range catObjs {
		catalog[i] = engine.CatalogStar{
			RA:        o.RADeg,
			Dec:       o.DecDeg,
			Magnitude: o.Magnitude,
			Name:      o.Name,
			CatalogID: o.CatalogID,
		}
	}

	solver := engine.NewPlateSolver(catalog)
	w.addLog(fmt.Sprintf("Solve: built %d sky tiles, matching...", len(solver.Catalog)))

	// Try common FOVs for various telescope/camera combos
	result := solver.Solve(stars, []float64{0.5, 1.0, 2.0, 3.0, 5.0, 8.0, 12.0, 20.0, 40.0})

	if !result.Solved {
		w.addLog("Solve: no match found — try with more stars or check catalog")
		return
	}

	raH := result.CenterRA / 15.0
	raMin := (raH - float64(int(raH))) * 60
	decD := int(result.CenterDec)
	decMin := (result.CenterDec - float64(decD)) * 60
	if decMin < 0 {
		decMin = -decMin
	}

	solveText := fmt.Sprintf("RA %dh%02.0fm  Dec %+d°%02.0f'  FOV %.1f°",
		int(raH), raMin, decD, decMin, result.FOVDeg)
	w.addLog(fmt.Sprintf("SOLVED: %s  (%d matches)", solveText, result.Matches))

	// Store solve result for pixel projection.
	w.mu.Lock()
	w.solveResult = &result
	w.solveLabels = nil
	w.solveLabelFade = 1.0
	w.mu.Unlock()

	// Search catalog for notable objects near the solved position.
	var overlayNames []string
	var labels []solveLabel

	if w.store != nil {
		halfFOV := result.FOVDeg / 2.0
		raMin := result.CenterRA - halfFOV/math.Cos(result.CenterDec*math.Pi/180)
		raMax := result.CenterRA + halfFOV/math.Cos(result.CenterDec*math.Pi/180)
		decMin := result.CenterDec - halfFOV
		decMax := result.CenterDec + halfFOV

		fieldObjs, _ := w.store.SearchByCoords(raMin, raMax, decMin, decMax, 200)

		// Separate DSOs and stars, then build labels by priority.
		var dsos []solveLabel
		var namedStars []solveLabel
		var catalogStars []solveLabel

		for _, obj := range fieldObjs {
			// Check if it's a DSO (non-star type)
			isDSO := obj.Type != "" && obj.Type != "Star" && obj.Type != "star" &&
				obj.Type != "Double Star" && obj.Type != "Variable Star"

			displayName := obj.Name
			if displayName == "" || displayName == obj.CatalogID {
				displayName = obj.CatalogID
			}
			if displayName == "" {
				continue
			}

			lbl := solveLabel{Name: displayName, RA: obj.RADeg, Dec: obj.DecDeg, Type: obj.Type}

			if isDSO {
				dsos = append(dsos, lbl)
				w.addLog(fmt.Sprintf("  In field: %s (%s, mag %.1f)", displayName, obj.Type, obj.Magnitude))
				overlayNames = append(overlayNames, displayName)
			} else if obj.Name != "" && obj.Name != obj.CatalogID && obj.Magnitude <= 6.0 {
				namedStars = append(namedStars, lbl)
				if obj.Magnitude <= 3.0 {
					overlayNames = append(overlayNames, displayName)
				}
			} else if obj.Magnitude <= 7.0 && obj.CatalogID != "" {
				catalogStars = append(catalogStars, lbl)
			}
		}

		// Build labels: DSOs first (all), then named stars (up to 15), then catalog (fill to 30)
		labels = append(labels, dsos...)
		maxNamed := 15
		if len(namedStars) < maxNamed {
			maxNamed = len(namedStars)
		}
		labels = append(labels, namedStars[:maxNamed]...)

		remaining := 30 - len(labels)
		if remaining > 0 && len(catalogStars) > 0 {
			if len(catalogStars) > remaining {
				catalogStars = catalogStars[:remaining]
			}
			labels = append(labels, catalogStars...)
		}

		w.addLog(fmt.Sprintf("  Annotations: %d DSOs, %d named stars, %d catalog objects",
			len(dsos), maxNamed, len(labels)-len(dsos)-maxNamed))
	}

	w.mu.Lock()
	w.solveLabels = labels
	w.mu.Unlock()

	// Start fading the annotations over 15 seconds.
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for i := 0; i < 150; i++ {
			<-tick.C
			w.mu.Lock()
			w.solveLabelFade = float64(150-i) / 150.0
			w.mu.Unlock()
		}
		w.mu.Lock()
		w.solveLabels = nil
		w.solveLabelFade = 0
		w.mu.Unlock()
	}()

	if len(overlayNames) > 0 {
		w.showSolveResult(strings.Join(overlayNames, "  ·  "))
	} else {
		w.showSolveResult(solveText)
	}
}

// ramMonitor periodically checks memory usage and logs warnings.
func (w *Window) ramMonitor() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	var warned80, warned90 bool
	totalRAM := totalSystemRAM()

	for range ticker.C {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		allocMB := float64(m.Sys) / (1024 * 1024)

		if totalRAM > 0 {
			pct := float64(m.Sys) / float64(totalRAM) * 100
			if pct > 90 && !warned90 {
				warned90 = true
				w.addLog(fmt.Sprintf("\u26a0 HIGH MEMORY: %.0f MB (%.0f%% of system RAM) — consider reducing stack depth", allocMB, pct))
			} else if pct > 80 && !warned80 {
				warned80 = true
				w.addLog(fmt.Sprintf("\u26a0 Memory: %.0f MB (%.0f%% of system RAM)", allocMB, pct))
			}
			// Reset warnings if memory drops
			if pct < 75 {
				warned80 = false
				warned90 = false
			}
		} else if allocMB > 4096 {
			w.addLog(fmt.Sprintf("\u26a0 Memory: %.0f MB — consider reducing stack depth", allocMB))
		}
	}
}

func (w *Window) sourceLabel() string {
	if w.config.Mode == "test" {
		return fmt.Sprintf("test dir=%s", w.config.TestDir)
	}
	return fmt.Sprintf("camera %s %dx%d@%dfps",
		w.config.Device, w.config.Width, w.config.Height, w.config.FPS)
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
