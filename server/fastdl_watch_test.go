package server

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/remote"
	"github.com/pterodactyl/wings/server/filesystem"
)

type fastDlWatchTestClient struct {
	remote.Client
	notified atomic.Int32
}

func (c *fastDlWatchTestClient) NotifyFastDlChanged(context.Context, string) (bool, error) {
	c.notified.Add(1)
	return true, nil
}

func TestFastDlWatcherNotifiesOnMatchingFiles(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Configuration{AuthenticationToken: "test-token"}
	cfg.System.RootDirectory = filepath.Join(root, "wings")
	config.Set(cfg)

	quiet, scan := fastDlWatchQuiet, fastDlWatchScan
	fastDlWatchQuiet, fastDlWatchScan = 300*time.Millisecond, time.Hour
	defer func() { fastDlWatchQuiet, fastDlWatchScan = quiet, scan }()

	data := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(data, "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "main", "iw_00.iwd"), []byte("stock"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := &fastDlWatchTestClient{}
	s, err := New(client)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Uuid = "2db32214-0000-0000-0000-000000000000"
	if s.fs, err = filesystem.New(data, 0, nil); err != nil {
		t.Fatal(err)
	}
	defer s.CtxCancel()

	// The stock file is original: nothing to report.
	info, _ := os.Stat(filepath.Join(data, "main", "iw_00.iwd"))
	if err := s.saveFastDlManifest(fastDlManifest{"main/iw_00.iwd": fastDlEntry(info)}); err != nil {
		t.Fatal(err)
	}

	s.ConfigureFastDlWatch(true, "*.iwd\n*.pk3")
	defer s.StopFastDlWatch()
	if _, err := os.Stat(fastDlWatchPath(s.ID())); err != nil {
		t.Fatalf("watch settings not saved: %v", err)
	}

	// Simulate a completed sync of the current state.
	s.fastDlSyncSucceeded(s.fastDlWatchFingerprint())
	time.Sleep(700 * time.Millisecond)
	if n := client.notified.Load(); n != 0 {
		t.Fatalf("expected no notification before changes, got %d", n)
	}

	// A config file does not match the file types.
	_ = os.WriteFile(filepath.Join(data, "main", "server.cfg"), []byte("x"), 0o644)
	time.Sleep(700 * time.Millisecond)
	if n := client.notified.Load(); n != 0 {
		t.Fatalf("cfg change must not notify, got %d", n)
	}

	// A new mod in a new fs_game folder: several writes, one notification.
	_ = os.MkdirAll(filepath.Join(data, "zombies"), 0o755)
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		_ = os.WriteFile(filepath.Join(data, "zombies", "zom.iwd"), []byte{byte(i)}, 0o644)
		time.Sleep(100 * time.Millisecond)
	}
	deadline := time.Now().Add(3 * time.Second)
	for client.notified.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if n := client.notified.Load(); n != 1 {
		t.Fatalf("expected exactly one notification, got %d", n)
	}

	// Turning it off removes the settings.
	s.ConfigureFastDlWatch(false, "")
	if _, err := os.Stat(fastDlWatchPath(s.ID())); !os.IsNotExist(err) {
		t.Fatalf("watch settings not removed: %v", err)
	}
}
