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

	"github.com/charmbracelet/bubbles/list"
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
	job      printJob
	err      error
}

// filesLoadedMsg arrives once ListFiles returns. Files is sorted with
// most-recently-modified first.
type filesLoadedMsg struct {
	files []moonraker.FileInfo
	err   error
}

// printStartedMsg arrives after a StartPrint Cmd completes. The footer
// will still wait for print_stats to confirm before showing "Printing"
// — this message just surfaces send failures.
type printStartedMsg struct {
	filename string
	err      error
}

// printJob mirrors the subset of print_stats we care about for the
// footer/job-status display.
type printJob struct {
	State    string // "standby" | "printing" | "paused" | "complete" | "error" | "cancelled"
	Filename string
	Duration float64 // seconds of active print time
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
	focusFiles
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

	// Current print job state (from print_stats).
	job printJob

	table    table.Model
	viewport viewport.Model
	input    textinput.Model
	files    list.Model

	// Console log lines; viewport content is rebuilt from this on append.
	logLines []string

	// When non-nil, a y/n confirmation prompt is pending. Intercepts
	// global key routing — see Update(). This same slot serves any
	// confirm-gated action (start print, cancel print, future AFC
	// eject, ...) so we don't grow a parallel flag per action.
	confirmation *pendingConfirmation

	focus focusArea

	width  int
	height int

	// Computed once per resize (see resizeLayout), consumed by View.
	layout layout

	ready bool
	err   error
}

// fileItem adapts a moonraker.FileInfo for bubbles/list.
type fileItem struct {
	info moonraker.FileInfo
}

func (f fileItem) Title() string       { return f.info.Path }
func (f fileItem) Description() string { return formatBytes(f.info.Size) }
func (f fileItem) FilterValue() string { return f.info.Path }

// formatBytes renders a byte count in a human-friendly unit.
func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func initialModel(client *moonraker.Client) model {
	ti := textinput.New()
	ti.Placeholder = "Type a gcode command (e.g. M115, G28) and press Enter"
	ti.Prompt = "> "
	ti.CharLimit = 256

	vp := viewport.New(40, 10)

	files := list.New(nil, list.NewDefaultDelegate(), 40, 10)
	files.Title = "" // we render our own pane title
	files.SetShowTitle(false)
	files.SetShowStatusBar(false)
	files.SetShowHelp(false)
	files.SetFilteringEnabled(true)

	return model{
		client:    client,
		sensors:   make(map[string]*sensorState),
		viewport:  vp,
		input:     ti,
		files:     files,
		focus:     focusTable,
		stepIndex: 2, // 10 mm
		job:       printJob{State: "standby"},
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.initialize(),
		m.listenUpdates(),
		m.listenGcode(),
		m.loadFiles(),
	)
}

// loadFiles fetches the gcode file list once at startup.
func (m model) loadFiles() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		files, err := client.ListFiles()
		if err != nil {
			return filesLoadedMsg{err: err}
		}
		// Most recently modified first — matches Mainsail's default.
		sort.Slice(files, func(i, j int) bool {
			return files[i].ModifiedTime > files[j].ModifiedTime
		})
		return filesLoadedMsg{files: files}
	}
}

// startPrint asks Moonraker to begin printing the named file. The job
// state will still go through print_stats, not optimistic updates.
func (m model) startPrint(filename string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		return printStartedMsg{filename: filename, err: client.StartPrint(filename)}
	}
}

// pendingConfirmation represents an action waiting for y/n approval.
// One slot on the model, reused for every confirm-gated action.
type pendingConfirmation struct {
	prompt string  // human-readable, rendered above the footer
	onYes  tea.Cmd // executed if the user presses y
}

// jobControlMsg is emitted after a pause/resume/cancel call completes.
// Like printStartedMsg, it only surfaces send failures — the visible
// state change waits for the next print_stats update.
type jobControlMsg struct {
	action string
	err    error
}

func (m model) pausePrint() tea.Cmd {
	client := m.client
	return func() tea.Msg { return jobControlMsg{"pause", client.PausePrint()} }
}

func (m model) resumePrint() tea.Cmd {
	client := m.client
	return func() tea.Msg { return jobControlMsg{"resume", client.ResumePrint()} }
}

func (m model) cancelPrint() tea.Cmd {
	client := m.client
	return func() tea.Msg { return jobControlMsg{"cancel", client.CancelPrint()} }
}

// jobActive returns true while a print is either running or paused —
// i.e. while pause/resume/cancel are meaningful actions.
func jobActive(state string) bool {
	return state == "printing" || state == "paused"
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

		// Add toolhead + print_stats alongside the heaters — same
		// subscription, just more objects in the request map.
		subMap["toolhead"] = []string{"position"}
		subMap["print_stats"] = []string{"state", "filename", "print_duration"}

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
		job := printJob{State: "standby"}
		mergeStatus(sensors, &position, &job, initial.Objects)

		return initMsg{sensors: sensors, position: position, job: job}
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
	job *printJob,
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
		case name == "print_stats":
			if v, ok := fields["state"]; ok {
				if s, ok := v.(string); ok {
					job.State = s
				}
			}
			if v, ok := fields["filename"]; ok {
				if s, ok := v.(string); ok {
					job.Filename = s
				}
			}
			if v, ok := fields["print_duration"]; ok {
				if f, ok := v.(float64); ok {
					job.Duration = f
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
		// 1. Confirmation prompt intercepts first — when one is armed,
		//    only y/n/esc are meaningful; everything else is swallowed.
		if m.confirmation != nil {
			switch msg.String() {
			case "y", "Y":
				cmd := m.confirmation.onYes
				m.confirmation = nil
				return m, cmd
			case "n", "N", "esc":
				m.confirmation = nil
			}
			return m, nil
		}

		// 2. Global keys that fire regardless of focus.
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

		// 3. Global job controls. Skip while the console is capturing
		//    text input — same rationale as the 'q' guard above.
		if m.focus != focusConsole {
			if cmd, handled := m.handleJobControl(msg); handled {
				return m, cmd
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
		case focusFiles:
			return m, m.handleFilesKey(msg)
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
		m.job = msg.job
		m.ready = true
		m.buildTable()
		m.resizeLayout()
		m.rebuildRows()

	case statusMsg:
		if !m.ready {
			return m, m.listenUpdates()
		}
		mergeStatus(m.sensors, &m.position, &m.job, msg.Objects)
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

	case filesLoadedMsg:
		if msg.err != nil {
			m.appendLog(fmt.Sprintf("!! load files: %v", msg.err))
			return m, nil
		}
		items := make([]list.Item, len(msg.files))
		for i, f := range msg.files {
			items[i] = fileItem{info: f}
		}
		m.files.SetItems(items)
		return m, nil

	case printStartedMsg:
		if msg.err != nil {
			m.appendLog(fmt.Sprintf("!! start print %q: %v", msg.filename, msg.err))
		}
		return m, nil

	case jobControlMsg:
		if msg.err != nil {
			m.appendLog(fmt.Sprintf("!! %s: %v", msg.action, msg.err))
		}
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil
	}

	return m, nil
}

func (m *model) toggleFocus() {
	// table → jog → console → files → table …
	// Confirmation is now global (not files-pane-scoped), so Tab no
	// longer needs to clear it — it survives focus changes intentionally.
	switch m.focus {
	case focusTable:
		m.focus = focusJog
	case focusJog:
		m.focus = focusConsole
		m.input.Focus()
	case focusConsole:
		m.focus = focusFiles
		m.input.Blur()
	case focusFiles:
		m.focus = focusTable
	}
}

// handleFilesKey implements the files pane's key behavior. The y/n
// confirmation sub-state used to live here (Phase 7); it now lives at
// the top of Update() so any global action can reuse it. Enter just
// arms a confirmation and returns — the actual y/n handling happens
// globally.
func (m *model) handleFilesKey(msg tea.KeyMsg) tea.Cmd {
	if msg.String() == "enter" {
		if it, ok := m.files.SelectedItem().(fileItem); ok {
			name := it.info.Path
			m.confirmation = &pendingConfirmation{
				prompt: "Start print: " + name + "?",
				onYes:  m.startPrint(name),
			}
		}
		return nil
	}

	var cmd tea.Cmd
	m.files, cmd = m.files.Update(msg)
	return cmd
}

// handleJobControl dispatches p/r/c global keypresses based on the
// current job state. Returns (cmd, true) when the key was a recognized
// job-control action — even if it was a no-op for the current state
// (so the key gets swallowed rather than falling through to pane
// routing). Returns (_, false) when the key isn't a job-control key
// at all.
func (m *model) handleJobControl(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "p":
		if m.job.State == "printing" {
			return m.pausePrint(), true
		}
		return nil, true
	case "r":
		if m.job.State == "paused" {
			return m.resumePrint(), true
		}
		return nil, true
	case "c":
		if jobActive(m.job.State) {
			m.confirmation = &pendingConfirmation{
				prompt: "Cancel current print?",
				onYes:  m.cancelPrint(),
			}
		}
		return nil, true
	}
	return nil, false
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

	m.files.SetSize(m.layout.filesContentW, m.layout.filesContentH)
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
	filesPane := renderPanel(
		"Files",
		m.renderFiles(),
		m.focus == focusFiles,
		m.layout.filesBoxW,
		paneHeight(m.layout, focusFiles),
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
		top := lipgloss.JoinHorizontal(lipgloss.Top, tablePane, jogPane, filesPane)
		stacked = lipgloss.JoinVertical(lipgloss.Left, top, consolePane)
	default: // stacked
		stacked = lipgloss.JoinVertical(lipgloss.Left, tablePane, jogPane, filesPane, consolePane)
	}
	body := bodyStyle.Render(stacked)

	title := appTitleStyle.Render("🌡 Moonraker")
	footer := renderFooter(m.focus, m.job)

	out := title + "\n" + body + "\n"
	if m.confirmation != nil {
		out += renderConfirmation(m.confirmation.prompt) + "\n"
	}
	out += footer + "\n"
	return out
}

func (m model) renderFiles() string {
	return m.files.View()
}

// paneHeight returns the outer height for a given pane in the current
// layout — wide mode shares one top-row height, stacked mode has
// per-pane heights.
func paneHeight(l layout, f focusArea) int {
	if l.mode == layoutWide {
		if f == focusTable || f == focusJog || f == focusFiles {
			return l.topRowH
		}
		return 0
	}
	switch f {
	case focusTable:
		return l.tableH
	case focusJog:
		return l.jogH
	case focusFiles:
		return l.filesH
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
