package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/pterodactyl/wings/config"
)

// Automatic FastDL synchronization. The Panel turns it on per server in the
// sync request (auto_sync) and Wings keeps the file types on disk. Wings then
// watches the server directory (inotify) and, once files that FastDL would
// upload stop changing, asks the Panel to synchronize. Wings never holds the
// storage credentials outside a sync: the Panel sends them with each sync.
//
// A periodic local scan backs up the watcher (lost events, full inotify queue,
// directories without a watch): it compares a fingerprint of the files FastDL
// would upload with the one taken after the last successful synchronization.

// Variables so tests can shorten them.
var (
	fastDlWatchQuiet    = 60 * time.Second // no changes for this long → sync
	fastDlWatchMaxWait  = 5 * time.Minute  // keep busy servers from never syncing
	fastDlWatchScan     = 10 * time.Minute // periodic fallback scan
	fastDlWatchRetry    = 5 * time.Minute  // Panel unreachable
	fastDlWatchBusy     = 30 * time.Second // a sync or install is running
	fastDlWatchRenotify = time.Hour        // same change already reported
)

type fastDlWatchConfig struct {
	Patterns string `json:"patterns"`
	// Fingerprint of the files FastDL would upload, after the last successful sync.
	Fingerprint string `json:"fingerprint,omitempty"`
}

type fastDlWatcher struct {
	s      *Server
	cancel context.CancelFunc

	mu           sync.Mutex
	cfg          fastDlWatchConfig
	matcher      *fastDlPatternMatcher
	timer        *time.Timer
	firstChange  time.Time
	notifiedFp   string
	notifiedAt   time.Time
	limitWarned  bool
	watchedPaths int
	// dirs holds the watched directories (only used by the run goroutine), so
	// removing or moving out a whole folder (e.g. a mod) is noticed.
	dirs map[string]struct{}
}

var fastDlWatchers sync.Map // server uuid -> *fastDlWatcher

func fastDlWatchPath(uuid string) string {
	return filepath.Join(config.Get().System.RootDirectory, "fastdl", uuid+".watch.json")
}

func loadFastDlWatchConfig(uuid string) (fastDlWatchConfig, bool) {
	var cfg fastDlWatchConfig
	b, err := os.ReadFile(fastDlWatchPath(uuid))
	if err != nil || json.Unmarshal(b, &cfg) != nil {
		return cfg, false
	}
	return cfg, true
}

func saveFastDlWatchConfig(uuid string, cfg fastDlWatchConfig) error {
	path := fastDlWatchPath(uuid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// StartFastDlWatchers resumes automatic synchronization after a Wings restart.
func StartFastDlWatchers(servers []*Server) {
	for _, s := range servers {
		if cfg, ok := loadFastDlWatchConfig(s.ID()); ok {
			s.startFastDlWatcher(cfg)
		}
	}
}

// ConfigureFastDlWatch turns automatic synchronization on (with the file types
// of the current sync request) or off.
func (s *Server) ConfigureFastDlWatch(enabled bool, patterns string) {
	if !enabled {
		s.StopFastDlWatch()
		_ = os.Remove(fastDlWatchPath(s.ID()))
		return
	}

	if v, ok := fastDlWatchers.Load(s.ID()); ok {
		w := v.(*fastDlWatcher)
		w.mu.Lock()
		changed := w.cfg.Patterns != patterns
		w.cfg.Patterns = patterns
		w.matcher = newFastDlPatternMatcher(patterns)
		cfg := w.cfg
		w.mu.Unlock()
		if changed {
			if err := saveFastDlWatchConfig(s.ID(), cfg); err != nil {
				s.Log().WithField("error", err).Warn("fastdl: could not save auto-sync settings")
			}
		}
		return
	}

	cfg, _ := loadFastDlWatchConfig(s.ID())
	cfg.Patterns = patterns
	if err := saveFastDlWatchConfig(s.ID(), cfg); err != nil {
		s.Log().WithField("error", err).Warn("fastdl: could not save auto-sync settings")
	}
	s.startFastDlWatcher(cfg)
}

// StopFastDlWatch stops the watcher (server deleted or auto-sync turned off).
func (s *Server) StopFastDlWatch() {
	if v, ok := fastDlWatchers.LoadAndDelete(s.ID()); ok {
		v.(*fastDlWatcher).cancel()
	}
}

// fastDlWatchFingerprint is taken when a sync starts: a file that changes while
// the sync runs is then still seen as new afterwards.
func (s *Server) fastDlWatchFingerprint() string {
	v, ok := fastDlWatchers.Load(s.ID())
	if !ok {
		return ""
	}
	fp, err := v.(*fastDlWatcher).fingerprint()
	if err != nil {
		s.Log().WithField("error", err).Warn("fastdl: could not fingerprint files before sync")
		return ""
	}
	return fp
}

// fastDlSyncSucceeded records what was synchronized (fingerprint taken at the
// start), so the watcher only reacts to changes made after it.
func (s *Server) fastDlSyncSucceeded(fp string) {
	v, ok := fastDlWatchers.Load(s.ID())
	if !ok || fp == "" {
		return
	}
	w := v.(*fastDlWatcher)
	w.mu.Lock()
	w.cfg.Fingerprint = fp
	cfg := w.cfg
	w.mu.Unlock()
	if err := saveFastDlWatchConfig(s.ID(), cfg); err != nil {
		s.Log().WithField("error", err).Warn("fastdl: could not save auto-sync settings")
	}
}

func (s *Server) startFastDlWatcher(cfg fastDlWatchConfig) {
	ctx, cancel := context.WithCancel(s.Context())
	w := &fastDlWatcher{s: s, cancel: cancel, cfg: cfg, matcher: newFastDlPatternMatcher(cfg.Patterns), dirs: map[string]struct{}{}}
	if _, loaded := fastDlWatchers.LoadOrStore(s.ID(), w); loaded {
		cancel()
		return
	}
	go w.run(ctx)
}

func (w *fastDlWatcher) run(ctx context.Context) {
	defer fastDlWatchers.CompareAndDelete(w.s.ID(), w)

	root := w.s.Filesystem().Path()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		w.s.Log().WithField("error", err).Warn("fastdl: file watcher unavailable; using periodic scans only")
	} else {
		defer watcher.Close()
		w.addTree(watcher, root)
		w.s.Log().WithField("directories", w.watchedPaths).Info("fastdl: watching files for automatic synchronization")
	}

	// A change made while Wings was down is picked up by the first scan.
	w.schedule(fastDlWatchQuiet)

	ticker := time.NewTicker(fastDlWatchScan)
	defer ticker.Stop()

	var events chan fsnotify.Event
	var errs chan error
	if watcher != nil {
		events, errs = watcher.Events, watcher.Errors
	}

	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			if w.timer != nil {
				w.timer.Stop()
			}
			w.mu.Unlock()
			return
		case <-ticker.C:
			w.schedule(0)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			// Queue overflow and similar: compare everything soon.
			w.s.Log().WithField("error", err).Debug("fastdl: file watcher error")
			w.change()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			w.handle(watcher, root, ev)
		}
	}
}

func (w *fastDlWatcher) addTree(watcher *fsnotify.Watcher, dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if _, ok := w.dirs[path]; ok {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			w.mu.Lock()
			warned := w.limitWarned
			w.limitWarned = true
			w.mu.Unlock()
			if !warned {
				w.s.Log().WithField("error", err).WithField("path", path).
					Warn("fastdl: could not watch every directory (raise fs.inotify.max_user_watches); periodic scans still catch changes")
			}
			return filepath.SkipDir
		}
		w.dirs[path] = struct{}{}
		w.watchedPaths++
		return nil
	})
}

func (w *fastDlWatcher) handle(watcher *fsnotify.Watcher, root string, ev fsnotify.Event) {
	if ev.Op == fsnotify.Chmod {
		return
	}
	if ev.Has(fsnotify.Create) {
		if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() {
			// New or moved-in directory: watch it and look at what came with it.
			w.addTree(watcher, ev.Name)
			w.change()
			return
		}
	}
	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		if _, ok := w.dirs[ev.Name]; ok {
			// A folder was deleted or moved away with everything in it.
			prefix := ev.Name + string(filepath.Separator)
			for d := range w.dirs {
				if d == ev.Name || len(d) > len(prefix) && d[:len(prefix)] == prefix {
					delete(w.dirs, d)
					w.watchedPaths--
				}
			}
			w.change()
			return
		}
	}
	rel, err := filepath.Rel(root, ev.Name)
	if err != nil {
		return
	}
	w.mu.Lock()
	m := w.matcher
	w.mu.Unlock()
	if m.shouldSync(rel) {
		w.change()
	}
}

// change restarts the quiet period, up to fastDlWatchMaxWait after the first change.
func (w *fastDlWatcher) change() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if w.firstChange.IsZero() {
		w.firstChange = now
	}
	delay := fastDlWatchQuiet
	if left := fastDlWatchMaxWait - now.Sub(w.firstChange); left < delay {
		delay = max(left, 0)
	}
	w.resetTimerLocked(delay)
}

func (w *fastDlWatcher) schedule(delay time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil && delay == 0 {
		return // a check is already pending
	}
	w.resetTimerLocked(delay)
}

func (w *fastDlWatcher) resetTimerLocked(delay time.Duration) {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(delay, w.check)
}

// check asks the Panel to synchronize when the files FastDL would upload differ
// from the last successful synchronization.
func (w *fastDlWatcher) check() {
	w.mu.Lock()
	w.timer = nil
	w.mu.Unlock()

	if w.s.Context().Err() != nil {
		return
	}
	if w.s.IsInProtectedState() || w.s.FastDlSyncRunning() {
		w.schedule(fastDlWatchBusy)
		return
	}

	fp, err := w.fingerprint()
	if err != nil {
		w.s.Log().WithField("error", err).Warn("fastdl: could not scan files for automatic synchronization")
		return
	}

	w.mu.Lock()
	w.firstChange = time.Time{}
	if fp == w.cfg.Fingerprint || (fp == w.notifiedFp && time.Since(w.notifiedAt) < fastDlWatchRenotify) {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(w.s.Context(), 30*time.Second)
	defer cancel()
	keep, err := w.s.client.NotifyFastDlChanged(ctx, w.s.ID())
	if err != nil {
		w.s.Log().WithField("error", err).Warn("fastdl: could not ask the Panel for an automatic synchronization; retrying later")
		w.schedule(fastDlWatchRetry)
		return
	}
	if !keep {
		w.s.Log().Info("fastdl: automatic synchronization is off for this server; no longer watching files")
		go w.s.ConfigureFastDlWatch(false, "")
		return
	}

	w.mu.Lock()
	w.notifiedFp, w.notifiedAt = fp, time.Now()
	w.mu.Unlock()
	w.s.Log().Info("fastdl: new or changed files detected; automatic synchronization requested")
}

// fingerprint hashes path, size and mtime of every file FastDL would upload.
func (w *fastDlWatcher) fingerprint() (string, error) {
	w.mu.Lock()
	m := w.matcher
	w.mu.Unlock()

	root := w.s.Filesystem().Path()
	manifest := w.s.loadFastDlManifest()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !m.shouldSync(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if manifest != nil && manifest.isOriginal(rel, info) && !m.isForced(rel) {
			return nil
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", rel, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
