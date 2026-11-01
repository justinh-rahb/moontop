package console

import (
	"html"
	"regexp"
	"strings"
)

// Macros write a little HTML for Mainsail and Fluidd to render — AFC's
// <span class=success--text>LOCKED</span>, its coloured ASCII art. A
// terminal can't render HTML, but it can colour the spans. These helpers
// keep span markup in console lines (normalised so each line stands on
// its own) and turn it into plain text or coloured segments.

var (
	markupTag = regexp.MustCompile(`(?i)<(/?)(span|b|i|u|a|br|div|p|strong|em|font|small|code)(\s[^<>]*)?/?>`)
	classAttr = regexp.MustCompile(`(?i)class\s*=\s*["']?([^"'\s>]+)`)
)

// HasMarkup reports whether s contains any of the tags macros use.
func HasMarkup(s string) bool { return markupTag.MatchString(s) }

// MarkupLines splits a gcode response into lines. A span may open on one
// line and close on a later one (AFC's banner does), so each line
// re-opens the spans still open from the lines before it and closes them
// at its end. Tags other than span are dropped. Text without markup is
// split as is.
func MarkupLines(msg string) []string {
	raw := strings.Split(msg, "\n")
	if !HasMarkup(msg) {
		return raw
	}
	var open []string // classes of the spans open across lines
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		var b strings.Builder
		for _, c := range open {
			b.WriteString(`<span class=` + c + `>`)
		}
		last := 0
		for _, m := range markupTag.FindAllStringSubmatchIndex(line, -1) {
			b.WriteString(line[last:m[0]])
			last = m[1]
			closing := line[m[2]:m[3]] == "/"
			if !strings.EqualFold(line[m[4]:m[5]], "span") {
				continue
			}
			if closing {
				if len(open) > 0 {
					open = open[:len(open)-1]
					b.WriteString("</span>")
				}
				continue
			}
			class := "none"
			if m[6] >= 0 {
				if c := classAttr.FindStringSubmatch(line[m[6]:m[7]]); c != nil {
					class = c[1]
				}
			}
			open = append(open, class)
			b.WriteString(`<span class=` + class + `>`)
		}
		b.WriteString(line[last:])
		for range open {
			b.WriteString("</span>")
		}
		out = append(out, b.String())
	}
	return out
}

// Segment is a run of text and the class of the innermost span around
// it ("" outside any span).
type Segment struct {
	Text  string
	Class string
}

// Segments splits a line from MarkupLines into runs of text by span
// class, with HTML entities decoded.
func Segments(line string) []Segment {
	if !HasMarkup(line) {
		return []Segment{{Text: line}}
	}
	var segs []Segment
	var stack []string
	top := func() string {
		if len(stack) == 0 {
			return ""
		}
		return stack[len(stack)-1]
	}
	add := func(s string) {
		if s == "" {
			return
		}
		s = html.UnescapeString(s)
		if n := len(segs); n > 0 && segs[n-1].Class == top() {
			segs[n-1].Text += s
			return
		}
		segs = append(segs, Segment{Text: s, Class: top()})
	}
	last := 0
	for _, m := range markupTag.FindAllStringSubmatchIndex(line, -1) {
		add(line[last:m[0]])
		last = m[1]
		if !strings.EqualFold(line[m[4]:m[5]], "span") {
			continue
		}
		if line[m[2]:m[3]] == "/" {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		class := ""
		if m[6] >= 0 {
			if c := classAttr.FindStringSubmatch(line[m[6]:m[7]]); c != nil {
				class = c[1]
			}
		}
		if class == "none" {
			class = ""
		}
		stack = append(stack, class)
	}
	add(line[last:])
	return segs
}

// Plain is a line's text without markup.
func Plain(line string) string {
	if !HasMarkup(line) {
		return line
	}
	var b strings.Builder
	for _, s := range Segments(line) {
		b.WriteString(s.Text)
	}
	return b.String()
}
