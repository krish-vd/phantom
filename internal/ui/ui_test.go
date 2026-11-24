package ui

import (
	"strings"
	"testing"
)

func styled(t *testing.T, on bool) {
	old := On
	On = on
	t.Cleanup(func() { On = old })
}

func TestPlainWhenOff(t *testing.T) {
	styled(t, false)
	for _, s := range []string{Bold("x"), Red("x"), Gradient("x"), Status("claimed", "x")} {
		if s != "x" {
			t.Errorf("styling leaked into plain output: %q", s)
		}
	}
	if got := Badge("claimed"); got != "CLAIMED  " {
		t.Errorf("plain badge: %q", got)
	}
	if Banner("1", "t", "c") != "" {
		t.Error("banner printed with styling off")
	}
}

func TestBoxLinesAreEvenWhenStyled(t *testing.T) {
	styled(t, true)
	lines := []string{Red("short"), Gray(strings.Repeat("long ", 40)), Badge("young") + " pypi/x"}
	box := Box("title", Badge("claimed"), lines, 50)
	for i, l := range strings.Split(box, "\n") {
		if w := Width(l); w != 50 {
			t.Errorf("line %d is %d wide, want 50: %q", i, w, Strip(l))
		}
	}
}

func TestTruncKeepsEscapes(t *testing.T) {
	styled(t, true)
	s := Trunc(Red("abcdefghij"), 5)
	if Strip(s) != "abcd…" {
		t.Errorf("got %q", Strip(s))
	}
	if !strings.HasSuffix(s, "\x1b[0m") {
		t.Error("truncated string does not reset its colour")
	}
}

func TestStackFillsWidth(t *testing.T) {
	styled(t, true)
	s := Stack([]Segment{{1, "claimed"}, {0, "young"}, {200, "unclaimed"}, {3, "real"}}, 40)
	if w := Width(s); w != 40 {
		t.Errorf("stack is %d wide", w)
	}
}

func TestWrap(t *testing.T) {
	got := Wrap("one two three four five six seven", 10, 2)
	if len(got) != 2 || got[0] != "one two" {
		t.Errorf("got %q", got)
	}
	for _, l := range got {
		if Width(l) > 10 {
			t.Errorf("line too long: %q", l)
		}
	}
}
