package server

import (
	"bytes"
	"context"
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

func TestCompareWithFastDlReference(t *testing.T) {
	serverRoot, refDir := t.TempDir(), t.TempDir()
	write := func(root, rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(refDir, "cstrike/maps/de_dust2.bsp", "stock-dust2")
	write(refDir, "cstrike/maps/de_inferno.bsp", "STOCK-infern")
	write(refDir, "cstrike/sound/a.wav", "stock-sound")

	write(serverRoot, "cstrike/maps/de_dust2.bsp", "stock-dust2")    // untouched stock file
	write(serverRoot, "cstrike/maps/de_inferno.bsp", "USER-inferno") // replaced by the user, same size
	write(serverRoot, "cstrike/sound/a.wav", "edited")               // edited, different size
	write(serverRoot, "cstrike/maps/custom.bsp", "custom")           // user's own map

	m, compared, err := compareWithFastDlReference(context.Background(), serverRoot, refDir)
	if err != nil {
		t.Fatal(err)
	}
	if compared != 2 {
		t.Fatalf("compared %d files, want 2 (same path and size)", compared)
	}
	if len(m) != 1 {
		t.Fatalf("manifest %v, want only de_dust2.bsp", m)
	}
	info, _ := os.Stat(filepath.Join(serverRoot, "cstrike/maps/de_dust2.bsp"))
	if !m.isOriginal("cstrike/maps/de_dust2.bsp", info) {
		t.Fatal("manifest must hold the server file's own size and mtime")
	}
}

func TestSameFileContentLargeFiles(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("x"), 200<<10)
	a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	_ = os.WriteFile(a, big, 0o644)
	_ = os.WriteFile(b, big, 0o644)
	changed := append([]byte{}, big...)
	changed[len(changed)-1] = 'y'
	_ = os.WriteFile(c, changed, 0o644)

	if same, err := sameFileContent(a, b); err != nil || !same {
		t.Fatalf("identical files: same=%v err=%v", same, err)
	}
	if same, _ := sameFileContent(a, c); same {
		t.Fatal("files differing in the last byte must not match")
	}
}
