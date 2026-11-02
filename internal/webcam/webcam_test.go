package webcam

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The printer's order decides, as in Mainsail and Fluidd: the first
// enabled camera is shown first, whatever its service.
func TestPickFollowsMoonrakerOrder(t *testing.T) {
	cams := []Cam{
		{Name: "SpaghettiSpy", Service: "mjpegstreamer", StreamURL: "/webcam/?action=stream", SnapshotURL: "/webcam/?action=snapshot", Enabled: true},
		{Name: "case", Service: "webrtc-camerastreamer", StreamURL: "/webcam/webrtc", SnapshotURL: "/webcam/snapshot.jpg", Enabled: true},
	}
	got, ok := Pick(cams)
	if !ok || got.Name != "SpaghettiSpy" {
		t.Fatalf("pick = %+v ok=%v", got, ok)
	}
	if u := Usable(append([]Cam{{Name: "off", Service: "mjpegstreamer", SnapshotURL: "/x", Enabled: false}}, cams...)); len(u) != 2 || u[1].Name != "case" {
		t.Fatalf("usable = %+v", u)
	}
}

func TestSourcesForServices(t *testing.T) {
	cases := []struct {
		svc, stream, snap string
		want              Sources
	}{
		{"mjpegstreamer", "/webcam/?action=stream", "/webcam/?action=snapshot", Sources{"/webcam/?action=stream", "/webcam/?action=snapshot"}},
		{"uv4l-mjpeg", "/stream", "/snap.jpg", Sources{"/stream", "/snap.jpg"}},
		{"mjpegstreamer-adaptive", "/webcam/?action=stream", "/webcam/?action=snapshot", Sources{"", "/webcam/?action=snapshot"}},
		{"webrtc-camerastreamer", "/webcam/webrtc", "/webcam/snapshot.jpg", Sources{"/webcam/stream.h264", "/webcam/snapshot.jpg"}},
		{"hlsstream", "http://cam/live.m3u8", "", Sources{"http://cam/live.m3u8", ""}},
		{"ipstream", "rtsp://cam/stream1", "http://cam/snap.jpg", Sources{"rtsp://cam/stream1", "http://cam/snap.jpg"}},
		{"webrtc-go2rtc", "http://host:1984/webrtc.html?src=cam", "http://host:1984/api/frame.jpeg?src=cam", Sources{"", "http://host:1984/api/frame.jpeg?src=cam"}},
		{"iframe", "/webcam/player", "", Sources{"", ""}},
		{"", "/webcam/webrtc", "/webcam/snapshot.jpg", Sources{"/webcam/stream.h264", "/webcam/snapshot.jpg"}},
	}
	for _, c := range cases {
		if got := SourcesFor(Cam{Service: c.svc, StreamURL: c.stream, SnapshotURL: c.snap}); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.svc, got, c.want)
		}
	}
	if u := Usable([]Cam{{Name: "player", Service: "iframe", StreamURL: "/webcam/player", Enabled: true}}); len(u) != 0 {
		t.Fatal("an iframe camera without a snapshot can't be shown")
	}
}

func TestCamFPS(t *testing.T) {
	if (Cam{}).FPS(12) != 12 || (Cam{TargetFPS: 5}).FPS(12) != 5 || (Cam{TargetFPS: 30}).FPS(10) != 10 {
		t.Fatal("fps")
	}
}

// A 2×1 picture, red then green, through each orientation.
func TestOrient(t *testing.T) {
	red, green := color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255}
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, red)
	img.Set(1, 0, green)
	at := func(i image.Image, x, y int) color.RGBA {
		r, g, b, a := i.At(x, y).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}
	if Orient(img, Cam{}) != image.Image(img) {
		t.Fatal("no orientation should return the frame untouched")
	}
	if o := Orient(img, Cam{FlipH: true}); at(o, 0, 0) != green {
		t.Fatal("flip_horizontal")
	}
	o := Orient(img, Cam{Rotation: 90})
	if b := o.Bounds(); b.Dx() != 1 || b.Dy() != 2 || at(o, 0, 0) != red || at(o, 0, 1) != green {
		t.Fatalf("rotation 90: %v", o.Bounds())
	}
	o = Orient(img, Cam{Rotation: 270})
	if at(o, 0, 0) != green || at(o, 0, 1) != red {
		t.Fatal("rotation 270")
	}
	if o := Orient(img, Cam{Rotation: 180}); at(o, 0, 0) != green {
		t.Fatal("rotation 180")
	}
	tall := image.NewRGBA(image.Rect(0, 0, 1, 2))
	tall.Set(0, 0, red)
	tall.Set(0, 1, green)
	if o := Orient(tall, Cam{FlipV: true}); at(o, 0, 0) != green {
		t.Fatal("flip_vertical")
	}
}

func TestPickSkipsDisabledAndEmpty(t *testing.T) {
	if _, ok := Pick([]Cam{{Name: "off", Enabled: false, SnapshotURL: "/a.jpg"}}); ok {
		t.Fatal("disabled camera was picked")
	}
	got, ok := Pick([]Cam{{Name: "plain", Service: "mjpegstreamer", SnapshotURL: "/a.jpg", Enabled: true}})
	if !ok || got.Name != "plain" {
		t.Fatalf("fallback = %+v ok=%v", got, ok)
	}
}

func TestResolveSnapshotURL(t *testing.T) {
	cases := []struct{ host, snap, want string }{
		{"192.168.1.217:7125", "/webcam/snapshot.jpg", "http://192.168.1.217/webcam/snapshot.jpg"},
		{"https://printer.local", "/webcam/snapshot.jpg", "https://printer.local/webcam/snapshot.jpg"},
		{"https://printer.local:7130", "webcam/snapshot.jpg", "https://printer.local:7130/webcam/snapshot.jpg"},
		{"printer.local:8080", "/snap.jpg", "http://printer.local:8080/snap.jpg"},
		{"192.168.1.217:7125", "http://10.0.0.5/snap.jpg", "http://10.0.0.5/snap.jpg"},
		{"[2001:db8::1]:7125", "/webcam/snapshot.jpg", "http://[2001:db8::1]/webcam/snapshot.jpg"},
	}
	for _, c := range cases {
		if got := Resolve(c.host, c.snap); got != c.want {
			t.Errorf("Resolve(%q, %q) = %q, want %q", c.host, c.snap, got, c.want)
		}
	}
}

func TestDrawHalfBlock(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	red := color.RGBA{R: 255, A: 255}
	green := color.RGBA{G: 255, A: 255}
	img.Set(0, 0, red)
	img.Set(1, 0, red)
	img.Set(0, 1, green)
	img.Set(1, 1, green)
	got := Draw(img, 1, 1)
	if !strings.Contains(got, "\x1b[38;2;255;0;0m") || !strings.Contains(got, "\x1b[48;2;0;255;0m") || !strings.Contains(got, "▀") {
		t.Fatalf("draw = %q", got)
	}
}

func TestDrawQuad(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	red := color.RGBA{R: 255, A: 255}
	black := color.RGBA{A: 255}
	img.Set(0, 0, red)
	img.Set(1, 0, black)
	img.Set(0, 1, black)
	img.Set(1, 1, black)
	got := DrawQuad(img, 1, 1)
	if !strings.Contains(got, "▘") || !strings.Contains(got, "\x1b[38;2;255;0;0m") {
		t.Fatalf("quad = %q", got)
	}
	if lipgloss.Width(got) != 1 {
		t.Fatalf("width %d", lipgloss.Width(got))
	}

	flat := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			flat.Set(x, y, color.RGBA{G: 40, A: 255})
		}
	}
	if strings.ContainsAny(DrawQuad(flat, 1, 1), "▘▝▀▖▌▞▛▗▚▐▜▄▙▟█") {
		t.Fatal("flat cell used a quadrant glyph")
	}
}

// HelixScreen's touchscreen mirror is an iframe "camera" whose snapshot
// URL is the same player page: nothing a terminal can show.
func TestIframeSnapshotThatIsThePage(t *testing.T) {
	c := Cam{Name: "Screen", Service: "iframe", StreamURL: "/helixscreen/screen", SnapshotURL: "/helixscreen/screen/", Enabled: true}
	if s := SourcesFor(c); s != (Sources{}) {
		t.Fatalf("sources = %+v", s)
	}
	if len(Usable([]Cam{c})) != 0 {
		t.Fatal("offered a camera with nothing to show")
	}
	c.SnapshotURL = "/helixscreen/screenshot.jpg"
	if s := SourcesFor(c); s.Snapshot != "/helixscreen/screenshot.jpg" {
		t.Fatalf("a real snapshot should be kept: %+v", s)
	}
}

func TestFetchRejectsWebPagesAcceptsPNG(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html>player</html>")
	}))
	defer page.Close()
	if _, err := Fetch(context.Background(), page.URL); !errors.Is(err, ErrNotImage) {
		t.Fatalf("html: %v", err)
	}
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, "not a picture")
	}))
	defer garbage.Close()
	if _, err := Fetch(context.Background(), garbage.URL); !errors.Is(err, ErrNotImage) {
		t.Fatalf("garbage: %v", err)
	}
	pngSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 3, 2)))
	}))
	defer pngSrv.Close()
	img, err := Fetch(context.Background(), pngSrv.URL)
	if err != nil || img.Bounds().Dx() != 3 {
		t.Fatalf("png: %v %v", img, err)
	}
}
