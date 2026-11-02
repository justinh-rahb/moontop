package main

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/justinh-rahb/moontop/internal/config"
)

func TestThemesComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, th := range themes {
		if seen[th.name] || th.name == "" || th.about == "" {
			t.Errorf("theme %q: duplicate or unnamed", th.name)
		}
		seen[th.name] = true
		for i, c := range []lipgloss.TerminalColor{th.borderBlurred, th.borderFocused, th.title, th.muted, th.warm, th.cool, th.err, th.ok, th.onDanger, th.rule, th.selectedBg} {
			if c == nil || colorHex(c) == "" {
				t.Errorf("theme %s: colour %d unset", th.name, i)
			}
		}
	}
	for _, want := range []string{"moontop", "phosphor", "amber", "terminal", "mono", "solarized", "gruvbox", "nord", "dracula", "catppuccin", "tokyo-night", "light"} {
		if !seen[want] {
			t.Errorf("missing theme %s", want)
		}
	}
}

func TestApplyTheme(t *testing.T) {
	t.Cleanup(func() { applyTheme("") })
	applyTheme("phosphor")
	if currentTheme != "phosphor" || colorBorderFocused != themeByName("phosphor").borderFocused {
		t.Fatal("phosphor not applied")
	}
	if panelStyle.GetBorderTopForeground() != colorBorderBlurred {
		t.Fatal("styles not rebuilt")
	}
	applyTheme("no-such-theme")
	if currentTheme != "moontop" {
		t.Fatalf("unknown theme → %q, want the default", currentTheme)
	}
}

func TestSetupDisplaySwitchesAndSavesTheme(t *testing.T) {
	t.Cleanup(func() { applyTheme("") })
	m := setupModel(t, fakePrinter(t))
	m = openCategory(t, m, "Display")
	if v := m.View(); !strings.Contains(v, "phosphor") || !strings.Contains(v, "all green") || !strings.Contains(v, "[x] moontop") {
		t.Fatalf("display category:\n%s", v)
	}
	m = press(t, m, "down", "space") // phosphor
	if currentTheme != "phosphor" || m.cfg.Display.Theme != "phosphor" {
		t.Fatalf("theme = %q, cfg %q", currentTheme, m.cfg.Display.Theme)
	}
	data, _ := os.ReadFile(m.cfgPath)
	if !strings.Contains(string(data), `theme = "phosphor"`) {
		t.Fatalf("config file:\n%s", data)
	}
	c, err := config.Load(m.cfgPath)
	if err != nil || c.Display.Theme != "phosphor" {
		t.Fatalf("reload = %+v, %v", c, err)
	}
	if !strings.Contains(m.View(), "[x] phosphor") {
		t.Fatal("check mark didn't move")
	}
	// Back to the default clears the setting.
	m = press(t, m, "up", "enter")
	if currentTheme != "moontop" || m.cfg.Display.Theme != "" {
		t.Fatalf("default: %q / %q", currentTheme, m.cfg.Display.Theme)
	}
}

func TestTintView(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev); applyTheme("") })

	applyTheme("moontop") // no text colour: untouched
	if got := tintView("a\x1b[0mb"); got != "a\x1b[0mb" {
		t.Fatalf("default theme changed the view: %q", got)
	}
	applyTheme("phosphor")
	on := "\x1b[38;2;127;217;154m"
	got := tintView("plain " + errStyle.Render("err") + " more\nnext")
	if !strings.HasPrefix(got, on+"plain ") {
		t.Fatalf("first line not tinted: %q", got)
	}
	if !strings.Contains(got, "\x1b[0m"+on+" more") {
		t.Fatalf("text after a styled segment not re-tinted: %q", got)
	}
	if !strings.Contains(got, "\n"+on+"next") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("lines / end: %q", got)
	}
}

// Setup remembers the cursor in each category, and the first visit to
// Display lands on the scheme in use, so changing it again is a short
// step rather than a walk down the whole list.
func TestSetupRemembersCursor(t *testing.T) {
	t.Cleanup(func() { applyTheme("") })
	applyTheme("amber")
	m := setupModel(t, fakePrinter(t))
	m = openCategory(t, m, "Display")
	if got := themes[m.setup.item].name; got != "amber" {
		t.Fatalf("first visit cursor on %q, want amber", got)
	}
	m = press(t, m, "down", "down")
	want := m.setup.item

	// Out to the categories, over to another one and back.
	m = press(t, m, "esc", "up", "down", "tab")
	if m.setup.item != want {
		t.Fatalf("after switching categories cursor = %d, want %d", m.setup.item, want)
	}
	// Close setup and open it again.
	m = press(t, m, "esc", "esc", "f2")
	if !m.setup.open {
		t.Fatal("setup didn't reopen")
	}
	m = press(t, m, "enter")
	if m.setup.item != want {
		t.Fatalf("after reopening cursor = %d, want %d", m.setup.item, want)
	}
	// Another category starts at its top the first time.
	m = press(t, m, "esc", "up", "tab")
	if m.setup.item != 0 {
		t.Fatalf("unvisited category cursor = %d", m.setup.item)
	}
}
