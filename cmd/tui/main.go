// Command tui is the Moonraker terminal UI.
//
// Usage:
//
//	go run ./cmd/tui -host <ip:port>
//	MOONRAKER_HOST=<ip:port> go run ./cmd/tui
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/justinh-rahb/moonraker-tui/internal/moonraker"
)

// ---------------------------------------------------------------------------
// Sensor-object prefixes we care about
// ---------------------------------------------------------------------------

var sensorPrefixes = []string{
	"extruder",
	"heater_bed",
	"heater_generic",
	"temperature_sensor",
	"temperature_fan",
}

func isSensorObject(name string) bool {
	for _, prefix := range sensorPrefixes {
		if name == prefix || strings.HasPrefix(name, prefix+" ") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Message types
// ---------------------------------------------------------------------------

type statusMsg moonraker.StatusUpdate

type gcodeRespMsg string

// jogStepSizes mirror Mainsail's "Toolhead" step selector.
var jogStepSizes = []float64{0.1, 1, 10, 25, 50, 100}

const jogFeedrate = 1500 // mm/min, used for relative G1 moves

type initMsg struct {
	sensors  map[string]*sensorState
	position [3]float64
	err      error
}

type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

// gcodeSentMsg is emitted after a GcodeScript call completes. If err is
// non-nil, the send failed; otherwise no UI state change is needed (the
// echo and response stream handle the visible feedback).
type gcodeSentMsg struct {
	cmd string
	err error
}

// ---------------------------------------------------------------------------
// Sensor state
// ---------------------------------------------------------------------------

type sensorState struct {
	Current float64
	Target  float64
}

func (s *sensorState) stateLabel() string {
	if s.Target > 0 {
		return "heating"
	}
	return "standby"
}

// ---------------------------------------------------------------------------
// Focus
// ---------------------------------------------------------------------------

type focusArea int

const (
	focusTable focusArea = iota
	focusConsole
	focusJog
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type model struct {
	client *moonraker.Client

	sensors     map[string]*sensorState
	sensorNames []string

	// Toolhead position (X, Y, Z) as reported by the printer.
	position [3]float64

	// Index into jogStepSizes — easier to cycle than a free float.
	stepIndex int

	table    table.Model
	viewport viewport.Model
	input    textinput.Model

	// Console log lines; viewport content is rebuilt from this on append.
	logLines []string

	focus focusArea

	width  int
	height int

	// Computed once per resize (see resizeLayout), consumed by View.
	layout layout

	ready bool
	err   error
}

func initialModel(client *moonraker.Client) model {
	ti := textinput.New()
	ti.Placeholder = "Type a gcode command (e.g. M115, G28) and press Enter"
	ti.Prompt = "> "
	ti.CharLimit = 256

	vp := viewport.New(40, 10)

	return model{
		client:    client,
		sensors:   make(map[string]*sensorState),
		viewport:  vp,
		input:     ti,
		focus:     focusTable,
		stepIndex: 2, // 10 mm
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.initialize(),
		m.listenUpdates(),
		m.listenGcode(),
	)
}

// initialize discovers sensor objects, subscribes to them, and returns
// the initial state snapshot.
func (m model) initialize() tea.Cmd {
	return func() tea.Msg {
		objects, err := m.client.ListObjects()
		if err != nil {
			return initMsg{err: fmt.Errorf("list objects: %w", err)}
		}

		subMap := make(map[string][]string)
		for _, name := range objects {
			if isSensorObject(name) {
				subMap[name] = []string{"temperature", "target"}
			}
		}

		if len(subMap) == 0 {
			return initMsg{err: fmt.Errorf("no heater/sensor objects found")}
		}

		// Add toolhead alongside the heaters — same subscription,
		// just another object in the request map.
		subMap["toolhead"] = []string{"position"}

		initial, err := m.client.Subscribe(subMap)
		if err != nil {
			return initMsg{err: fmt.Errorf("subscribe: %w", err)}
		}

		sensors := make(map[string]*sensorState, len(subMap))
		for name := range subMap {
			if isSensorObject(name) {
				sensors[name] = &sensorState{}
			}
		}
		var position [3]float64
		mergeStatus(sensors, &position, initial.Objects)

		return initMsg{sensors: sensors, position: position}
	}
}

// listenUpdates blocks on one status update, then is re-issued by Update().
func (m model) listenUpdates() tea.Cmd {
	return func() tea.Msg {
		update, ok := <-m.client.Updates()
		if !ok {
			return errMsg{fmt.Errorf("updates channel closed")}
		}
		return statusMsg(update)
	}
}

// listenGcode blocks on one gcode response line, then is re-issued.
func (m model) listenGcode() tea.Cmd {
	return func() tea.Msg {
		line, ok := <-m.client.GcodeResponses()
		if !ok {
			return errMsg{fmt.Errorf("gcode response channel closed")}
		}
		return gcodeRespMsg(line)
	}
}

// sendGcode wraps a Client.GcodeScript call as a Cmd so the Update loop
// doesn't block on the round-trip.
func (m model) sendGcode(cmd string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		return gcodeSentMsg{cmd: cmd, err: client.GcodeScript(cmd)}
	}
}

// ---------------------------------------------------------------------------
// Delta merge
// ---------------------------------------------------------------------------

// mergeStatus applies a Moonraker status diff. One pass over the diff
// dispatches each object to a per-type handler — sensors map updates,
// toolhead position array, and (later) whatever other objects we add.
// New object types are a single new case here, not a separate merge fn.
func mergeStatus(
	sensors map[string]*sensorState,
	position *[3]float64,
	objects map[string]map[string]any,
) {
	for name, fields := range objects {
		switch {
		case name == "toolhead":
			if v, ok := fields["position"]; ok {
				if arr, ok := v.([]any); ok {
					for i := 0; i < 3 && i < len(arr); i++ {
						if f, ok := arr[i].(float64); ok {
							position[i] = f
						}
					}
				}
			}
		default:
			s, ok := sensors[name]
			if !ok {
				continue
			}
			if v, ok := fields["temperature"]; ok {
				if f, ok := v.(float64); ok {
					s.Current = f
				}
			}
			if v, ok := fields["target"]; ok {
				if f, ok := v.(float64); ok {
					s.Target = f
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		// Global keys that fire regardless of focus.
		switch msg.String() {
		case "ctrl+c":
			m.client.Close()
			return m, tea.Quit
		case "tab":
			m.toggleFocus()
			return m, nil
		case "q":
			// "q" quits only when the console isn't focused — otherwise
			// the user could never type a literal 'q' into gcode.
			if m.focus != focusConsole {
				m.client.Close()
				return m, tea.Quit
			}
		}

		// Route the key to the focused sub-model / handler.
		switch m.focus {
		case focusConsole:
			if msg.String() == "enter" {
				return m, m.submitCommand()
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		case focusTable:
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			return m, cmd
		case focusJog:
			return m, m.handleJogKey(msg)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.ready {
			m.resizeLayout()
		}

	case initMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.sensors = msg.sensors
		m.sensorNames = sortedKeys(msg.sensors)
		m.position = msg.position
		m.ready = true
		m.buildTable()
		m.resizeLayout()
		m.rebuildRows()

	case statusMsg:
		if !m.ready {
			return m, m.listenUpdates()
		}
		mergeStatus(m.sensors, &m.position, msg.Objects)
		m.rebuildRows()
		return m, m.listenUpdates()

	case gcodeRespMsg:
		m.appendLog(string(msg))
		return m, m.listenGcode()

	case gcodeSentMsg:
		if msg.err != nil {
			m.appendLog(fmt.Sprintf("!! send failed: %v", msg.err))
		}
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil
	}

	return m, nil
}

func (m *model) toggleFocus() {
	// table → console → jog → table …
	switch m.focus {
	case focusTable:
		m.focus = focusConsole
		m.input.Focus()
	case focusConsole:
		m.focus = focusJog
		m.input.Blur()
	case focusJog:
		m.focus = focusTable
	}
}

// handleJogKey reacts to a keypress while the jog pane is focused.
// Returns a Cmd that performs the (non-blocking) GcodeScript send, or
// nil for keys that just adjust local UI state (step cycling).
func (m *model) handleJogKey(msg tea.KeyMsg) tea.Cmd {
	step := jogStepSizes[m.stepIndex]
	switch msg.String() {
	case "left":
		return m.sendJog("X", -step)
	case "right":
		return m.sendJog("X", step)
	case "up":
		return m.sendJog("Y", step)
	case "down":
		return m.sendJog("Y", -step)
	case "pgup":
		return m.sendJog("Z", step)
	case "pgdown":
		return m.sendJog("Z", -step)
	case "[":
		if m.stepIndex > 0 {
			m.stepIndex--
		}
	case "]":
		if m.stepIndex < len(jogStepSizes)-1 {
			m.stepIndex++
		}
	case "H":
		return m.sendGcode("G28")
	}
	return nil
}

// sendJog emits a relative move on a single axis, wrapping it in
// SAVE/RESTORE_GCODE_STATE so we leave the printer in absolute mode
// regardless of what it was in before.
func (m *model) sendJog(axis string, delta float64) tea.Cmd {
	script := strings.Join([]string{
		"SAVE_GCODE_STATE NAME=_ui_movement",
		"G91",
		fmt.Sprintf("G1 %s%g F%d", axis, delta, jogFeedrate),
		"RESTORE_GCODE_STATE NAME=_ui_movement",
	}, "\n")
	return m.sendGcode(script)
}

func (m *model) submitCommand() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	if text == "" {
		return nil
	}
	m.appendLog(">>> " + text)
	return m.sendGcode(text)
}

func (m *model) appendLog(line string) {
	// Some gcode responses come as multi-line blobs; split so the viewport
	// wraps each line cleanly.
	for _, l := range strings.Split(line, "\n") {
		m.logLines = append(m.logLines, l)
	}
	m.viewport.SetContent(strings.Join(m.logLines, "\n"))
	m.viewport.GotoBottom()
}

// ---------------------------------------------------------------------------
// Table construction and resizing
// ---------------------------------------------------------------------------

func (m *model) buildTable() {
	columns := []table.Column{
		{Title: "Name", Width: 24},
		{Title: "State", Width: 10},
		{Title: "Current", Width: 10},
		{Title: "Target", Width: 10},
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(false),
	)

	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("236")).
		Bold(false)
	t.SetStyles(s)

	m.table = t
}

// resizeLayout recomputes pane dimensions from the current window size
// and applies them to the sub-models that need explicit sizing
// (bubbles/table columns + height, viewport, textinput). Pure layout
// math lives in computeLayout (layout.go); this is the imperative
// "tell each widget how big it is" step.
func (m *model) resizeLayout() {
	m.layout = computeLayout(m.width, m.height, len(m.sensorNames))
	if m.layout.mode == layoutMinimal {
		return
	}

	// Reserve 8 cols for bubbles/table's internal cell padding (1 char
	// each side × 4 columns) so the Target header doesn't wrap.
	const cellPadTotal = 8
	nameW := m.layout.tableContentW - cellPadTotal - 30
	if nameW < 12 {
		nameW = 12
	}
	m.table.SetColumns([]table.Column{
		{Title: "Name", Width: nameW},
		{Title: "State", Width: 10},
		{Title: "Current", Width: 10},
		{Title: "Target", Width: 10},
	})

	// Data rows inside the table widget: outer pane height - title(1)
	// - table-header(1) - borders(2).
	var paneH int
	switch m.layout.mode {
	case layoutWide:
		paneH = m.layout.topRowH
	default:
		paneH = m.layout.tableH
	}
	rowH := paneH - 4
	if rowH < 1 {
		rowH = 1
	}
	m.table.SetHeight(rowH)

	m.viewport.Width = m.layout.consoleBoxW - panelInnerXReserve()
	m.viewport.Height = m.layout.consoleViewportH
	m.viewport.SetContent(strings.Join(m.logLines, "\n"))
	m.viewport.GotoBottom()
	m.input.Width = m.layout.inputW
}

func (m *model) rebuildRows() {
	rows := make([]table.Row, 0, len(m.sensorNames))
	for _, name := range m.sensorNames {
		s := m.sensors[name]
		targetStr := fmt.Sprintf("%.1f°C", s.Target)
		if s.Target == 0 && !hasTarget(name) {
			targetStr = "—"
		}
		rows = append(rows, table.Row{
			friendlyName(name),
			s.stateLabel(),
			fmt.Sprintf("%.1f°C", s.Current),
			targetStr,
		})
	}
	m.table.SetRows(rows)
}

func hasTarget(name string) bool {
	return strings.HasPrefix(name, "extruder") ||
		name == "heater_bed" ||
		strings.HasPrefix(name, "heater_generic")
}

func friendlyName(name string) string {
	switch {
	case name == "extruder":
		return "Extruder"
	case strings.HasPrefix(name, "extruder"):
		return "Extruder " + strings.TrimPrefix(name, "extruder")
	case name == "heater_bed":
		return "Bed"
	case strings.HasPrefix(name, "heater_generic "):
		return strings.TrimPrefix(name, "heater_generic ")
	case strings.HasPrefix(name, "temperature_sensor "):
		n := strings.TrimPrefix(name, "temperature_sensor ")
		return n + " (sensor)"
	case strings.HasPrefix(name, "temperature_fan "):
		n := strings.TrimPrefix(name, "temperature_fan ")
		return n + " (fan)"
	default:
		return name
	}
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m model) View() string {
	if m.err != nil {
		return errStyle.Render(fmt.Sprintf("\n  Error: %v\n\n", m.err)) +
			footerStyle.Render("  Press ctrl+c to quit.\n")
	}
	if !m.ready {
		return "\n  Connecting…\n"
	}
	if m.layout.mode == layoutMinimal {
		return renderMinimal(m.width, m.height)
	}

	tablePane := renderPanel(
		"Temperatures",
		m.table.View(),
		m.focus == focusTable,
		m.layout.tableBoxW,
		paneHeight(m.layout, focusTable),
	)
	jogPane := renderPanel(
		"Toolhead",
		m.renderJog(),
		m.focus == focusJog,
		m.layout.jogBoxW,
		paneHeight(m.layout, focusJog),
	)
	consolePane := renderPanel(
		"Console",
		m.viewport.View()+"\n"+m.input.View(),
		m.focus == focusConsole,
		m.layout.consoleBoxW,
		0, // auto-size to content
	)

	var stacked string
	switch m.layout.mode {
	case layoutWide:
		top := lipgloss.JoinHorizontal(lipgloss.Top, tablePane, jogPane)
		stacked = lipgloss.JoinVertical(lipgloss.Left, top, consolePane)
	default: // stacked
		stacked = lipgloss.JoinVertical(lipgloss.Left, tablePane, jogPane, consolePane)
	}
	body := bodyStyle.Render(stacked)

	title := appTitleStyle.Render("🌡 Moonraker")
	footer := renderFooter(m.focus)

	return title + "\n" + body + "\n" + footer + "\n"
}

// paneHeight returns the outer height for a given pane in the current
// layout — wide mode shares one top-row height, stacked mode has
// per-pane heights.
func paneHeight(l layout, f focusArea) int {
	if l.mode == layoutWide {
		if f == focusTable || f == focusJog {
			return l.topRowH
		}
		return 0
	}
	switch f {
	case focusTable:
		return l.tableH
	case focusJog:
		return l.jogH
	}
	return 0
}

// renderJog produces the contents of the jog pane: an XY directional
// pad, Z controls, the active step size, and the current toolhead
// position.
func (m model) renderJog() string {
	contentW := m.layout.jogBoxW - panelInnerXReserve()
	if contentW < 16 {
		contentW = 16
	}

	pos := jogPosStyle.Render(fmt.Sprintf(
		"X %8.2f\nY %8.2f\nZ %8.2f",
		m.position[0], m.position[1], m.position[2],
	))

	pad := jogPadStyle.Render(lipgloss.JoinVertical(lipgloss.Center,
		"  ↑  ",
		"← + →",
		"  ↓  ",
	))
	pad = lipgloss.PlaceHorizontal(contentW, lipgloss.Center, pad)

	step := jogStepStyle.Render(fmt.Sprintf("step  %g mm", jogStepSizes[m.stepIndex]))
	step = lipgloss.PlaceHorizontal(contentW, lipgloss.Center, step)

	hint := jogHintStyle.Render(strings.Join([]string{
		"PgUp/PgDn  Z ±",
		"[ / ]      step",
		"H          home",
	}, "\n"))

	return lipgloss.JoinVertical(lipgloss.Left,
		pos,
		"",
		pad,
		"",
		step,
		"",
		hint,
	)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sortedKeys(m map[string]*sensorState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	host := flag.String("host", "", "Moonraker host:port (e.g. 192.168.1.100:7125)")
	flag.Parse()

	if *host == "" {
		*host = os.Getenv("MOONRAKER_HOST")
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "Usage: tui -host <ip:port>")
		fmt.Fprintln(os.Stderr, "   or: MOONRAKER_HOST=<ip:port> tui")
		os.Exit(1)
	}

	client, err := moonraker.New(*host)
	if err != nil {
		log.Fatalf("Failed to connect to Moonraker: %v", err)
	}

	m := initialModel(client)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}
}
