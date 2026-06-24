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

// isSensorObject returns true if the object name looks like a
// heater or temperature sensor.
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

// statusMsg carries a single status update from Moonraker.
type statusMsg moonraker.StatusUpdate

// initMsg carries the results of the async initialization sequence
// (list objects → subscribe → initial state).
type initMsg struct {
	sensors map[string]*sensorState
	err     error
}

// errMsg carries a fatal error.
type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

// ---------------------------------------------------------------------------
// Sensor state
// ---------------------------------------------------------------------------

type sensorState struct {
	Current float64
	Target  float64
}

// stateLabel returns "heating"/"standby" based on target temp.
func (s *sensorState) stateLabel() string {
	if s.Target > 0 {
		return "heating"
	}
	return "standby"
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type model struct {
	client *moonraker.Client

	// Per-sensor state, keyed by Klipper object name.
	sensors map[string]*sensorState

	// Sorted sensor names for stable row ordering.
	sensorNames []string

	// The bubbles table widget.
	table table.Model

	// Window dimensions for responsive sizing.
	width  int
	height int

	// True once initialization is complete.
	ready bool

	// If non-nil, we hit a fatal error.
	err error
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func initialModel(client *moonraker.Client) model {
	return model{
		client:  client,
		sensors: make(map[string]*sensorState),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.initialize(),    // discover objects + subscribe
		m.listenUpdates(), // start the listen loop
	)
}

// initialize discovers sensor objects, subscribes to them, and returns
// the initial state snapshot. Runs as a single Cmd so we can sequence
// list → subscribe without complicating the model.
func (m model) initialize() tea.Cmd {
	return func() tea.Msg {
		// 1. List all printer objects.
		objects, err := m.client.ListObjects()
		if err != nil {
			return initMsg{err: fmt.Errorf("list objects: %w", err)}
		}

		// 2. Filter to sensor/heater objects.
		subMap := make(map[string][]string)
		for _, name := range objects {
			if isSensorObject(name) {
				// Subscribe to temperature + target for all;
				// sensors without a target field just won't
				// report it — no harm.
				subMap[name] = []string{"temperature", "target"}
			}
		}

		if len(subMap) == 0 {
			return initMsg{err: fmt.Errorf("no heater/sensor objects found")}
		}

		// 3. Subscribe and get initial state.
		initial, err := m.client.Subscribe(subMap)
		if err != nil {
			return initMsg{err: fmt.Errorf("subscribe: %w", err)}
		}

		// 4. Build sensor map from initial snapshot.
		sensors := make(map[string]*sensorState, len(subMap))
		for name := range subMap {
			sensors[name] = &sensorState{}
		}
		mergeSensors(sensors, initial.Objects)

		return initMsg{sensors: sensors}
	}
}

// listenUpdates blocks on the Updates channel for ONE message.
// Re-issued after each statusMsg in Update().
func (m model) listenUpdates() tea.Cmd {
	return func() tea.Msg {
		update, ok := <-m.client.Updates()
		if !ok {
			return errMsg{fmt.Errorf("updates channel closed")}
		}
		return statusMsg(update)
	}
}

// ---------------------------------------------------------------------------
// Delta merge
// ---------------------------------------------------------------------------

// mergeSensors applies a Moonraker status diff to the sensor map.
//
// Moonraker sends partial updates — only changed fields for changed
// objects. So we:
//   - Iterate only over objects present in the diff (unchanged objects
//     are absent, NOT present with old values).
//   - For each object, update only the fields that appear in the diff.
//   - Skip objects we don't track (non-sensor objects).
//
// This means a diff like {"extruder": {"temperature": 31.2}} updates
// ONLY extruder.Current — extruder.Target retains its previous value.
func mergeSensors(sensors map[string]*sensorState, objects map[string]map[string]any) {
	for name, fields := range objects {
		s, ok := sensors[name]
		if !ok {
			continue // not a sensor we're tracking
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

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.client.Close()
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.ready {
			m.resizeTable()
		}

	case initMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.sensors = msg.sensors
		m.sensorNames = sortedKeys(msg.sensors)
		m.ready = true
		m.buildTable()
		m.resizeTable()
		m.rebuildRows()

	case statusMsg:
		if !m.ready {
			// Queue arrived before init finished — rare but possible.
			// Re-listen; the data will come again.
			return m, m.listenUpdates()
		}
		mergeSensors(m.sensors, msg.Objects)
		m.rebuildRows()
		return m, m.listenUpdates()

	case errMsg:
		m.err = msg.err
		return m, nil
	}

	return m, nil
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

	// Style the table header.
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(true)
	// Dim the cursor row styling since this is read-only —
	// we don't want a bright selection highlight.
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("236")).
		Bold(false)
	t.SetStyles(s)

	m.table = t
}

// resizeTable adjusts the table dimensions to fit the current terminal
// window, accounting for the border box and title.
func (m *model) resizeTable() {
	// Reserve space: 2 border lines + 1 title + 1 help line + margins.
	const (
		boxBorderH = 2 // top + bottom border
		titleH     = 1
		helpH      = 2
		marginH    = 2 // top margin
		padH       = 2 // vertical padding inside box
		marginW    = 4 // left margin + border sides
	)

	tableH := m.height - boxBorderH - titleH - helpH - marginH - padH
	if tableH < 3 {
		tableH = 3
	}

	tableW := m.width - marginW - 6 // border + padding
	if tableW < 40 {
		tableW = 40
	}

	m.table.SetHeight(tableH)

	// Distribute width across columns proportionally.
	nameW := tableW - 30 // give 10 each to state/current/target
	if nameW < 12 {
		nameW = 12
	}

	m.table.SetColumns([]table.Column{
		{Title: "Name", Width: nameW},
		{Title: "State", Width: 10},
		{Title: "Current", Width: 10},
		{Title: "Target", Width: 10},
	})
}

// rebuildRows regenerates the table rows from the sensor map.
// Called after every delta merge.
func (m *model) rebuildRows() {
	rows := make([]table.Row, 0, len(m.sensorNames))
	for _, name := range m.sensorNames {
		s := m.sensors[name]
		targetStr := fmt.Sprintf("%.1f°C", s.Target)
		// Sensors without a target (temperature_sensor, etc.) always
		// report 0 — show "—" instead.
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

// hasTarget returns true for object types that have a settable target temp.
func hasTarget(name string) bool {
	return strings.HasPrefix(name, "extruder") ||
		name == "heater_bed" ||
		strings.HasPrefix(name, "heater_generic")
}

// friendlyName converts a Klipper object name like "temperature_sensor chamber"
// into a more readable "Chamber (sensor)".
func friendlyName(name string) string {
	switch {
	case name == "extruder":
		return "Extruder"
	case strings.HasPrefix(name, "extruder"):
		// extruder1, extruder2, etc.
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

var (
	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(0, 1).
			MarginTop(1).
			MarginLeft(2)

	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Bold(true).
			MarginLeft(3).
			MarginTop(1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginLeft(3)
)

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v\n\n  Press q to quit.\n", m.err)
	}

	if !m.ready {
		return "\n  Connecting…\n"
	}

	title := titleStyle.Render("🌡 Temperatures")
	box := boxStyle.Render(m.table.View())
	help := helpStyle.Render("q to quit")

	return title + "\n" + box + "\n" + help + "\n"
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
