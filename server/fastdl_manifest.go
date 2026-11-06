package server

import (
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"emperror.dev/errors"

	"github.com/pterodactyl/wings/config"
)

// fastDlManifest lists the files the egg installation script delivered
// ("original" game files), keyed by slash-separated path relative to the
// server root, with their size and modification time (unix nanoseconds).
//
// FastDL synchronization skips a file while it still has the exact size and
// mtime recorded here: players already have the stock game files. A file the
// user replaces or edits gets a new mtime and is synchronized again.
type fastDlManifest map[string][2]int64

func fastDlManifestPath(uuid string) string {
	return filepath.Join(config.Get().System.RootDirectory, "fastdl", uuid+".json.gz")
}

func fastDlEntry(info fs.FileInfo) [2]int64 {
	return [2]int64{info.Size(), info.ModTime().UnixNano()}
}

// scanFastDlFiles returns every regular file below root (symlinks are not followed).
func scanFastDlFiles(root string) (fastDlManifest, error) {
	files := make(fastDlManifest)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable entries cannot be original files we know about; keep going.
			if d != nil && d.IsDir() && path != root {
				return filepath.SkipDir
			}
			if path == root {
				return err
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		files[filepath.ToSlash(rel)] = fastDlEntry(info)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// buildFastDlManifest decides which files are original after an installation.
// A file is original when the install created or changed it (it differs from
// the snapshot taken before the install), or when it was already original
// before and is still untouched. On a first install everything is original;
// on a reinstall the user's own files, which the script left alone, are not.
func buildFastDlManifest(before, after, previous fastDlManifest) fastDlManifest {
	manifest := make(fastDlManifest, len(after))
	for path, entry := range after {
		old, existed := before[path]
		if !existed || old != entry {
			manifest[path] = entry
			continue
		}
		if prev, ok := previous[path]; ok && prev == entry {
			manifest[path] = entry
		}
	}
	return manifest
}

// isOriginal reports whether rel is still the file the installation delivered.
func (m fastDlManifest) isOriginal(rel string, info fs.FileInfo) bool {
	entry, ok := m[rel]
	return ok && entry == fastDlEntry(info)
}

// loadFastDlManifest returns nil (synchronize everything) when the server has
// no manifest, e.g. it was installed before manifests existed.
func (s *Server) loadFastDlManifest() fastDlManifest {
	f, err := os.Open(fastDlManifestPath(s.ID()))
	if err != nil {
		if !os.IsNotExist(err) {
			s.Log().WithField("error", err).Warn("fastdl: could not open install manifest; synchronizing every file")
		}
		return nil
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		s.Log().WithField("error", err).Warn("fastdl: install manifest is corrupt; synchronizing every file")
		return nil
	}
	defer gz.Close()

	var m fastDlManifest
	if err := json.NewDecoder(gz).Decode(&m); err != nil {
		s.Log().WithField("error", err).Warn("fastdl: install manifest is corrupt; synchronizing every file")
		return nil
	}
	return m
}

func (s *Server) saveFastDlManifest(m fastDlManifest) error {
	path := fastDlManifestPath(s.ID())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.WrapIf(err, "fastdl: could not create manifest directory")
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return errors.WrapIf(err, "fastdl: could not create manifest file")
	}
	defer os.Remove(tmp.Name())

	gz := gzip.NewWriter(tmp)
	if err := json.NewEncoder(gz).Encode(m); err != nil {
		_ = tmp.Close()
		return errors.WrapIf(err, "fastdl: could not write manifest")
	}
	if err := gz.Close(); err != nil {
		_ = tmp.Close()
		return errors.WrapIf(err, "fastdl: could not write manifest")
	}
	if err := tmp.Close(); err != nil {
		return errors.WrapIf(err, "fastdl: could not write manifest")
	}
	return errors.WrapIf(os.Rename(tmp.Name(), path), "fastdl: could not save manifest")
}

// RemoveFastDlManifest deletes the server's install manifest (server deletion).
func (s *Server) RemoveFastDlManifest() {
	if err := os.Remove(fastDlManifestPath(s.ID())); err != nil && !os.IsNotExist(err) {
		s.Log().WithField("error", err).Warn("fastdl: could not remove install manifest")
	}
}

// fastDlInstallSnapshot is taken right before an install script runs.
type fastDlInstallSnapshot struct {
	files fastDlManifest
	ok    bool
}

func (s *Server) snapshotForFastDlManifest() fastDlInstallSnapshot {
	files, err := scanFastDlFiles(s.Filesystem().Path())
	if err != nil {
		s.Log().WithField("error", err).Warn("fastdl: could not scan files before install; manifest will not be updated")
		return fastDlInstallSnapshot{}
	}
	return fastDlInstallSnapshot{files: files, ok: true}
}

// recordFastDlManifest stores the original files after a successful install.
// It runs before the Panel is notified, because the Panel starts a FastDL
// synchronization as soon as it hears the install finished.
func (s *Server) recordFastDlManifest(snapshot fastDlInstallSnapshot) {
	if !snapshot.ok {
		return
	}
	after, err := scanFastDlFiles(s.Filesystem().Path())
	if err != nil {
		s.Log().WithField("error", err).Warn("fastdl: could not scan files after install; manifest not updated")
		return
	}

	manifest := buildFastDlManifest(snapshot.files, after, s.loadFastDlManifest())
	if err := s.saveFastDlManifest(manifest); err != nil {
		s.Log().WithField("error", err).Warn("fastdl: could not save install manifest")
		return
	}
	s.Log().WithField("original_files", len(manifest)).Info("fastdl: install manifest saved")
}

// rsyncLiteralPattern turns a relative path into an anchored rsync pattern that
// matches only that path. Returns false for names rsync filter files cannot hold.
func rsyncLiteralPattern(rel string) (string, bool) {
	if strings.ContainsAny(rel, "\n\r") {
		return "", false
	}
	if strings.ContainsAny(rel, "*?[") {
		// Backslash escapes only count when the pattern has a wildcard character.
		r := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)
		rel = r.Replace(rel)
	}
	return "/" + rel, true
}
