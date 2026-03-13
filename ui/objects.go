package ui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/paul/photonforge/db"
	"github.com/paul/photonforge/engine"
)

// Target tracking fields — access via w.mu.
// These are added to the Window struct conceptually; we store them here
// and access them through exported methods.

// objectsTargetState holds the locked-target state, stored as fields
// that the Window struct should carry. Since we cannot modify window.go,
// we use a package-level variable keyed to the Window pointer.
// This is safe because there is only one Window instance.
var targetState = struct {
	object     *db.CelestialObject
	locked     bool
	objectName string
}{}

// showObjectsDialogNew is the unified tabbed Objects dialog.
// It replaces both showSearchDialog and the old showObjectsDialog.
// The sidebar wiring in window.go should call this method.
func (w *Window) showObjectsDialogNew() {
	inFrameTab := w.buildInFrameTab()
	allObjectsTab := w.buildAllObjectsTab()

	tabs := container.NewAppTabs(
		container.NewTabItem("In Frame", inFrameTab),
		container.NewTabItem("All Objects", allObjectsTab),
	)
	tabs.SetTabLocation(container.TabLocationTop)

	// Target status bar at the bottom.
	targetLabel := widget.NewLabel(w.targetStatusText())
	targetLabel.TextStyle = fyne.TextStyle{Monospace: true}

	content := container.NewBorder(nil, targetLabel, nil, nil, tabs)

	d := dialog.NewCustom("Objects", "Close", content, w.win)
	d.Resize(fyne.NewSize(780, 600))
	d.Show()
}

// objectTypeColor returns a color representative of the object type for the preview icon.
func objectTypeColor(typ string) color.NRGBA {
	t := strings.ToLower(typ)
	switch {
	case strings.Contains(t, "galaxy"):
		return color.NRGBA{R: 180, G: 140, B: 220, A: 255} // lavender
	case strings.Contains(t, "nebula") || strings.Contains(t, "remnant"):
		return color.NRGBA{R: 220, G: 80, B: 80, A: 255} // red/pink
	case strings.Contains(t, "globular"):
		return color.NRGBA{R: 255, G: 210, B: 100, A: 255} // gold
	case strings.Contains(t, "open cluster") || strings.Contains(t, "star cloud"):
		return color.NRGBA{R: 140, G: 200, B: 255, A: 255} // light blue
	case strings.Contains(t, "planet"):
		return color.NRGBA{R: 200, G: 160, B: 100, A: 255} // warm tan
	case strings.Contains(t, "star") || strings.Contains(t, "double") || strings.Contains(t, "variable"):
		return color.NRGBA{R: 255, G: 255, B: 220, A: 255} // warm white
	default:
		return color.NRGBA{R: 160, G: 160, B: 160, A: 255} // grey
	}
}

// objectTypeIcon generates a small colored preview circle for the object type.
func objectTypeIcon(typ string) fyne.CanvasObject {
	c := canvas.NewCircle(objectTypeColor(typ))
	c.StrokeWidth = 0
	return container.New(&fixedSizeLayout{size: fyne.NewSize(18, 18)}, c)
}

// --- Tab 1: In Frame ---

func (w *Window) buildInFrameTab() fyne.CanvasObject {
	stars := w.stacker.LatestStars()

	if len(stars) == 0 {
		return container.NewCenter(widget.NewLabel("No stars detected yet. Wait for frames to process."))
	}

	// Sort by brightness (brightest first).
	sorted := make([]engine.Star, len(stars))
	copy(sorted, stars)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Brightness > sorted[j].Brightness
	})
	if len(sorted) > 50 {
		sorted = sorted[:50]
	}

	type starEntry struct {
		label string
		star  engine.Star
	}
	entries := make([]starEntry, len(sorted))
	for i, s := range sorted {
		entries[i] = starEntry{
			label: fmt.Sprintf("#%-3d  (%4.0f, %4.0f)   br=%-6.0f  r=%.1f", i+1, s.X, s.Y, s.Brightness, s.Radius),
			star:  s,
		}
	}

	list := widget.NewList(
		func() int { return len(entries) },
		func() fyne.CanvasObject {
			lbl := widget.NewLabel("placeholder text for sizing")
			lbl.TextStyle = fyne.TextStyle{Monospace: true}
			return lbl
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(entries) {
				obj.(*widget.Label).SetText(entries[id].label)
			}
		},
	)

	// Action buttons — shown when a star is selected.
	actionBar := container.NewHBox()
	actionBar.Hide()

	list.OnSelected = func(id widget.ListItemID) {
		if id >= len(entries) {
			return
		}
		s := entries[id].star

		// Highlight the star.
		w.mu.Lock()
		w.highlightActive = true
		w.highlightX = s.X
		w.highlightY = s.Y
		w.highlightAngle = 0
		w.mu.Unlock()

		// Show action buttons.
		actionBar.Objects = nil
		actionBar.Add(widget.NewButtonWithIcon("Highlight", theme.VisibilityIcon(), func() {
			w.mu.Lock()
			w.highlightActive = true
			w.highlightX = s.X
			w.highlightY = s.Y
			w.highlightAngle = 0
			w.mu.Unlock()
			w.addLog(fmt.Sprintf("Highlighting star #%d at (%.0f,%.0f)", id+1, s.X, s.Y))
		}))
		actionBar.Add(widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), func() {
			w.mu.Lock()
			w.highlightActive = false
			w.mu.Unlock()
			w.addLog("Highlight cleared")
		}))
		actionBar.Show()
		actionBar.Refresh()

		w.addLog(fmt.Sprintf("Selected star #%d at (%.0f,%.0f) br=%.0f", id+1, s.X, s.Y, s.Brightness))
	}

	header := widget.NewLabel(fmt.Sprintf("%d detected stars (sorted by brightness):", len(entries)))
	header.TextStyle = fyne.TextStyle{Bold: true}

	return container.NewBorder(header, actionBar, nil, nil, list)
}

// --- Tab 2: All Objects ---

func (w *Window) buildAllObjectsTab() fyne.CanvasObject {
	if w.store == nil {
		return container.NewCenter(widget.NewLabel("No database connected."))
	}

	// State for current filters.
	var currentDifficulty string
	var currentCategory string
	var currentQuery string
	var results []db.CelestialObject

	// Results list — table-like rows with type icon, name, type, mag, difficulty.
	resultList := widget.NewList(
		func() int { return len(results) },
		func() fyne.CanvasObject {
			icon := container.New(&fixedSizeLayout{size: fyne.NewSize(18, 18)}, canvas.NewCircle(color.White))
			name := widget.NewLabel("Object Name Here")
			name.TextStyle = fyne.TextStyle{Bold: true}
			name.Truncation = fyne.TextTruncateEllipsis
			nameCol := container.New(&fixedWidthLayout{width: 200}, name)

			typ := widget.NewLabel("Type Here")
			typ.TextStyle = fyne.TextStyle{Monospace: true}
			typCol := container.New(&fixedWidthLayout{width: 130}, typ)

			mag := widget.NewLabel("0.0")
			mag.TextStyle = fyne.TextStyle{Monospace: true}
			mag.Alignment = fyne.TextAlignTrailing
			magCol := container.New(&fixedWidthLayout{width: 45}, mag)

			diff := widget.NewLabel("Difficulty")
			diff.TextStyle = fyne.TextStyle{Monospace: true}
			diffCol := container.New(&fixedWidthLayout{width: 110}, diff)

			desc := widget.NewLabel("Description text")
			desc.Truncation = fyne.TextTruncateEllipsis

			return container.NewHBox(
				pad(icon, 2, 6, 2, 4),
				nameCol, typCol, magCol, diffCol, desc,
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(results) {
				return
			}
			o := results[id]
			row := obj.(*fyne.Container)

			// Icon (padded container → fixedSize container → circle)
			if iconPad, ok := row.Objects[0].(*fyne.Container); ok {
				if iconBox, ok2 := iconPad.Objects[0].(*fyne.Container); ok2 {
					if circle, ok3 := iconBox.Objects[0].(*canvas.Circle); ok3 {
						circle.FillColor = objectTypeColor(o.Type)
						circle.Refresh()
					}
				}
			}

			// Name
			if nameCol, ok := row.Objects[1].(*fyne.Container); ok {
				if lbl, ok2 := nameCol.Objects[0].(*widget.Label); ok2 {
					displayName := o.Name
					if o.CatalogID != "" && o.CatalogID != o.Name {
						displayName = fmt.Sprintf("%s (%s)", o.Name, o.CatalogID)
					}
					lbl.SetText(displayName)
				}
			}

			// Type
			if typCol, ok := row.Objects[2].(*fyne.Container); ok {
				if lbl, ok2 := typCol.Objects[0].(*widget.Label); ok2 {
					lbl.SetText(o.Type)
				}
			}

			// Magnitude
			if magCol, ok := row.Objects[3].(*fyne.Container); ok {
				if lbl, ok2 := magCol.Objects[0].(*widget.Label); ok2 {
					if o.Magnitude < 90 {
						lbl.SetText(fmt.Sprintf("%.1f", o.Magnitude))
					} else {
						lbl.SetText("—")
					}
				}
			}

			// Difficulty
			if diffCol, ok := row.Objects[4].(*fyne.Container); ok {
				if lbl, ok2 := diffCol.Objects[0].(*widget.Label); ok2 {
					lbl.SetText(o.Difficulty)
				}
			}

			// Description
			if lbl, ok := row.Objects[5].(*widget.Label); ok {
				lbl.SetText(o.Description)
			}
		},
	)

	// Action bar for selected object.
	actionBar := container.NewHBox()
	actionBar.Hide()

	resultList.OnSelected = func(id widget.ListItemID) {
		if id >= len(results) {
			return
		}
		obj := results[id]

		actionBar.Objects = nil
		actionBar.Add(widget.NewButtonWithIcon("More Info", theme.InfoIcon(), func() {
			w.showObjectInfoPopup(obj)
		}))

		lockLabel := "Lock Target"
		if targetState.locked && targetState.object != nil && targetState.object.CatalogID == obj.CatalogID {
			lockLabel = "Unlock Target"
		}
		actionBar.Add(widget.NewButtonWithIcon(lockLabel, theme.MediaPlayIcon(), func() {
			w.toggleLockTarget(obj)
			// Update button text after toggle.
			resultList.UnselectAll()
			actionBar.Hide()
		}))
		actionBar.Show()
		actionBar.Refresh()
	}

	statusLabel := widget.NewLabel("")
	statusLabel.TextStyle = fyne.TextStyle{Italic: true}

	// Debounce timer for search.
	var debounceTimer *time.Timer

	doSearch := func() {
		if w.store == nil {
			return
		}
		var err error
		results, err = w.store.SearchFiltered(currentQuery, currentDifficulty, currentCategory, 100)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error: %v", err))
			return
		}
		if len(results) == 0 {
			statusLabel.SetText("No objects found")
		} else {
			statusLabel.SetText(fmt.Sprintf("%d objects", len(results)))
		}
		resultList.UnselectAll()
		actionBar.Hide()
		resultList.Refresh()
	}

	// Search entry with live filtering.
	searchEntry := widget.NewEntry()
	searchEntry.SetPlaceHolder("Search objects...")
	searchEntry.OnChanged = func(query string) {
		currentQuery = query
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(200*time.Millisecond, func() {
			fyne.Do(doSearch)
		})
	}

	// Difficulty filter buttons.
	diffAll := widget.NewButton("All", nil)
	diffNaked := widget.NewButton("Naked Eye", nil)
	diffBino := widget.NewButton("Binoculars", nil)
	diffSmall := widget.NewButton("Small Scope", nil)
	diffAdv := widget.NewButton("Advanced", nil)

	diffButtons := []*widget.Button{diffAll, diffNaked, diffBino, diffSmall, diffAdv}
	diffValues := []string{"", db.DiffNakedEye, db.DiffBinoculars, db.DiffSmallTelescope, db.DiffAdvanced}

	updateDiffButtons := func(active int) {
		for i, btn := range diffButtons {
			if i == active {
				btn.Importance = widget.HighImportance
			} else {
				btn.Importance = widget.MediumImportance
			}
			btn.Refresh()
		}
	}

	for i := range diffButtons {
		idx := i
		diffButtons[i].OnTapped = func() {
			currentDifficulty = diffValues[idx]
			updateDiffButtons(idx)
			doSearch()
		}
		diffButtons[i].Importance = widget.MediumImportance
	}
	diffAll.Importance = widget.HighImportance

	diffRow := container.NewHBox()
	diffLabel := canvas.NewText("DIFFICULTY", colorSectionLbl)
	diffLabel.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	diffLabel.TextSize = 10
	diffRow.Add(diffLabel)
	for _, btn := range diffButtons {
		diffRow.Add(btn)
	}

	// Category filter buttons.
	catAll := widget.NewButton("All", nil)
	catMessier := widget.NewButton("Messier", nil)
	catCaldwell := widget.NewButton("Caldwell", nil)
	catNGC := widget.NewButton("NGC", nil)
	catIC := widget.NewButton("IC", nil)
	catStars := widget.NewButton("Stars", nil)
	catDoubles := widget.NewButton("Doubles", nil)
	catVariable := widget.NewButton("Variable", nil)
	catSolar := widget.NewButton("Solar", nil)

	catButtons := []*widget.Button{catAll, catMessier, catCaldwell, catNGC, catIC, catStars, catDoubles, catVariable, catSolar}
	catValues := []string{"", "Messier", "Caldwell", "NGC", "IC", "Star", "Double Star", "Variable Star", "Solar System"}

	updateCatButtons := func(active int) {
		for i, btn := range catButtons {
			if i == active {
				btn.Importance = widget.HighImportance
			} else {
				btn.Importance = widget.MediumImportance
			}
			btn.Refresh()
		}
	}

	for i := range catButtons {
		idx := i
		catButtons[i].OnTapped = func() {
			currentCategory = catValues[idx]
			updateCatButtons(idx)
			doSearch()
		}
		catButtons[i].Importance = widget.MediumImportance
	}
	catAll.Importance = widget.HighImportance

	catRow := container.NewHBox()
	catLabel := canvas.NewText("CATEGORY", colorSectionLbl)
	catLabel.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	catLabel.TextSize = 10
	catRow.Add(catLabel)
	for _, btn := range catButtons {
		catRow.Add(btn)
	}

	// Initial load — show all objects.
	doSearch()

	filterPanel := container.NewVBox(
		searchEntry,
		diffRow,
		catRow,
		statusLabel,
	)

	return container.NewBorder(filterPanel, actionBar, nil, nil, resultList)
}

// --- Object Info Popup ---

func (w *Window) showObjectInfoPopup(obj db.CelestialObject) {
	// Build detailed info content.
	nameText := canvas.NewText(obj.Name, color.White)
	nameText.TextStyle = fyne.TextStyle{Bold: true}
	nameText.TextSize = 16

	var lines []string

	if obj.CatalogID != "" {
		lines = append(lines, fmt.Sprintf("Catalog ID:   %s", obj.CatalogID))
	}
	lines = append(lines, fmt.Sprintf("Type:         %s", obj.Type))
	if obj.Category != "" {
		lines = append(lines, fmt.Sprintf("Category:     %s", obj.Category))
	}
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("RA:           %.3f\u00b0", obj.RADeg))
	lines = append(lines, fmt.Sprintf("Dec:          %+.3f\u00b0", obj.DecDeg))

	if obj.Magnitude < 90 {
		lines = append(lines, fmt.Sprintf("Magnitude:    %.1f", obj.Magnitude))
	}
	if obj.SizeArcMin > 0 {
		lines = append(lines, fmt.Sprintf("Angular size: %.1f arcmin", obj.SizeArcMin))
	}

	if obj.Difficulty != "" {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Difficulty:   %s", obj.Difficulty))
		lines = append(lines, fmt.Sprintf("              %s", difficultyExplanation(obj.Difficulty)))
	}

	if obj.Description != "" {
		lines = append(lines, "")
		lines = append(lines, obj.Description)
	}

	infoLabel := widget.NewLabel(strings.Join(lines, "\n"))
	infoLabel.TextStyle = fyne.TextStyle{Monospace: true}
	infoLabel.Wrapping = fyne.TextWrapWord

	// Lock target button in the info popup.
	lockLabel := "Lock Target"
	if targetState.locked && targetState.object != nil && targetState.object.CatalogID == obj.CatalogID {
		lockLabel = "Unlock Target"
	}
	lockBtn := widget.NewButtonWithIcon(lockLabel, theme.MediaPlayIcon(), func() {
		w.toggleLockTarget(obj)
	})

	content := container.NewVBox(
		nameText,
		widget.NewSeparator(),
		infoLabel,
		widget.NewSeparator(),
		lockBtn,
	)

	scroll := container.NewVScroll(content)
	scroll.SetMinSize(fyne.NewSize(400, 350))

	d := dialog.NewCustom(obj.Name, "Close", scroll, w.win)
	d.Resize(fyne.NewSize(440, 420))
	d.Show()
}

// --- Target Lock/Unlock ---

func (w *Window) toggleLockTarget(obj db.CelestialObject) {
	if targetState.locked && targetState.object != nil && targetState.object.CatalogID == obj.CatalogID {
		w.unlockTarget()
	} else {
		w.lockTarget(obj)
	}
}

func (w *Window) lockTarget(obj db.CelestialObject) {
	targetState.object = &obj
	targetState.locked = true
	targetState.objectName = obj.Name

	w.addLog(fmt.Sprintf("Target locked: %s (%s)", obj.Name, obj.CatalogID))

	// If we can see this object in frame, highlight it.
	w.updateTargetHighlight()
}

func (w *Window) unlockTarget() {
	name := targetState.objectName
	targetState.object = nil
	targetState.locked = false
	targetState.objectName = ""

	w.mu.Lock()
	w.highlightActive = false
	w.mu.Unlock()

	w.addLog(fmt.Sprintf("Target unlocked: %s", name))
}

// updateTargetHighlight checks if the locked target is visible in the current
// frame's detected stars (by coordinate proximity to catalog RA/Dec mapped stars)
// and activates the highlight overlay if found.
func (w *Window) updateTargetHighlight() {
	if !targetState.locked || targetState.object == nil {
		return
	}

	// For now, we don't have plate solving, so we can't map RA/Dec to pixel coords.
	// Instead, log the target status. The directional indicator is a placeholder
	// until plate solving is implemented.
	obj := targetState.object
	detected := w.stacker.LatestDetected()
	if detected > 0 {
		w.addLog(fmt.Sprintf("Target: %s — %d stars detected, plate solving not yet available", obj.Name, detected))
	} else {
		w.addLog(fmt.Sprintf("Target: %s — no stars detected yet", obj.Name))
	}
}

// targetStatusText returns a summary string for the target status.
func (w *Window) targetStatusText() string {
	if !targetState.locked || targetState.object == nil {
		return "No target locked"
	}
	obj := targetState.object
	detected := w.stacker.LatestDetected()
	return fmt.Sprintf("Target: %s (%s) | %d stars detected", obj.Name, obj.CatalogID, detected)
}

// TargetLocked returns whether a target is currently locked.
func (w *Window) TargetLocked() bool {
	return targetState.locked
}

// TargetName returns the name of the currently locked target, or empty string.
func (w *Window) TargetName() string {
	return targetState.objectName
}

// SkyPositionText returns a sky position summary for display in the footer.
func (w *Window) SkyPositionText() string {
	detected := w.stacker.LatestDetected()
	matches := w.stacker.LatestMatches()

	var parts []string
	parts = append(parts, fmt.Sprintf("%d stars", detected))
	if matches > 0 {
		parts = append(parts, fmt.Sprintf("%d matched", matches))
	}

	if targetState.locked && targetState.object != nil {
		parts = append(parts, fmt.Sprintf("Target: %s", targetState.objectName))
	}

	return strings.Join(parts, " | ")
}

// difficultyExplanation returns a human-readable explanation of a difficulty level.
func difficultyExplanation(diff string) string {
	switch diff {
	case db.DiffNakedEye:
		return "Visible without any equipment"
	case db.DiffBinoculars:
		return "Easy target with binoculars or a finder scope"
	case db.DiffSmallTelescope:
		return "Needs a small telescope (60-80mm aperture)"
	case db.DiffAdvanced:
		return "Requires good equipment and/or dark skies"
	default:
		return ""
	}
}

