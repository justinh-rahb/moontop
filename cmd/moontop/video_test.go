package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/justinh-rahb/moontop/internal/moonraker/moonrakertest"
	"github.com/justinh-rahb/moontop/internal/webcam"
)

func TestCameraToggleSwapsConsolePane(t *testing.T) {
	srv := fakePrinter(t)
	m := readyModel(t, srv)
	m.setFocus(focusConsole)
	m.input.SetValue("G28")

	m, cmd := updateCmd(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	if cmd == nil || !m.showVideo || m.focus != focusConsole {
		t.Fatalf("toggle on: show=%v focus=%v cmd=%v", m.showVideo, m.focus, cmd != nil)
	}
	view := m.View()
	if !strings.Contains(view, "Camera") || !strings.Contains(view, "ctrl+t console") || !strings.Contains(view, "looking up the webcam") {
		t.Fatalf("camera view:\n%s", view)
	}
	if strings.Contains(view, "G28") {
		t.Fatal("console draft still drawn while the camera is up")
	}

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if m.input.Value() != "G28" {
		t.Fatalf("camera ate a key into the console input: %q", m.input.Value())
	}

	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	if m.showVideo || !strings.Contains(m.View(), "5 Console") || !strings.Contains(m.View(), "G28") {
		t.Fatalf("toggle off: show=%v view has console draft=%v", m.showVideo, strings.Contains(m.View(), "G28"))
	}
}

func TestRenderPanelKeepsCameraGraphics(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	const cols, rows = 12, 3
	body := webcam.PlaceKitty(buf.Bytes(), cols, rows) + webcam.Draw(img, cols, rows)
	if body == "" || !strings.Contains(body, "\x1b_Ga=T") {
		t.Fatal("placement missing")
	}
	outerW := cols + panelInnerXReserve()
	outerH := rows + 3 // title, body, two borders
	panel := renderPanel("5 case", body, true, outerW, outerH)
	if w := lipgloss.Width(panel); w != outerW {
		t.Fatalf("panel width %d, want %d", w, outerW)
	}
	if lipgloss.Height(panel) != outerH {
		t.Fatalf("panel height %d, want %d", lipgloss.Height(panel), outerH)
	}
	if strings.Count(panel, "▀") != cols*rows {
		t.Fatalf("half-blocks = %d, want %d", strings.Count(panel, "▀"), cols*rows)
	}
	if !strings.Contains(panel, "\x1b_Ga=T") {
		t.Fatal("graphics sequence was stripped")
	}
}

func TestCameraDetailCycle(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	red := color.RGBA{R: 255, A: 255}
	black := color.RGBA{A: 255}
	img.Set(0, 0, red)
	img.Set(1, 0, black)
	img.Set(0, 1, black)
	img.Set(1, 1, black)
	m := model{showVideo: true, camSharp: true, camImg: img}
	m.pngEncode().Store(true)
	m.paintCamera()
	if !strings.Contains(m.camPaint, "\x1b_Ga=T") {
		t.Fatal("auto should be the sharp picture")
	}
	if m.streamWidth() != 960 || m.detailLabel() != "" {
		t.Fatalf("auto width %d label %q", m.streamWidth(), m.detailLabel())
	}
	m.cycleCameraDetail()
	if m.camDetail != camDetail640 || m.streamWidth() != 640 || m.detailLabel() != "640" || !m.camEncode.Load() || !strings.Contains(m.camPaint, "\x1b_Ga=T") {
		t.Fatalf("640 detail=%d encode=%v label=%q", m.camDetail, m.camEncode.Load(), m.detailLabel())
	}
	m.cycleCameraDetail()
	if m.camDetail != camDetail480 || m.streamWidth() != 480 || m.detailLabel() != "480" || !m.camEncode.Load() || !strings.Contains(m.camPaint, "\x1b_Ga=T") {
		t.Fatalf("480 detail=%d encode=%v label=%q", m.camDetail, m.camEncode.Load(), m.detailLabel())
	}
	m.cycleCameraDetail()
	if m.streamWidth() != 480 || m.camEncode.Load() || strings.Contains(m.camPaint, "\x1b_Ga=T") || !strings.Contains(m.camPaint, "▘") {
		t.Fatalf("quad encode=%v paint=%q", m.camEncode.Load(), m.camPaint)
	}
	m.cycleCameraDetail()
	if m.streamWidth() != 480 || strings.Contains(m.camPaint, "▘") || !strings.Contains(m.camPaint, "▀") {
		t.Fatalf("half paint=%q", m.camPaint)
	}
	m.cycleCameraDetail()
	if m.camDetail != camDetailAuto || m.streamWidth() != 960 || !m.camEncode.Load() || !strings.Contains(m.camPaint, "\x1b_Ga=T") {
		t.Fatal("cycle did not return to sharp")
	}

	plain := model{showVideo: true, camImg: img}
	plain.cycleCameraDetail()
	if plain.camDetail != camDetailQuad || plain.streamWidth() != 960 {
		t.Fatalf("block terminal landed on %d width %d", plain.camDetail, plain.streamWidth())
	}
}

// The pane follows the printer's [webcam] sections: disabled ones are
// skipped, the first enabled is shown, n moves to the next, and each
// camera's rotation, flips and target_fps come along.
func TestCameraFollowsMoonrakerConfig(t *testing.T) {
	srv := fakePrinter(t)
	srv.Handle("server.webcams.list", moonrakertest.Result(map[string]any{"webcams": []any{
		map[string]any{"name": "off", "uid": "u0", "enabled": false, "service": "mjpegstreamer", "stream_url": "/a/?action=stream", "snapshot_url": "/a/?action=snapshot"},
		map[string]any{"name": "nozzle", "uid": "u1", "enabled": true, "service": "mjpegstreamer-adaptive", "stream_url": "/webcam/?action=stream",
			"snapshot_url": "/webcam/?action=snapshot", "target_fps": 4, "flip_horizontal": true, "rotation": 90},
		map[string]any{"name": "case", "uid": "u2", "enabled": true, "service": "webrtc-camerastreamer", "stream_url": "/webcam2/webrtc", "snapshot_url": "/webcam2/snapshot.jpg"},
		map[string]any{"name": "player", "uid": "u3", "enabled": true, "service": "iframe", "stream_url": "/webcam/player"},
	}}))
	m := readyModel(t, srv)
	m.setFocus(focusConsole)
	m, cmd := updateCmd(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	m = update(t, m, collect(cmd)...)
	if len(m.camList) != 2 || m.camName != "nozzle" {
		t.Fatalf("cameras %+v, showing %q", m.camList, m.camName)
	}
	if m.camStream != "" || !strings.HasSuffix(m.camURL, "/webcam/?action=snapshot") {
		t.Fatalf("adaptive mjpeg should poll the snapshot: stream %q url %q", m.camStream, m.camURL)
	}
	if !m.camCfg.FlipH || m.camCfg.Rotation != 90 || m.cameraInterval() != 250*time.Millisecond {
		t.Fatalf("config not carried: %+v interval %v", m.camCfg, m.cameraInterval())
	}
	if v := m.View(); !strings.Contains(v, "5 nozzle") || !strings.Contains(v, "1/2 n next") {
		t.Fatalf("title:\n%s", v)
	}
	m, _ = updateCmd(t, m, keyRunes("n"))
	if m.camName != "case" || !strings.HasSuffix(m.camStream, "/webcam2/stream.h264") {
		t.Fatalf("n: %q stream %q", m.camName, m.camStream)
	}
	m, _ = updateCmd(t, m, keyRunes("n"))
	if m.camName != "nozzle" {
		t.Fatalf("n wraps: %q", m.camName)
	}
}
