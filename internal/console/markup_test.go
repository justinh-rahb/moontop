package console

import (
	"reflect"
	"strings"
	"testing"
)

// AFC's "READY" banner, as jimmyjon711's printer sends it: spans that
// open on one line and close on another.
const afcReady = "lane4 tool cmd: T3  <span class=success--text>LOCKED</span><span class=success--text> AND LOADED</span>\n" +
	"<span class=success--text>R  _____     ____\n" +
	"E /      \\  |  </span><span class=info--text>o</span><span class=success--text> |\n" +
	"A |       |/ ___/\n" +
	"D |_________/\n" +
	"Y <span class=warning--text>|</span><span class=error--text>_</span>\n" +
	"  Turtle_1</span>"

func TestMarkupLines(t *testing.T) {
	lines := MarkupLines(afcReady)
	if len(lines) != 7 {
		t.Fatalf("%d lines", len(lines))
	}
	want := []string{
		"lane4 tool cmd: T3  LOCKED AND LOADED",
		"R  _____     ____",
		"E /      \\  |  o |",
		"A |       |/ ___/",
		"D |_________/",
		"Y |_",
		"  Turtle_1",
	}
	for i, l := range lines {
		if got := Plain(l); got != want[i] {
			t.Errorf("line %d plain = %q, want %q", i, got, want[i])
		}
	}
	// A line inside an open span is coloured as that span.
	if segs := Segments(lines[3]); len(segs) != 1 || segs[0].Class != "success--text" {
		t.Fatalf("carried span: %+v", segs)
	}
	got := Segments(lines[2])
	wantSegs := []Segment{{"E /      \\  |  ", "success--text"}, {"o", "info--text"}, {" |", "success--text"}}
	if !reflect.DeepEqual(got, wantSegs) {
		t.Fatalf("segments = %+v", got)
	}
	if segs := Segments(lines[0]); segs[0] != (Segment{"lane4 tool cmd: T3  ", ""}) || segs[1].Class != "success--text" {
		t.Fatalf("first line: %+v", segs)
	}
	if segs := Segments(lines[5]); len(segs) != 3 || segs[0].Class != "success--text" || segs[1].Class != "warning--text" || segs[2].Class != "error--text" {
		t.Fatalf("Y line: %+v", segs)
	}
}

func TestMarkupPlainLinesUntouched(t *testing.T) {
	for _, s := range []string{"// value < 5 and > 2", "echo: 3 &lt; 4", ""} {
		if l := MarkupLines(s); len(l) != 1 || l[0] != s || Plain(s) != s {
			t.Errorf("%q changed: %q", s, l)
		}
	}
	if got := Plain(`<b>bold</b> &amp; <span class="error--text">red</span><br/>`); got != "bold & red" {
		t.Fatalf("plain = %q", got)
	}
	if strings.Contains(strings.Join(MarkupLines("<b>x</b>\ny"), ""), "<b>") {
		t.Fatal("non-span tags should be dropped")
	}
}
