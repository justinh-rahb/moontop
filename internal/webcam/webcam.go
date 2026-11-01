// Package webcam turns a Moonraker webcam entry into a picture for the
// dashboard. The live picture is the camera's H.264 stream; a JPEG
// snapshot is the fallback. Block cells draw it anywhere. A Kitty
// graphics placement draws the same frame sharp where the terminal allows.
package webcam

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Cam is one webcam from server.webcams.list: what the dashboard needs
// to choose it, play it and show it the way the printer's config says
// (Moonraker's [webcam] sections, the same ones Mainsail and Fluidd read).
type Cam struct {
	Name        string
	UID         string
	Service     string
	StreamURL   string
	SnapshotURL string
	Enabled     bool
	TargetFPS   int
	FlipH       bool
	FlipV       bool
	Rotation    int // 0, 90, 180 or 270, clockwise
}

// Sources is what the pane can play for a camera: a live stream for
// ffmpeg and/or a snapshot to poll. Both are as configured (maybe
// relative); Resolve makes them absolute.
type Sources struct {
	Stream   string
	Snapshot string
}

// SourcesFor follows the camera's Moonraker service:
//
//	mjpegstreamer, uv4l-mjpeg      the MJPEG stream, live
//	mjpegstreamer-adaptive         the snapshot, polled at target_fps
//	webrtc-camerastreamer          the stream.h264 beside the player page
//	hlsstream, ipstream            the stream URL, live (ffmpeg reads it)
//	webrtc-go2rtc, -mediamtx,
//	-janus, jmuxer-stream, iframe  the snapshot only (no terminal decoder)
//
// An unknown service gets a stream.h264 sibling if it has one, else the
// snapshot.
func SourcesFor(c Cam) Sources {
	snap := strings.TrimSpace(c.SnapshotURL)
	stream := strings.TrimSpace(c.StreamURL)
	switch strings.ToLower(c.Service) {
	case "mjpegstreamer", "uv4l-mjpeg":
		return Sources{Stream: stream, Snapshot: snap}
	case "mjpegstreamer-adaptive":
		return Sources{Snapshot: snap}
	case "webrtc-camerastreamer":
		return Sources{Stream: H264Path(stream, snap), Snapshot: snap}
	case "hlsstream", "ipstream":
		return Sources{Stream: stream, Snapshot: snap}
	case "webrtc-go2rtc", "webrtc-mediamtx", "webrtc-janus", "jmuxer-stream", "iframe":
		return Sources{Snapshot: snap}
	}
	return Sources{Stream: H264Path(stream, snap), Snapshot: snap}
}

// Usable is the cameras the pane can show, in Moonraker's order (the
// order of the [webcam] sections, which Mainsail and Fluidd keep too):
// enabled, with a stream or a snapshot to play.
func Usable(cams []Cam) []Cam {
	var out []Cam
	for _, c := range cams {
		if !c.Enabled {
			continue
		}
		if s := SourcesFor(c); s.Stream == "" && s.Snapshot == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Pick is the first usable camera.
func Pick(cams []Cam) (Cam, bool) {
	u := Usable(cams)
	if len(u) == 0 {
		return Cam{}, false
	}
	return u[0], true
}

// FPS is the camera's target_fps for the pane: Moonraker's default (15)
// when unset, at most max.
func (c Cam) FPS(max int) int {
	f := c.TargetFPS
	if f <= 0 {
		f = 15
	}
	return min(f, max)
}

// Orient applies the camera's configured rotation and flips, as the web
// front ends do with CSS. Rotation is clockwise, after flipping.
func Orient(img image.Image, c Cam) image.Image {
	if img == nil || (!c.FlipH && !c.FlipV && c.Rotation%360 == 0) {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rot := ((c.Rotation % 360) + 360) % 360
	ow, oh := w, h
	if rot == 90 || rot == 270 {
		ow, oh = h, w
	}
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := x, y
			if c.FlipH {
				sx = w - 1 - x
			}
			if c.FlipV {
				sy = h - 1 - y
			}
			var dx, dy int
			switch rot {
			case 90:
				dx, dy = h-1-y, x
			case 180:
				dx, dy = w-1-x, h-1-y
			case 270:
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// Resolve makes a snapshot URL absolute. Relative paths on a plain
// http Moonraker at port 7125 are served by the printer's port-80
// front end (nginx), not by Moonraker itself. An absolute snapshot
// URL is kept as given. https keeps its host and port.
func Resolve(moonrakerHost, snapshot string) string {
	snapshot = strings.TrimSpace(snapshot)
	if snapshot == "" {
		return ""
	}
	if strings.Contains(snapshot, "://") {
		return snapshot
	}
	if !strings.HasPrefix(snapshot, "/") {
		snapshot = "/" + snapshot
	}
	addr := moonrakerHost
	scheme := "http"
	switch {
	case strings.HasPrefix(addr, "https://"):
		scheme = "https"
		addr = strings.TrimPrefix(addr, "https://")
	case strings.HasPrefix(addr, "http://"):
		addr = strings.TrimPrefix(addr, "http://")
	}
	host := addr
	if h, port, err := net.SplitHostPort(addr); err == nil && scheme == "http" && port == "7125" {
		host = h
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return scheme + "://" + host + snapshot
}

// Fetch downloads one JPEG snapshot.
func Fetch(ctx context.Context, url string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	img, err := jpeg.Decode(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return img, nil
}

// Draw scales img into cols by rows terminal cells. Each cell is the
// upper-half block, so the picture is cols by rows*2 pixels.
func Draw(img image.Image, cols, rows int) string {
	if img == nil || cols < 1 || rows < 1 {
		return ""
	}
	b := img.Bounds()
	rgba := image.NewRGBA(b)
	draw.Draw(rgba, b, img, b.Min, draw.Src)
	pw, ph := cols, rows*2
	var bld strings.Builder
	for y := 0; y < rows; y++ {
		if y > 0 {
			bld.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			top := average(rgba, b, x, y*2, pw, ph)
			bot := average(rgba, b, x, y*2+1, pw, ph)
			fmt.Fprintf(&bld, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", top.R, top.G, top.B, bot.R, bot.G, bot.B)
		}
		bld.WriteString("\x1b[0m")
	}
	return bld.String()
}

// average is the mean colour of the source rectangle that maps to
// pixel (x, y) of a pw by ph picture.
func average(img *image.RGBA, b image.Rectangle, x, y, pw, ph int) color.RGBA {
	sw, sh := b.Dx(), b.Dy()
	x0 := b.Min.X + x*sw/pw
	x1 := b.Min.X + (x+1)*sw/pw
	y0 := b.Min.Y + y*sh/ph
	y1 := b.Min.Y + (y+1)*sh/ph
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	var r, g, bl, n int
	for py := y0; py < y1; py++ {
		row := img.Pix[img.PixOffset(x0, py):]
		for px := x0; px < x1; px++ {
			r += int(row[0])
			g += int(row[1])
			bl += int(row[2])
			n++
			row = row[4:]
		}
	}
	if n == 0 {
		return color.RGBA{}
	}
	return color.RGBA{R: uint8(r / n), G: uint8(g / n), B: uint8(bl / n), A: 255}
}

// quadGlyph is the 2×2 block for a bit mask. Bit 0 is the top left,
// then top right, bottom left, bottom right. A set bit is foreground.
var quadGlyph = [16]rune{
	' ', '▘', '▝', '▀',
	'▖', '▌', '▞', '▛',
	'▗', '▚', '▐', '▜',
	'▄', '▙', '▟', '█',
}

// DrawQuad scales img into cols by rows cells with four pixels per
// cell. Each cell still has only two colors; the glyph picks which
// quadrants use the foreground. It walks the same pixels Draw does.
func DrawQuad(img image.Image, cols, rows int) string {
	if img == nil || cols < 1 || rows < 1 {
		return ""
	}
	b := img.Bounds()
	rgba := image.NewRGBA(b)
	draw.Draw(rgba, b, img, b.Min, draw.Src)
	pw, ph := cols*2, rows*2
	var bld strings.Builder
	var sample [4]color.RGBA
	for y := 0; y < rows; y++ {
		if y > 0 {
			bld.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			sample[0] = average(rgba, b, x*2, y*2, pw, ph)
			sample[1] = average(rgba, b, x*2+1, y*2, pw, ph)
			sample[2] = average(rgba, b, x*2, y*2+1, pw, ph)
			sample[3] = average(rgba, b, x*2+1, y*2+1, pw, ph)
			glyph, fg, bg := quadCell(sample)
			fmt.Fprintf(&bld, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm%c", fg.R, fg.G, fg.B, bg.R, bg.G, bg.B, glyph)
		}
		bld.WriteString("\x1b[0m")
	}
	return bld.String()
}

// quadCell picks the two colors and the glyph for four samples.
// A nearly flat cell is a space in the average color, so a smooth
// surface does not flicker between quadrant shapes.
func quadCell(s [4]color.RGBA) (glyph rune, fg, bg color.RGBA) {
	spread := 0
	for i := 1; i < 4; i++ {
		if d := colorDist(s[0], s[i]); d > spread {
			spread = d
		}
	}
	if spread < 18*18 {
		return ' ', s[0], s[0]
	}
	sum := 0
	var lum [4]int
	for i, p := range s {
		lum[i] = int(p.R)*3 + int(p.G)*6 + int(p.B)
		sum += lum[i]
	}
	mean := sum / 4
	var hiR, hiG, hiB, hiN, loR, loG, loB, loN int
	for i, p := range s {
		if lum[i] >= mean {
			hiR += int(p.R)
			hiG += int(p.G)
			hiB += int(p.B)
			hiN++
			continue
		}
		loR += int(p.R)
		loG += int(p.G)
		loB += int(p.B)
		loN++
	}
	if hiN == 0 || loN == 0 {
		return ' ', s[0], s[0]
	}
	fg = color.RGBA{R: uint8(hiR / hiN), G: uint8(hiG / hiN), B: uint8(hiB / hiN), A: 255}
	bg = color.RGBA{R: uint8(loR / loN), G: uint8(loG / loN), B: uint8(loB / loN), A: 255}
	mask := 0
	for i, p := range s {
		if colorDist(p, fg) <= colorDist(p, bg) {
			mask |= 1 << i
		}
	}
	return quadGlyph[mask], fg, bg
}

func colorDist(a, b color.RGBA) int {
	dr := int(a.R) - int(b.R)
	dg := int(a.G) - int(b.G)
	db := int(a.B) - int(b.B)
	return dr*dr + dg*dg + db*db
}
