package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Layout
//
// One pass on tea.WindowSizeMsg → one layout struct → render functions
// read from it. Resize math lives here, not scattered across views.
//
// All dimensions are *outer* box sizes (border + padding + content)
// unless the field name says otherwise.
// ---------------------------------------------------------------------------

type layoutMode int

const (
	// layoutWide: temp + jog side by side on top, console full-width below.
	// This is the normal Phase 5 layout.
	layoutWide layoutMode = iota

	// layoutStacked: terminal is too narrow for two columns; stack all
	// three panes vertically (temp → jog → console).
	layoutStacked

	// layoutMinimal: terminal can't even fit the stacked layout; show
	// a "too small" message instead of attempting to render.
	layoutMinimal
)

// jogContentMin is the number of content lines renderJog needs.
// 3 (pos) + 1 + 3 (pad) + 1 + 1 (step) + 1 + 3 (hint) = 13.
const jogContentMin = 13

// Minimum terminal sizes. Below the wide threshold we stack; below the
// minimum threshold we give up and print a message.
const (
	minWideW = 90
	minTermW = 50
	minTermH = 16
)

type layout struct {
	mode layoutMode

	// Wide and stacked modes both populate these.
	tableBoxW   int
	jogBoxW     int
	consoleBoxW int

	// Wide mode: shared outer height for the top row.
	topRowH int

	// Stacked mode: per-pane outer heights.
	tableH int
	jogH   int

	// Console pane: viewport (inner content) height.
	consoleViewportH int
	inputW           int

	// Inner table content width — bubbles/table column widths must sum
	// to this to avoid wrapping its header row.
	tableContentW int
}

// computeLayout produces a layout for the current terminal size.
//
// sensorCount is the number of rows the temp table wants to show — used
// to hug the top-row height to actual content when there's room, and
// to compute the inner table height.
func computeLayout(termW, termH, sensorCount int) layout {
	if termW < minTermW || termH < minTermH {
		return layout{mode: layoutMinimal}
	}

	// Reserve outer-frame chrome: app title (1) + sep (1) + body block +
	// sep (1) + footer (1) + trailing newline (1) = 4 chrome lines plus
	// 1 left margin + 3 right gutter columns.
	const (
		chromeV = 5 // title + sep + sep + footer + trailing-line safety
		marginL = 1
		gutterR = 3
	)

	availW := termW - marginL - gutterR
	availH := termH - chromeV
	if availW < 1 {
		availW = 1
	}
	if availH < 1 {
		availH = 1
	}

	if termW < minWideW {
		return computeStacked(availW, availH, sensorCount)
	}
	return computeWide(availW, availH, sensorCount)
}

// computeWide: Phase 5 layout — temp + jog side by side, console below.
func computeWide(availW, availH, sensorCount int) layout {
	jogBoxW := 26
	if jogBoxW > availW/3 {
		jogBoxW = availW / 3
	}
	tableBoxW := availW - jogBoxW
	if tableBoxW < 44 {
		tableBoxW = 44
	}

	tableContentW := tableBoxW - panelInnerXReserve()
	if tableContentW < 30 {
		tableContentW = 30
	}

	// Outer top-row height = max(table needs, jog needs).
	// Inner = title(1) + table-header(1) + sensor rows.
	tableInner := 2 + sensorCount
	jogInner := 1 + jogContentMin // title + jog body
	innerH := tableInner
	if jogInner > innerH {
		innerH = jogInner
	}
	topRowH := innerH + 2 // +2 borders, no padding rows
	if topRowH > availH-5 {
		topRowH = availH - 5 // keep at least ~5 lines for console
		if topRowH < 8 {
			topRowH = 8
		}
	}

	// Console gets all remaining height; its inner block is
	// title(1) + viewport + input(1).
	consoleOuter := availH - topRowH
	consoleViewportH := consoleOuter - 2 - 1 - 1 // borders + title + input
	if consoleViewportH < 3 {
		consoleViewportH = 3
	}

	return layout{
		mode:             layoutWide,
		tableBoxW:        tableBoxW,
		jogBoxW:          jogBoxW,
		consoleBoxW:      availW,
		topRowH:          topRowH,
		consoleViewportH: consoleViewportH,
		inputW:           availW - panelInnerXReserve() - 2, // - prompt
		tableContentW:    tableContentW,
	}
}

// computeStacked: narrow terminal — temp, jog, console all full-width
// and stacked vertically. Jog still works but renders with its content
// centered in the wider box.
func computeStacked(availW, availH, sensorCount int) layout {
	tableInner := 2 + sensorCount
	jogInner := 1 + jogContentMin

	tableH := tableInner + 2
	jogH := jogInner + 2

	consoleOuter := availH - tableH - jogH
	if consoleOuter < 7 {
		// Squeeze: trim sensor rows down to free space for console.
		consoleOuter = 7
		tableH = availH - jogH - consoleOuter
		if tableH < 5 {
			tableH = 5
		}
	}

	consoleViewportH := consoleOuter - 2 - 1 - 1
	if consoleViewportH < 3 {
		consoleViewportH = 3
	}

	contentW := availW - panelInnerXReserve()
	if contentW < 30 {
		contentW = 30
	}

	return layout{
		mode:             layoutStacked,
		tableBoxW:        availW,
		jogBoxW:          availW,
		consoleBoxW:      availW,
		tableH:           tableH,
		jogH:             jogH,
		consoleViewportH: consoleViewportH,
		inputW:           availW - panelInnerXReserve() - 2,
		tableContentW:    contentW,
	}
}

// panelInnerXReserve returns the columns consumed by a panel's border
// + padding on both sides. Encapsulated here so the panel chrome
// constant lives next to renderPanel.
func panelInnerXReserve() int {
	return 4 // 1 border + 1 padding per side
}

// ---------------------------------------------------------------------------
// renderPanel
//
// All three panes funnel through this — same border, same title bar,
// same focus-color swap. Body text is rendered as-is inside.
// ---------------------------------------------------------------------------

func renderPanel(title, body string, focused bool, outerW, outerH int) string {
	style := panelStyle.Width(outerW)
	if outerH > 0 {
		style = style.Height(outerH)
	}
	titleStyle := panelTitleStyle
	if focused {
		style = style.BorderForeground(colorBorderFocused)
		titleStyle = panelTitleFocusedStyle
	}

	bar := titleStyle.Render(title)
	content := lipgloss.JoinVertical(lipgloss.Left, bar, body)
	return style.Render(content)
}

// ---------------------------------------------------------------------------
// Footer
//
// Context-sensitive: shows the active pane's keybindings, plus the
// always-on global ones.
// ---------------------------------------------------------------------------

func renderFooter(f focusArea) string {
	var paneName, paneKeys string
	switch f {
	case focusTable:
		paneName = "Temperatures"
		paneKeys = "↑/↓: select"
	case focusConsole:
		paneName = "Console"
		paneKeys = "type gcode + enter to send"
	case focusJog:
		paneName = "Toolhead"
		paneKeys = "arrows: jog XY  •  PgUp/PgDn: jog Z  •  [/]: step  •  H: home"
	}
	left := footerFocusStyle.Render(paneName)
	mid := footerStyle.Render("  " + paneKeys)
	right := footerStyle.Render("  •  tab: next pane  •  ctrl+c: quit")
	// One MarginLeft on the whole line, no nested style hacks.
	return lipgloss.NewStyle().MarginLeft(1).Render(left + mid + right)
}

// ---------------------------------------------------------------------------
// Minimum-size message
// ---------------------------------------------------------------------------

func renderMinimal(termW, termH int) string {
	return fmt.Sprintf(
		"\n  Terminal too small (%dx%d).\n  Need at least %dx%d.\n",
		termW, termH, minTermW, minTermH,
	)
}
