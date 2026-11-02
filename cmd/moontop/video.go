package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/justinh-rahb/moontop/internal/webcam"
)

// camReadyMsg is Moonraker's camera list, reduced to the ones the pane
// can show, in configured order.
type camReadyMsg struct {
	cams []webcam.Cam
	gen  int
	err  error
}

// camFrameMsg is one picture, either a snapshot or a decoded H.264
// frame. live frames come from ffmpeg; png is set when the terminal
// can draw the frame sharp.
type camFrameMsg struct {
	img  image.Image
	png  []byte
	gen  int
	err  error
	live bool
}

// camPollMsg asks for the next snapshot after cameraInterval.
type camPollMsg struct{ gen int }

// camStreamEndMsg is the ffmpeg reader finishing. A cancel from
// toggling the pane closed also produces one, with the old gen.
type camStreamEndMsg struct{ gen int }

// toggleCamera swaps pane 5 between the gcode console and the camera.
// The console log stays put. The last frame stays too, so opening the
// camera again has a picture before the next one arrives.
func (m *model) toggleCamera() tea.Cmd {
	m.showVideo = !m.showVideo
	m.camGen++
	m.stopStream()
	m.setFocus(focusConsole)
	if !m.showVideo {
		m.camNote = ""
		return nil
	}
	return m.openCamera()
}

func (m *model) openCamera() tea.Cmd {
	m.camSharp = webcam.SharpGraphics()
	m.pngEncode().Store(m.placingSharp())
	m.camErr = ""
	if webcam.GraphicsEnabled() && !m.camSharp {
		m.camNote = "tmux passthrough off"
	} else {
		m.camNote = ""
	}
	if m.camStream == "" && m.camURL == "" {
		return m.lookupCamera()
	}
	var cmds []tea.Cmd
	if m.camStream != "" {
		cmds = append(cmds, m.startStream())
	}
	if m.camURL != "" {
		cmds = append(cmds, m.fetchCamera())
	}
	return tea.Batch(cmds...)
}

func (m *model) lookupCamera() tea.Cmd {
	c := m.client
	gen := m.camGen
	return func() tea.Msg {
		list, err := c.Webcams()
		if err != nil {
			return camReadyMsg{gen: gen, err: err}
		}
		cams := make([]webcam.Cam, len(list))
		for i, w := range list {
			cams[i] = webcam.Cam{
				Name: w.Name, UID: w.UID, Service: w.Service,
				StreamURL: w.StreamURL, SnapshotURL: w.SnapshotURL,
				Enabled: w.Enabled, TargetFPS: int(w.TargetFPS),
				FlipH: w.FlipHorizontal, FlipV: w.FlipVertical, Rotation: int(w.Rotation),
			}
		}
		usable := webcam.Usable(cams)
		if len(usable) == 0 {
			return camReadyMsg{gen: gen, err: fmt.Errorf("no enabled webcam the terminal can show (add one under [webcam] in moonraker.conf)")}
		}
		return camReadyMsg{gen: gen, cams: usable}
	}
}

// useCamera points the pane at camera i of camList: its stream and
// snapshot, resolved against the printer's address.
func (m *model) useCamera(i int) {
	if len(m.camList) == 0 {
		return
	}
	i = (i + len(m.camList)) % len(m.camList)
	m.camIndex = i
	cam := m.camList[i]
	m.camCfg = cam
	m.camName = cam.Name
	src := webcam.SourcesFor(cam)
	m.camURL = webcam.Resolve(m.client.Host(), src.Snapshot)
	m.camStream = ""
	if src.Stream != "" {
		m.camStream = webcam.Resolve(m.client.Host(), src.Stream)
	}
}

// nextCamera moves to the printer's next enabled camera.
func (m *model) nextCamera() tea.Cmd {
	if len(m.camList) < 2 {
		return nil
	}
	m.camGen++
	m.stopStream()
	m.camImg, m.camPNG, m.camPaint = nil, nil, ""
	m.useCamera(m.camIndex + 1)
	return m.openCamera()
}

// cameraInterval is the snapshot poll period from the camera's
// target_fps (at most 10 a second: each one is an HTTP request).
func (m *model) cameraInterval() time.Duration {
	return time.Second / time.Duration(m.camCfg.FPS(10))
}

func (m *model) fetchCamera() tea.Cmd {
	url := m.camURL
	gen := m.camGen
	cam := m.camCfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		img, err := webcam.Fetch(ctx, url)
		return camFrameMsg{img: webcam.Orient(img, cam), gen: gen, err: err}
	}
}

// startStream decodes camStream in the background. Frames that the UI
// has not consumed yet are dropped, so a slow redraw cannot queue
// seconds of video behind the live picture.
func (m *model) startStream() tea.Cmd {
	m.stopStream()
	if m.camStream == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan camFrameMsg, 1)
	m.camStop = cancel
	m.camFrames = ch
	m.camWantStream = true
	// A new ffmpeg must not share camGen with the one it replaces.
	// The old reader closes its channel, and that end message would
	// otherwise look like the new stream died.
	m.camGen++
	gen := m.camGen
	rawURL := m.camStream
	width := m.streamWidth()
	encode := m.pngEncode()
	cam := m.camCfg
	go func() {
		defer close(ch)
		err := webcam.Play(ctx, rawURL, width, cam.FPS(12), func(img image.Image) error {
			if len(ch) == cap(ch) {
				return nil
			}
			img = webcam.Orient(img, cam)
			msg := camFrameMsg{img: img, gen: gen, live: true}
			if encode.Load() {
				var buf bytes.Buffer
				enc := png.Encoder{CompressionLevel: png.BestSpeed}
				if encErr := enc.Encode(&buf, img); encErr != nil {
					return encErr
				}
				msg.png = buf.Bytes()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case ch <- msg:
				return nil
			default:
				return nil
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			select {
			case ch <- camFrameMsg{gen: gen, live: true, err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return waitCamFrame(ch, gen)
}

func waitCamFrame(ch <-chan camFrameMsg, gen int) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return camStreamEndMsg{gen: gen}
		}
		return msg
	}
}

func (m *model) stopStream() {
	m.camWantStream = false
	m.camLive = false
	if m.camStop != nil {
		m.camStop()
		m.camStop = nil
	}
	m.camFrames = nil
}

func (m *model) onCamReady(msg camReadyMsg) tea.Cmd {
	if !m.showVideo || msg.gen != m.camGen {
		return nil
	}
	if msg.err != nil {
		m.camErr = msg.err.Error()
		return nil
	}
	// Stay on the camera chosen before (by uid, else name) if it's
	// still there; otherwise the printer's first.
	i := 0
	for k, c := range msg.cams {
		if m.camCfg.Name != "" && (c.UID != "" && c.UID == m.camCfg.UID || c.UID == "" && c.Name == m.camCfg.Name) {
			i = k
		}
	}
	m.camList = msg.cams
	m.useCamera(i)
	return m.openCamera()
}

func (m *model) onCamFrame(msg camFrameMsg) tea.Cmd {
	if !m.showVideo || msg.gen != m.camGen {
		return nil
	}
	if msg.live {
		if msg.err != nil {
			m.camWantStream = false
			m.camLive = false
			m.noteStream(msg.err)
			if m.camURL != "" {
				return m.fetchCamera()
			}
			m.camErr = msg.err.Error()
			return nil
		}
		m.camLive = true
		m.camErr = ""
		if m.camSharp {
			m.camNote = ""
		}
		m.camImg = msg.img
		m.camPNG = msg.png
		m.paintCamera()
		return waitCamFrame(m.camFrames, m.camGen)
	}
	if m.camLive {
		return nil
	}
	if errors.Is(msg.err, webcam.ErrNotImage) {
		// A page, not a picture (an iframe camera's player): polling
		// again won't change that.
		m.camErr = m.camName + "'s snapshot URL is a web page, not a picture; a terminal can't show it"
		if len(m.camList) > 1 {
			m.camErr += " (n: next camera)"
		}
		return nil
	}
	if msg.err != nil {
		if m.camImg == nil {
			m.camErr = msg.err.Error()
		}
	} else {
		m.camImg = msg.img
		m.camPNG = nil
		m.camErr = ""
		m.paintCamera()
	}
	if m.camWantStream {
		return nil
	}
	gen := m.camGen
	return tea.Tick(m.cameraInterval(), func(time.Time) tea.Msg {
		return camPollMsg{gen: gen}
	})
}

func (m *model) onCamPoll(msg camPollMsg) tea.Cmd {
	if !m.showVideo || msg.gen != m.camGen || m.camURL == "" || m.camWantStream || m.camLive {
		return nil
	}
	return m.fetchCamera()
}

func (m *model) onCamEnd(msg camStreamEndMsg) tea.Cmd {
	if !m.showVideo || msg.gen != m.camGen || !m.camWantStream {
		return nil
	}
	m.camWantStream = false
	m.camLive = false
	m.noteStream(fmt.Errorf("snapshot"))
	if m.camURL != "" {
		return m.fetchCamera()
	}
	return nil
}

// noteStream is the short title hint when the H.264 decode is not what
// is on screen. A missing ffmpeg and a dead stream both fall back to
// the snapshot; the passthrough hint stays, because that is why a
// Ghostty inside tmux is still drawing blocks.
func (m *model) noteStream(err error) {
	if webcam.GraphicsEnabled() && !m.camSharp {
		m.camNote = "tmux passthrough off"
		return
	}
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "ffmpeg not found") {
		m.camNote = "no ffmpeg"
		return
	}
	m.camNote = "snapshot"
}

// Camera picture styles. Auto is the 960px sharp picture where the
// terminal can draw one, and quadrant blocks everywhere else. On a
// sharp terminal, b cycles 960, 640, 480, quadrant blocks, and half
// blocks. 480 and the block rungs share the smaller decode.
const (
	camDetailAuto = iota
	camDetail640
	camDetail480
	camDetailQuad
	camDetailHalf
)

func (m *model) cycleCameraDetail() tea.Cmd {
	prev := m.streamWidth()
	m.camDetail = m.nextDetail()
	m.pngEncode().Store(m.placingSharp())
	m.paintCamera()
	if m.showVideo && m.camStream != "" && m.streamWidth() != prev {
		return m.startStream()
	}
	return nil
}

func (m model) nextDetail() int {
	if !m.camSharp {
		switch m.camDetail {
		case camDetailAuto:
			return camDetailQuad
		case camDetailQuad:
			return camDetailHalf
		default:
			return camDetailAuto
		}
	}
	switch m.camDetail {
	case camDetailAuto:
		return camDetail640
	case camDetail640:
		return camDetail480
	case camDetail480:
		return camDetailQuad
	case camDetailQuad:
		return camDetailHalf
	default:
		return camDetailAuto
	}
}

// streamWidth is the JPEG width ffmpeg should produce. The block rungs
// stay on the 480 pipe so stepping down from 480 does not start another
// decode.
func (m model) streamWidth() int {
	if !m.camSharp || m.camDetail == camDetailAuto {
		return webcam.FrameWide
	}
	if m.camDetail == camDetail640 {
		return webcam.FrameNarrow
	}
	return webcam.FrameSmall
}

// pngEncode is shared with the ffmpeg goroutine. The model is copied
// by value on every update, so the flag itself has to live on the heap.
func (m *model) pngEncode() *atomic.Bool {
	if m.camEncode == nil {
		m.camEncode = &atomic.Bool{}
	}
	return m.camEncode
}

func (m model) detailLabel() string {
	switch m.camDetail {
	case camDetail640:
		return "640"
	case camDetail480:
		return "480"
	case camDetailQuad:
		return "quad"
	case camDetailHalf:
		return "half"
	default:
		return ""
	}
}

// placingSharp is the Kitty picture. Forced block modes skip it, which
// also stops the PNG encode on the next frame. Each sharp width uses it.
func (m model) placingSharp() bool {
	switch m.camDetail {
	case camDetailAuto, camDetail640, camDetail480:
		return m.camSharp
	default:
		return false
	}
}

func (m model) drawBlocks(img image.Image, cols, rows int) string {
	if m.camDetail == camDetailHalf {
		return webcam.Draw(img, cols, rows)
	}
	return webcam.DrawQuad(img, cols, rows)
}

// paintCamera renders the current frame once. View calls are much more
// frequent than frames, and a Kitty frame is a base64 PNG.
func (m *model) paintCamera() {
	if !m.showVideo || m.camImg == nil {
		return
	}
	cols, rows := m.cameraCells()
	m.camPaintCols, m.camPaintRows = cols, rows
	if m.placingSharp() {
		pngBytes := m.camPNG
		if len(pngBytes) == 0 {
			var buf bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestSpeed}
			if err := enc.Encode(&buf, m.camImg); err == nil {
				pngBytes = buf.Bytes()
				m.camPNG = pngBytes
			}
		}
		// The frame goes out once, from the pane title's line (see
		// cameraTitlePrefix), in the slot not on screen. Half blocks
		// stay underneath: terminals that honour the placement draw the
		// photo over them; the rest show the blocks.
		m.camSlot = 1 - m.camSlot
		if seq := webcam.PlaceKittyFrame(pngBytes, cols, rows, m.camSlot, 1); seq != "" {
			m.camKitty = seq
			m.camPaint = webcam.Draw(m.camImg, cols, rows)
			m.camPlaced = true
			return
		}
	}
	m.camKitty = ""
	m.camPaint = m.drawBlocks(m.camImg, cols, rows)
}

// cameraTitlePrefix is the current frame's placement, written ahead of
// the camera pane's title. The title line only changes when a new frame
// arrives, so the frame is sent once; the body lines, which share their
// screen rows with the panes beside them and are rewritten whenever
// those change, carry only the half blocks.
func (m model) cameraTitlePrefix() string {
	if m.camKitty == "" || !m.cameraOnScreen() {
		return ""
	}
	if cols, rows := m.cameraCells(); cols != m.camPaintCols || rows != m.camPaintRows {
		return ""
	}
	return m.camKitty
}

func (m model) cameraCells() (cols, rows int) {
	cols = m.layout.w(focusConsole) - panelInnerXReserve()
	rows = m.layout.consoleViewportH + 1
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// renderCamera draws the latest frame into the console pane's box.
func (m model) renderCamera() string {
	cols, rows := m.cameraCells()
	if m.camImg != nil {
		if m.camPaint != "" && m.camPaintCols == cols && m.camPaintRows == rows {
			return m.camPaint
		}
		if m.placingSharp() {
			return webcam.Draw(m.camImg, cols, rows)
		}
		return m.drawBlocks(m.camImg, cols, rows)
	}
	switch {
	case m.camErr != "":
		return clipStatus("camera: "+m.camErr, cols)
	case m.camURL == "" && m.camStream == "":
		return "looking up the webcam…"
	default:
		return "connecting…"
	}
}

// cameraOnScreen is true when this view will include the placement.
// Any other screen has to delete it, or the photo stays painted over
// the console, the help overlay, and everything else.
func (m model) cameraOnScreen() bool {
	if !m.placingSharp() || !m.showVideo || m.camImg == nil || len(m.camPNG) == 0 {
		return false
	}
	if m.err != nil || !m.ready || m.setup.open || m.showHelp || m.macroForm != nil {
		return false
	}
	if m.browser.open || m.exclude.open || m.jobs.open || m.machine.open || m.updates.open {
		return false
	}
	if m.manualProbeActive() && m.confirmation == nil {
		return false
	}
	if m.mesh.open || m.logs.open || m.cfgFiles.open {
		return false
	}
	if m.prompt.Shown != nil && m.confirmation == nil {
		return false
	}
	if m.layout.mode == layoutMinimal || m.layout.hidden[focusConsole] {
		return false
	}
	return true
}

// cameraDeletePrefix removes a placement that this view will not redraw.
func (m model) cameraDeletePrefix() string {
	if m.camPlaced && !m.cameraOnScreen() {
		return webcam.DeleteKitty()
	}
	return ""
}

func clipStatus(s string, cols int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if cols < 1 || len(s) <= cols {
		return s
	}
	return s[:cols]
}
