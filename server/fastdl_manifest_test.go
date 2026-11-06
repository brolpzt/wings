package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildFastDlManifest(t *testing.T) {
	before := fastDlManifest{
		"custom/mymap.bsp":     {10, 1},
		"cstrike/halflife.wad": {20, 1},
		"cstrike/old.bsp":      {30, 1},
	}
	after := fastDlManifest{
		"custom/mymap.bsp":     {10, 1}, // user's file, untouched by the reinstall
		"cstrike/halflife.wad": {20, 1}, // stock file, untouched (known from previous manifest)
		"cstrike/old.bsp":      {31, 2}, // overwritten by the install script
		"cstrike/de_new.bsp":   {40, 2}, // created by the install script
	}
	previous := fastDlManifest{"cstrike/halflife.wad": {20, 1}, "gone.wad": {1, 1}}

	got := buildFastDlManifest(before, after, previous)
	want := fastDlManifest{"cstrike/halflife.wad": {20, 1}, "cstrike/old.bsp": {31, 2}, "cstrike/de_new.bsp": {40, 2}}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %v want %v", k, got[k], v)
		}
	}

	// First install: everything the script wrote is original.
	if got := buildFastDlManifest(fastDlManifest{}, after, nil); len(got) != len(after) {
		t.Fatalf("first install: got %d entries want %d", len(got), len(after))
	}
}

func TestFastDlManifestScanAndIsOriginal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "maps", "de_dust2.bsp")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stock"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(root, "link.bsp")); err != nil {
		t.Fatal(err)
	}

	m, err := scanFastDlFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatalf("expected only the regular file, got %v", m)
	}

	info, _ := os.Stat(path)
	if !m.isOriginal("maps/de_dust2.bsp", info) {
		t.Fatal("untouched file should be original")
	}

	// Editing the file (new mtime) makes it a user file again.
	later := info.ModTime().Add(time.Second)
	_ = os.Chtimes(path, later, later)
	info, _ = os.Stat(path)
	if m.isOriginal("maps/de_dust2.bsp", info) {
		t.Fatal("modified file must not be original")
	}

	var nilManifest fastDlManifest
	if nilManifest.isOriginal("maps/de_dust2.bsp", info) {
		t.Fatal("no manifest means nothing is original")
	}
}

func TestRsyncLiteralPattern(t *testing.T) {
	cases := map[string]string{
		"cstrike/maps/de_dust2.bsp": "/cstrike/maps/de_dust2.bsp",
		"sound/[x] a*b?.wav":        `/sound/\[x] a\*b\?.wav`,
		`odd\name[1].wav`:           `/odd\\name\[1].wav`,
		`plain\name.wav`:            `/plain\name.wav`,
	}
	for in, want := range cases {
		if got, ok := rsyncLiteralPattern(in); !ok || got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	if _, ok := rsyncLiteralPattern("bad\nname"); ok {
		t.Error("newline names must be rejected")
	}
}
