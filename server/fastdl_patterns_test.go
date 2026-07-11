package server

import "testing"

func TestFastDlPatternMatcher(t *testing.T) {
	m := newFastDlPatternMatcher("*.bsp, *.wav")

	cases := []struct {
		path string
		want bool
	}{
		{"de_dust2.bsp", true},
		{"maps/de_dust2.bsp", true},
		{"sound/VO/hi.wav", true},
		{"readme.txt", false},
		{"maps/foo.nav", false},
	}

	for _, tc := range cases {
		if got := m.shouldSync(tc.path); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.path, got, tc.want)
		}
	}
}

func TestFastDlPatternMatcherMapsGlob(t *testing.T) {
	m := newFastDlPatternMatcher("maps/*.nav")
	if !m.shouldSync("maps/custom.nav") {
		t.Fatal("maps/*.nav should match maps/custom.nav")
	}
	if m.shouldSync("maps/sub/x.nav") {
		t.Fatal("maps/*.nav should not match nested maps/sub/x.nav")
	}
}

func TestFastDlPatternMatcherSoundDir(t *testing.T) {
	m := newFastDlPatternMatcher("sound/")
	if !m.shouldSync("sound/vo/announcer.wav") {
		t.Fatal("sound/ subtree should match")
	}
	if m.shouldSync("materials/sound/foo.wav") {
		t.Fatal("sound/ should not match materials/sound/")
	}
}

func TestFastDlPatternMatcherDoubleStar(t *testing.T) {
	m := newFastDlPatternMatcher("**/*.mdl")
	if !m.shouldSync("models/player/custom.mdl") {
		t.Fatal("**/*.mdl should match nested mdl")
	}
}

func TestFastDlPatternMatcherSemicolonAndNewline(t *testing.T) {
	m := newFastDlPatternMatcher("*.bsp;\n*.nav")
	if !m.shouldSync("x.nav") || !m.shouldSync("y.bsp") {
		t.Fatal("should split on ; and newline")
	}
}

func TestFastDlPatternMatcherLeadingSlash(t *testing.T) {
	m := newFastDlPatternMatcher("/maps/*.bsp")
	if !m.shouldSync("maps/foo.bsp") {
		t.Fatal("leading slash on pattern should match")
	}
}

func TestFastDlPatternExcludePak(t *testing.T) {
	m := newFastDlPatternMatcher("*.pk3, !pak*.pk3")
	if !m.shouldSync("mymod.pk3") {
		t.Fatal("custom pk3 should sync")
	}
	if m.shouldSync("pak0.pk3") {
		t.Fatal("pak0.pk3 should be excluded")
	}
	if m.shouldSync("base/pak1.pk3") {
		t.Fatal("pak1.pk3 should be excluded in subdir")
	}
}

func TestFastDlPatternOnlyExcludes(t *testing.T) {
	m := newFastDlPatternMatcher("!*.log")
	if m.shouldSync("foo.log") {
		t.Fatal(".log excluded")
	}
	if !m.shouldSync("foo.bsp") {
		t.Fatal(".bsp should sync when only excludes set")
	}
}
