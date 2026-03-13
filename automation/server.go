package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"fyne.io/fyne/v2"
)

// Automatable is the minimal interface any Fyne app must implement
// to be controllable via the automation HTTP API.
type Automatable interface {
	FyneWindow() fyne.Window
	AppState() map[string]any
}

// Server is the HTTP automation server.
type Server struct {
	app  Automatable
	addr string
	srv  *http.Server
}

// New creates an automation server for the given app.
func New(app Automatable) *Server {
	return &Server{app: app}
}

// Start begins listening on the given address (e.g., "127.0.0.1:9876").
func (s *Server) Start(addr string) error {
	s.addr = addr
	mux := http.NewServeMux()

	// Screenshots
	mux.HandleFunc("POST /screenshot", s.handleScreenshot)
	mux.HandleFunc("POST /screenshot/save", s.handleScreenshotSave)

	// Widget tree
	mux.HandleFunc("GET /widgets", s.handleWidgetTree)
	mux.HandleFunc("GET /widgets/find", s.handleWidgetFind)
	mux.HandleFunc("POST /widgets/set", s.handleWidgetSet)

	// Input simulation
	mux.HandleFunc("POST /click", s.handleClick)
	mux.HandleFunc("POST /click/label", s.handleClickByLabel)
	mux.HandleFunc("POST /type", s.handleType)
	mux.HandleFunc("POST /key", s.handleKey)
	mux.HandleFunc("POST /scroll", s.handleScroll)

	// Dialogs
	mux.HandleFunc("GET /dialogs", s.handleDialogs)
	mux.HandleFunc("POST /dialogs/dismiss", s.handleDialogDismiss)

	// App state
	mux.HandleFunc("GET /state", s.handleState)

	// Health
	mux.HandleFunc("GET /health", s.handleHealth)

	s.srv = &http.Server{Handler: logMiddleware(mux)}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("automation server: %w", err)
	}
	fmt.Printf("Automation server listening on %s\n", addr)
	go s.srv.Serve(ln) //nolint:errcheck
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.srv.Shutdown(ctx) //nolint:errcheck
	}
}

// statusRecorder captures the HTTP status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logMiddleware logs every API request with method, path, status, and duration.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("[automation] %s %s → %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// reqContext returns a context with a 5-second timeout for handler operations.
func reqContext(r *http.Request) context.Context {
	ctx, _ := context.WithTimeout(r.Context(), 5*time.Second) //nolint:govet
	return ctx
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// --- Screenshot Handlers ---

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	ctx := reqContext(r)
	data, err := doSync(ctx, func() []byte {
		img := s.app.FyneWindow().Canvas().Capture()
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil
		}
		return buf.Bytes()
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if data == nil {
		writeError(w, 500, "failed to capture screenshot")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(data) //nolint:errcheck
}

func (s *Server) handleScreenshotSave(w http.ResponseWriter, r *http.Request) {
	var req ScreenshotSaveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, 400, "path is required")
		return
	}

	ctx := reqContext(r)
	data, err := doSync(ctx, func() []byte {
		img := s.app.FyneWindow().Canvas().Capture()
		var buf bytes.Buffer
		png.Encode(&buf, img) //nolint:errcheck
		return buf.Bytes()
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	if err := os.WriteFile(req.Path, data, 0644); err != nil {
		writeError(w, 500, "failed to save: "+err.Error())
		return
	}

	// Get image dimensions from the capture
	imgSize, _ := doSync(ctx, func() [2]int {
		img := s.app.FyneWindow().Canvas().Capture()
		b := img.Bounds()
		return [2]int{b.Dx(), b.Dy()}
	})

	writeJSON(w, 200, ScreenshotSaveResponse{
		Path:   req.Path,
		Width:  imgSize[0],
		Height: imgSize[1],
		Size:   int64(len(data)),
	})
}

// --- State Handler ---

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	ctx := reqContext(r)
	state, err := doSync(ctx, func() map[string]any {
		return s.app.AppState()
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, StateResponse{State: state})
}

// --- Health Handler ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	windowReady := false
	if win := s.app.FyneWindow(); win != nil {
		windowReady = true
	}
	writeJSON(w, 200, HealthResponse{
		Status: "ok",
		Window: windowReady,
	})
}

// --- Click Handlers ---

func (s *Server) handleClick(w http.ResponseWriter, r *http.Request) {
	var req ClickRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}

	ctx := reqContext(r)
	err := doSyncVoid(ctx, func() {
		canvas := s.app.FyneWindow().Canvas()
		pos := fyne.NewPos(req.X, req.Y)

		// Check overlays first (dialogs are on top)
		for _, overlay := range canvas.Overlays().List() {
			if obj := hitTest(overlay, pos, fyne.NewPos(0, 0)); obj != nil {
				if tappable, ok := obj.(fyne.Tappable); ok {
					tappable.Tapped(&fyne.PointEvent{
						AbsolutePosition: pos,
						Position:         pos,
					})
					return
				}
			}
		}

		// Then check main content
		if obj := hitTest(canvas.Content(), pos, fyne.NewPos(0, 0)); obj != nil {
			if tappable, ok := obj.(fyne.Tappable); ok {
				tappable.Tapped(&fyne.PointEvent{
					AbsolutePosition: pos,
					Position:         pos,
				})
			}
		}
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "clicked"})
}

func (s *Server) handleClickByLabel(w http.ResponseWriter, r *http.Request) {
	var req ClickByLabelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Label == "" {
		writeError(w, 400, "label is required")
		return
	}

	ctx := reqContext(r)
	found, err := doSync(ctx, func() bool {
		canvas := s.app.FyneWindow().Canvas()
		tree := walkTree(canvas.Content(), "", 0)
		matches := findWidgets(tree, req.Label, req.Type)
		if len(matches) == 0 {
			// Check overlays
			for i, overlay := range canvas.Overlays().List() {
				otree := walkTree(overlay, fmt.Sprintf("o%d/", i), 0)
				matches = findWidgets(otree, req.Label, req.Type)
				if len(matches) > 0 {
					obj := resolveByID(overlay, matches[0].ID)
					if tappable, ok := obj.(fyne.Tappable); ok {
						centerX := matches[0].Position[0] + matches[0].Size[0]/2
						centerY := matches[0].Position[1] + matches[0].Size[1]/2
						tappable.Tapped(&fyne.PointEvent{
							AbsolutePosition: fyne.NewPos(centerX, centerY),
							Position:         fyne.NewPos(matches[0].Size[0]/2, matches[0].Size[1]/2),
						})
						return true
					}
				}
			}
			return false
		}
		obj := resolveByID(canvas.Content(), matches[0].ID)
		if tappable, ok := obj.(fyne.Tappable); ok {
			centerX := matches[0].Position[0] + matches[0].Size[0]/2
			centerY := matches[0].Position[1] + matches[0].Size[1]/2
			tappable.Tapped(&fyne.PointEvent{
				AbsolutePosition: fyne.NewPos(centerX, centerY),
				Position:         fyne.NewPos(matches[0].Size[0]/2, matches[0].Size[1]/2),
			})
			return true
		}
		return false
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 404, fmt.Sprintf("no widget found with label %q", req.Label))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "clicked"})
}

// --- Type/Key Handlers ---

func (s *Server) handleType(w http.ResponseWriter, r *http.Request) {
	var req TypeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}

	ctx := reqContext(r)
	err := doSyncVoid(ctx, func() {
		canvas := s.app.FyneWindow().Canvas()
		focused := canvas.Focused()
		if focused == nil {
			return
		}
		for _, ch := range req.Text {
			focused.TypedRune(ch)
		}
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "typed"})
}

func (s *Server) handleKey(w http.ResponseWriter, r *http.Request) {
	var req KeyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}

	ctx := reqContext(r)
	err := doSyncVoid(ctx, func() {
		canvas := s.app.FyneWindow().Canvas()
		keyName := fyne.KeyName(req.Name)
		// Try focused widget first, then canvas-level handler
		if focused := canvas.Focused(); focused != nil {
			focused.TypedKey(&fyne.KeyEvent{Name: keyName})
		}
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "key_sent"})
}

// --- Scroll Handler ---

func (s *Server) handleScroll(w http.ResponseWriter, r *http.Request) {
	var req ScrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}

	ctx := reqContext(r)
	err := doSyncVoid(ctx, func() {
		canvas := s.app.FyneWindow().Canvas()
		pos := fyne.NewPos(req.X, req.Y)
		if obj := hitTest(canvas.Content(), pos, fyne.NewPos(0, 0)); obj != nil {
			if scrollable, ok := obj.(fyne.Scrollable); ok {
				scrollable.Scrolled(&fyne.ScrollEvent{
					PointEvent: fyne.PointEvent{
						AbsolutePosition: pos,
						Position:         pos,
					},
					Scrolled: fyne.NewDelta(req.DX, req.DY),
				})
			}
		}
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "scrolled"})
}

// --- Widget Tree Handlers ---

func (s *Server) handleWidgetTree(w http.ResponseWriter, r *http.Request) {
	ctx := reqContext(r)
	tree, err := doSync(ctx, func() WidgetNode {
		canvas := s.app.FyneWindow().Canvas()
		return walkTree(canvas.Content(), "", 0)
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, tree)
}

func (s *Server) handleWidgetFind(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("label")
	widgetType := r.URL.Query().Get("type")
	if label == "" && widgetType == "" {
		writeError(w, 400, "label or type query param required")
		return
	}

	ctx := reqContext(r)
	results, err := doSync(ctx, func() []WidgetNode {
		canvas := s.app.FyneWindow().Canvas()
		tree := walkTree(canvas.Content(), "", 0)
		found := findWidgets(tree, label, widgetType)
		// Also search overlays
		for i, overlay := range canvas.Overlays().List() {
			otree := walkTree(overlay, fmt.Sprintf("o%d/", i), 0)
			found = append(found, findWidgets(otree, label, widgetType)...)
		}
		return found
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, WidgetFindResponse{Widgets: results})
}

func (s *Server) handleWidgetSet(w http.ResponseWriter, r *http.Request) {
	var req WidgetSetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, 400, "id is required")
		return
	}

	ctx := reqContext(r)
	err := doSyncVoid(ctx, func() {
		canvas := s.app.FyneWindow().Canvas()
		obj := resolveByID(canvas.Content(), req.ID)
		if obj == nil {
			for _, overlay := range canvas.Overlays().List() {
				if o := resolveByID(overlay, req.ID); o != nil {
					obj = o
					break
				}
			}
		}
		if obj != nil {
			setWidgetValue(obj, req.Value)
		}
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "set"})
}

// --- Dialog Handlers ---

func (s *Server) handleDialogs(w http.ResponseWriter, r *http.Request) {
	ctx := reqContext(r)
	dialogs, err := doSync(ctx, func() []DialogInfo {
		canvas := s.app.FyneWindow().Canvas()
		overlays := canvas.Overlays().List()
		var result []DialogInfo
		for i, overlay := range overlays {
			tree := walkTree(overlay, fmt.Sprintf("o%d/", i), 0)
			info := DialogInfo{
				Index:   i,
				Widgets: tree.Children,
			}
			// Extract button labels
			buttons := findWidgets(tree, "", "Button")
			for _, b := range buttons {
				if b.Label != "" {
					info.Buttons = append(info.Buttons, b.Label)
				}
			}
			// Extract title from first label
			labels := findWidgets(tree, "", "Label")
			if len(labels) > 0 {
				info.Title = labels[0].Label
			}
			result = append(result, info)
		}
		return result
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, DialogsResponse{Dialogs: dialogs})
}

func (s *Server) handleDialogDismiss(w http.ResponseWriter, r *http.Request) {
	var req DialogDismissRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}

	ctx := reqContext(r)
	err := doSyncVoid(ctx, func() {
		canvas := s.app.FyneWindow().Canvas()
		overlays := canvas.Overlays().List()
		if req.Index >= len(overlays) {
			return
		}
		overlay := overlays[req.Index]

		if req.Button != "" {
			tree := walkTree(overlay, fmt.Sprintf("o%d/", req.Index), 0)
			buttons := findWidgets(tree, req.Button, "Button")
			if len(buttons) > 0 {
				obj := resolveByID(overlay, buttons[0].ID)
				if tappable, ok := obj.(fyne.Tappable); ok {
					tappable.Tapped(&fyne.PointEvent{})
					return
				}
			}
		}
		// Fallback: hide the overlay
		overlay.Hide()
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "dismissed"})
}
