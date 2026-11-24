// Package ui draws Phantom's terminal output: colour, status badges, panels,
// bars and a live status line.
//
// Styling is on only when stdout is a terminal. Piped output, CI logs and
// tests get plain text. NO_COLOR (https://no-color.org) or TERM=dumb turns
// it off everywhere; FORCE_COLOR turns it on.
package ui

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// On reports whether output is styled.
var On = detect()

func detect() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		enableVT(os.Stdout)
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return enableVT(os.Stdout)
}

// ramp is the purple-to-cyan gradient (xterm-256 colours) used by the logo,
// the spinner and progress bars.
var ramp = []int{93, 99, 105, 111, 117, 123}

func rampAt(i, n int) int {
	if n <= 1 {
		return ramp[0]
	}
	return ramp[i*(len(ramp)-1)/(n-1)]
}

func sgr(code, s string) string {
	if !On || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func Bold(s string) string             { return sgr("1", s) }
func Fg(c int, s string) string        { return sgr("38;5;"+strconv.Itoa(c), s) }
func Red(s string) string              { return sgr("1;38;5;203", s) }
func Green(s string) string            { return Fg(114, s) }
func Yellow(s string) string           { return Fg(221, s) }
func Purple(s string) string           { return Fg(141, s) }
func Cyan(s string) string             { return Fg(117, s) }
func Gray(s string) string             { return Fg(244, s) }
func BoldColor(c int, s string) string { return sgr("1;38;5;"+strconv.Itoa(c), s) }

// Gradient colours each character of s along the ramp.
func Gradient(s string) string {
	if !On {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		b.WriteString("\x1b[1;38;5;" + strconv.Itoa(rampAt(i, len(rs))) + "m" + string(r))
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

type style struct {
	badge string // SGR for the badge
	fg    int    // foreground colour for text in this status
}

var styles = map[string]style{
	"claimed":   {"1;38;5;231;48;5;160", 203},
	"young":     {"1;38;5;16;48;5;214", 214},
	"unclaimed": {"1;38;5;231;48;5;97", 141},
	"real":      {"38;5;16;48;5;114", 114},
	"fail":      {"1;38;5;231;48;5;160", 203},
	"failed":    {"1;38;5;231;48;5;160", 203},
	"warn":      {"1;38;5;16;48;5;214", 214},
	"ok":        {"38;5;114;48;5;236", 114},
	"passed":    {"1;38;5;16;48;5;114", 114},
}

// Badge renders a coloured label like " CLAIMED ", padded so badges of the
// four statuses line up.
func Badge(s string) string { return BadgeW(s, 9) }

// Tag is a badge with no padding, for one standing on its own.
func Tag(s string) string { return BadgeW(s, len(s)) }

// BadgeW renders a badge whose label is padded to w characters.
func BadgeW(s string, w int) string {
	label := fmt.Sprintf("%-*s", w, strings.ToUpper(s))
	st, ok := styles[strings.ToLower(s)]
	if !On || !ok {
		return label
	}
	return sgr(st.badge, " "+label+" ")
}

// Status colours s in the colour of a status (claimed, young, ...).
func Status(status, s string) string {
	if st, ok := styles[status]; ok {
		return Fg(st.fg, s)
	}
	return s
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// Strip removes escape codes.
func Strip(s string) string { return ansi.ReplaceAllString(s, "") }

// Width is the number of visible characters in s.
func Width(s string) int { return utf8.RuneCountInString(Strip(s)) }

// Pad right-pads s with spaces to w visible characters.
func Pad(s string, w int) string {
	if d := w - Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// Trunc shortens s to at most n visible characters, keeping escape codes.
func Trunc(s string, n int) string {
	if Width(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	seen := 0
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "\x1b[") {
			if loc := ansi.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
				b.WriteString(s[i : i+loc[1]])
				i += loc[1]
				continue
			}
		}
		if seen == n-1 {
			b.WriteString("…")
			break
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		seen++
		i += size
	}
	if On {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// Wrap breaks plain text into lines of at most w characters, at most max lines.
func Wrap(s string, w, max int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= w:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) > max {
		lines = lines[:max]
		last := []rune(lines[max-1])
		if len(last)+2 <= w {
			lines[max-1] = string(last) + " …"
		} else {
			lines[max-1] = string(last[:min(len(last), w-1)]) + "…"
		}
	}
	for i := range lines {
		lines[i] = Trunc(lines[i], w)
	}
	return lines
}

// Box draws a rounded panel width columns wide, with a title on its top edge
// and an optional label at the right end of it. Long lines are cut.
func Box(title, right string, lines []string, width int) string {
	inner := width - 4
	edge := func(s string) string { return Fg(240, s) }
	tail := 1
	if right != "" {
		tail = Width(right) + 3
	}
	fill := width - 5 - Width(title) - tail
	if fill < 1 {
		fill = 1
	}
	var b strings.Builder
	b.WriteString(edge("╭─ ") + Bold(title) + " " + edge(strings.Repeat("─", fill)))
	if right != "" {
		b.WriteString(" " + right + " " + edge("─"))
	} else {
		b.WriteString(edge("─"))
	}
	b.WriteString(edge("╮") + "\n")
	for _, l := range lines {
		b.WriteString(edge("│") + " " + Pad(Trunc(l, inner), inner) + " " + edge("│") + "\n")
	}
	b.WriteString(edge("╰" + strings.Repeat("─", width-2) + "╯"))
	return b.String()
}

// Bar draws a gradient progress bar width cells wide.
func Bar(done, total, width int) string {
	f := 0
	if total > 0 {
		f = int(math.Round(float64(done) / float64(total) * float64(width)))
	}
	f = max(0, min(f, width))
	if !On {
		return "[" + strings.Repeat("#", f) + strings.Repeat("-", width-f) + "]"
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < f {
			b.WriteString("\x1b[38;5;" + strconv.Itoa(rampAt(i, width)) + "m█")
		} else {
			b.WriteString("\x1b[38;5;238m░")
		}
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// Meter draws a bar filled to frac (0..1) in one colour.
func Meter(frac float64, width, color int) string {
	f := int(math.Round(frac * float64(width)))
	if frac > 0 && f == 0 {
		f = 1
	}
	f = max(0, min(f, width))
	if !On {
		return strings.Repeat("#", f) + strings.Repeat("-", width-f)
	}
	return Fg(color, strings.Repeat("█", f)) + Fg(238, strings.Repeat("░", width-f))
}

// Segment is one part of a Stack bar.
type Segment struct {
	N      int
	Status string
}

// Stack draws one bar split into segments sized by N and coloured by status.
// Every non-empty segment gets at least one cell.
func Stack(segs []Segment, width int) string {
	total := 0
	for _, s := range segs {
		total += s.N
	}
	if total == 0 {
		return Fg(238, strings.Repeat("░", width))
	}
	cells := make([]int, len(segs))
	used, big := 0, 0
	for i, s := range segs {
		if s.N > 0 {
			cells[i] = max(1, s.N*width/total)
		}
		used += cells[i]
		if s.N > segs[big].N {
			big = i
		}
	}
	cells[big] += width - used
	var b strings.Builder
	for i, s := range segs {
		if cells[i] <= 0 {
			continue
		}
		ch := "█"
		if !On {
			ch = string(s.Status[0])
		}
		b.WriteString(Status(s.Status, strings.Repeat(ch, cells[i])))
	}
	return b.String()
}

var ghost = []string{
	"  ▄▀▀▀▀▀▄ ",
	" █  ● ●  █",
	" █       █",
	" █▄▀▄▀▄▀▄█",
}

// Banner returns the logo with the name, version, tagline and credit line,
// or "" when styling is off.
func Banner(version, tagline, credit string) string {
	if !On {
		return ""
	}
	text := []string{"", Gradient("p h a n t o m") + "  " + Gray("v"+version), Gray(tagline), Fg(240, credit)}
	var b strings.Builder
	b.WriteString("\n")
	for i, l := range ghost {
		c := ramp[i+1]
		parts := strings.Split(l, "●")
		for j, p := range parts {
			if j > 0 {
				b.WriteString(sgr("1;38;5;231", "●"))
			}
			b.WriteString(Fg(c, p))
		}
		b.WriteString("    " + text[i] + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// Cols is the terminal width, or 80 when unknown.
func Cols() int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 20 {
		return n
	}
	if n := termCols(); n > 20 {
		return n
	}
	return 80
}

// Ago describes how long ago t was, in days.
func Ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := int(time.Since(t).Hours() / 24)
	switch {
	case d < 0:
		return "in the future"
	case d == 0:
		return "today"
	case d == 1:
		return "yesterday"
	case d < 60:
		return fmt.Sprintf("%d days ago", d)
	case d < 730:
		return fmt.Sprintf("%d months ago", d/30)
	default:
		return fmt.Sprintf("%d years ago", d/365)
	}
}

var spin = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Live is one status line that redraws in place with a spinner while
// finished results print above it. With styling off, Set does nothing and
// Println prints normally.
type Live struct {
	mu     sync.Mutex
	text   string
	since  time.Time
	frame  int
	active bool
	stop   chan struct{}
	done   chan struct{}
}

// NewLive starts a live line. Always Stop it (defer is fine).
func NewLive(text string) *Live {
	l := &Live{text: text, since: time.Now()}
	if !On {
		return l
	}
	l.active, l.stop, l.done = true, make(chan struct{}), make(chan struct{})
	fmt.Print("\x1b[?25l") // hide the cursor while redrawing
	l.mu.Lock()
	l.draw()
	l.mu.Unlock()
	go l.loop()
	return l
}

func (l *Live) loop() {
	defer close(l.done)
	t := time.NewTicker(90 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.mu.Lock()
			l.frame++
			l.draw()
			l.mu.Unlock()
		}
	}
}

// draw must be called with mu held.
func (l *Live) draw() {
	sp := BoldColor(ramp[(l.frame/2)%len(ramp)], spin[l.frame%len(spin)])
	el := ""
	if d := time.Since(l.since); d >= time.Second {
		el = Gray(fmt.Sprintf("  %ds", int(d.Seconds())))
	}
	fmt.Print("\r\x1b[2K" + Trunc(sp+" "+l.text+el, Cols()-1))
}

// Set replaces the live text. The elapsed timer restarts when the text changes.
func (l *Live) Set(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if text != l.text {
		l.text, l.since = text, time.Now()
	}
	if l.active {
		l.draw()
	}
}

// Println prints a finished line above the live line.
func (l *Live) Println(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		fmt.Println(s)
		return
	}
	fmt.Print("\r\x1b[2K" + s + "\n")
	l.draw()
}

// Stop clears the live line and shows the cursor again. Safe to call twice.
func (l *Live) Stop() {
	l.mu.Lock()
	active := l.active
	l.active = false
	l.mu.Unlock()
	if !active {
		return
	}
	close(l.stop)
	<-l.done
	fmt.Print("\r\x1b[2K\x1b[?25h")
}
