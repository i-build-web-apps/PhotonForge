package automation

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// walkTree recursively traverses the Fyne widget/container tree and builds
// a WidgetNode tree. MUST be called on the Fyne main thread (inside doSync).
func walkTree(obj fyne.CanvasObject, pathPrefix string, index int) WidgetNode {
	if obj == nil {
		return WidgetNode{ID: pathPrefix + "nil", Type: "nil"}
	}

	id := fmt.Sprintf("%s%d", pathPrefix, index)

	node := WidgetNode{
		ID:       id,
		Type:     typeName(obj),
		Visible:  obj.Visible(),
		Position: [2]float32{obj.Position().X, obj.Position().Y},
		Size:     [2]float32{obj.Size().Width, obj.Size().Height},
		Enabled:  true,
	}

	// Extract label/value/enabled based on concrete widget type.
	switch w := obj.(type) {
	case *widget.Button:
		node.Label = w.Text
		node.Enabled = !w.Disabled()
	case *widget.Label:
		node.Label = w.Text
	case *widget.Entry:
		node.Label = w.PlaceHolder
		node.Value = w.Text
		node.Enabled = !w.Disabled()
	case *widget.Slider:
		node.Value = w.Value
		node.Enabled = !w.Disabled()
	case *widget.Check:
		node.Label = w.Text
		node.Value = w.Checked
		node.Enabled = !w.Disabled()
	case *widget.Select:
		node.Label = w.PlaceHolder
		node.Value = w.Selected
		node.Enabled = !w.Disabled()
	case *widget.Hyperlink:
		node.Label = w.Text
	case *widget.RichText:
		// Extract concatenated text from segments
		var sb strings.Builder
		for _, seg := range w.Segments {
			if ts, ok := seg.(*widget.TextSegment); ok {
				sb.WriteString(ts.Text)
			}
		}
		node.Label = sb.String()
	}

	// Recurse into children. Fyne uses *fyne.Container to hold child objects.
	if container, ok := obj.(*fyne.Container); ok && container != nil {
		childPrefix := id + "/"
		for i, child := range container.Objects {
			if child != nil {
				childNode := walkTree(child, childPrefix, i)
				node.Children = append(node.Children, childNode)
			}
		}
	}

	return node
}

// typeName returns the Go type name of a Fyne object (without package path).
func typeName(obj fyne.CanvasObject) string {
	if obj == nil {
		return "nil"
	}
	t := reflect.TypeOf(obj)
	if t.Kind() == reflect.Ptr {
		return t.Elem().Name()
	}
	return t.Name()
}

// findWidgets searches the widget tree for nodes matching the given criteria.
// Label matching is case-insensitive substring. Type matching is exact.
func findWidgets(root WidgetNode, label, widgetType string) []WidgetNode {
	var results []WidgetNode
	var search func(n WidgetNode)
	search = func(n WidgetNode) {
		matches := true
		if label != "" {
			labelLower := strings.ToLower(label)
			if !strings.Contains(strings.ToLower(n.Label), labelLower) {
				// Also check string values (e.g., entry text)
				if sv, ok := n.Value.(string); !ok || !strings.Contains(strings.ToLower(sv), labelLower) {
					matches = false
				}
			}
		}
		if widgetType != "" && n.Type != widgetType {
			matches = false
		}
		if matches && (label != "" || widgetType != "") {
			results = append(results, n)
		}
		for _, child := range n.Children {
			search(child)
		}
	}
	search(root)
	return results
}

// resolveByID walks the fyne object tree to find the CanvasObject at a path ID.
// Path format: "0/3/1" means root container → child 3 → child 1.
// For overlay paths like "o0/0/3/1", strips the overlay prefix first.
// MUST be called on the Fyne main thread.
func resolveByID(root fyne.CanvasObject, id string) fyne.CanvasObject {
	if root == nil || id == "" {
		return nil
	}

	parts := strings.Split(id, "/")
	if len(parts) == 0 {
		return nil
	}

	// Skip the root index and any overlay prefix (e.g., "o0")
	startIdx := 1
	if len(parts) <= startIdx {
		return root
	}

	current := root
	for i := startIdx; i < len(parts); i++ {
		idx, err := strconv.Atoi(parts[i])
		if err != nil {
			return nil
		}
		cont, ok := current.(*fyne.Container)
		if !ok || idx >= len(cont.Objects) || idx < 0 {
			return nil
		}
		current = cont.Objects[idx]
	}
	return current
}

// hitTest finds the deepest tappable CanvasObject at the given absolute position.
// Traverses containers in reverse order (later children are rendered on top).
// MUST be called on the Fyne main thread.
func hitTest(obj fyne.CanvasObject, absPos fyne.Position, parentPos fyne.Position) fyne.CanvasObject {
	if obj == nil || !obj.Visible() {
		return nil
	}

	objAbsPos := fyne.NewPos(parentPos.X+obj.Position().X, parentPos.Y+obj.Position().Y)
	size := obj.Size()

	// Check if position is within this object's bounds
	if absPos.X < objAbsPos.X || absPos.Y < objAbsPos.Y ||
		absPos.X > objAbsPos.X+size.Width || absPos.Y > objAbsPos.Y+size.Height {
		return nil
	}

	// Recurse into containers (deepest tappable match wins)
	if c, ok := obj.(*fyne.Container); ok {
		for i := len(c.Objects) - 1; i >= 0; i-- {
			if child := hitTest(c.Objects[i], absPos, objAbsPos); child != nil {
				return child
			}
		}
	}

	// Return this object if it's tappable
	if _, ok := obj.(fyne.Tappable); ok {
		return obj
	}

	return nil
}

// setWidgetValue sets the value of a widget by type.
// MUST be called on the Fyne main thread.
func setWidgetValue(obj fyne.CanvasObject, value any) {
	switch w := obj.(type) {
	case *widget.Slider:
		if v, ok := toFloat64(value); ok {
			w.SetValue(v)
		}
	case *widget.Entry:
		if v, ok := value.(string); ok {
			w.SetText(v)
		}
	case *widget.Check:
		if v, ok := value.(bool); ok {
			w.SetChecked(v)
		}
	case *widget.Select:
		if v, ok := value.(string); ok {
			w.SetSelected(v)
		}
	}
}

// toFloat64 converts a JSON number (which json.Unmarshal decodes as float64)
// or a string to float64.
func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	}
	return 0, false
}
