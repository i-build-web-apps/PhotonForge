package automation

// ScreenshotSaveRequest for POST /screenshot/save
type ScreenshotSaveRequest struct {
	Path string `json:"path"`
}

type ScreenshotSaveResponse struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Size   int64  `json:"size_bytes"`
}

// WidgetNode represents a single widget in the tree
type WidgetNode struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Label    string       `json:"label,omitempty"`
	Value    any          `json:"value,omitempty"`
	Enabled  bool         `json:"enabled"`
	Visible  bool         `json:"visible"`
	Position [2]float32   `json:"position"`
	Size     [2]float32   `json:"size"`
	Children []WidgetNode `json:"children,omitempty"`
}

type WidgetFindRequest struct {
	Label string `json:"label,omitempty"`
	Type  string `json:"type,omitempty"`
}

type WidgetFindResponse struct {
	Widgets []WidgetNode `json:"widgets"`
}

type WidgetSetRequest struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

// ClickRequest for POST /click (coordinates)
type ClickRequest struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
}

// ClickByLabelRequest for POST /click/label
type ClickByLabelRequest struct {
	Label string `json:"label"`
	Type  string `json:"type,omitempty"`
}

// TypeRequest for POST /type
type TypeRequest struct {
	Text string `json:"text"`
}

// KeyRequest for POST /key
type KeyRequest struct {
	Name string `json:"name"`
}

// ScrollRequest for POST /scroll
type ScrollRequest struct {
	X  float32 `json:"x"`
	Y  float32 `json:"y"`
	DX float32 `json:"dx"`
	DY float32 `json:"dy"`
}

// DialogInfo for GET /dialogs
type DialogInfo struct {
	Index   int          `json:"index"`
	Title   string       `json:"title,omitempty"`
	Buttons []string     `json:"buttons,omitempty"`
	Widgets []WidgetNode `json:"widgets,omitempty"`
}

type DialogsResponse struct {
	Dialogs []DialogInfo `json:"dialogs"`
}

type DialogDismissRequest struct {
	Index  int    `json:"index"`
	Button string `json:"button,omitempty"`
}

// StateResponse for GET /state
type StateResponse struct {
	State map[string]any `json:"state"`
}

// ErrorResponse for error replies
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse for GET /health
type HealthResponse struct {
	Status string `json:"status"`
	Window bool   `json:"window_ready"`
}
