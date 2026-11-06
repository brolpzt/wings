package server

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"emperror.dev/errors"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/environment"
)

// Reference manifests: for servers installed before manifests existed, the egg
// install script runs again into a scratch directory (never into the server),
// and every server file identical to the freshly installed one (same path, same
// bytes) is recorded as original. The scratch directory and the container are
// always removed afterwards; leftovers from a crash are removed on boot.

const fastDlReferenceTimeout = 2 * time.Hour

// One reference install per node at a time: they download whole games.
var fastDlReferenceSlot = make(chan struct{}, 1)

// FastDlManifestStatus is what the Panel shows about a server's manifest.
type FastDlManifestStatus struct {
	Exists        bool       `json:"exists"`
	OriginalFiles int        `json:"original_files"`
	UpdatedAt     *time.Time `json:"updated_at"`
	State         string     `json:"state"` // idle | queued | running
	LastError     string     `json:"last_error,omitempty"`
	LastFinished  *time.Time `json:"last_finished_at,omitempty"`
}

type fastDlReferenceState struct {
	state    string
	err      string
	finished *time.Time
}

var fastDlReferenceStates sync.Map // server uuid -> fastDlReferenceState

func fastDlReferenceRoot() string {
	return filepath.Join(config.Get().System.RootDirectory, "fastdl", "ref")
}

// CleanupFastDlReferences removes containers and scratch directories left behind
// by a reference install interrupted by a Wings crash or restart. Call on boot.
func CleanupFastDlReferences(ctx context.Context, servers []*Server) error {
	if c, err := environment.Docker(); err == nil {
		for _, s := range servers {
			err := c.ContainerRemove(ctx, s.ID()+"_fastdl_ref", container.RemoveOptions{RemoveVolumes: true, Force: true})
			if err != nil && !client.IsErrNotFound(err) {
				s.Log().WithField("error", err).Warn("fastdl: could not remove leftover reference container")
			}
			_ = os.RemoveAll(filepath.Join(config.Get().System.TmpDirectory, s.ID()+"_fastdl_ref"))
		}
	}
	return os.RemoveAll(fastDlReferenceRoot())
}

// FastDlManifestStatus reports the server's manifest and reference install state.
func (s *Server) FastDlManifestStatus() FastDlManifestStatus {
	st := FastDlManifestStatus{State: "idle"}
	if v, ok := fastDlReferenceStates.Load(s.ID()); ok {
		ref := v.(fastDlReferenceState)
		st.State, st.LastError, st.LastFinished = ref.state, ref.err, ref.finished
	}
	if info, err := os.Stat(fastDlManifestPath(s.ID())); err == nil {
		t := info.ModTime()
		st.Exists, st.UpdatedAt = true, &t
		st.OriginalFiles = len(s.loadFastDlManifest())
	}
	return st
}

// QueueFastDlReferenceManifest starts a reference install in the background.
// Returns false when one is already queued or running for this server.
func (s *Server) QueueFastDlReferenceManifest() bool {
	if v, ok := fastDlReferenceStates.Load(s.ID()); ok {
		if state := v.(fastDlReferenceState).state; state == "queued" || state == "running" {
			return false
		}
	}
	fastDlReferenceStates.Store(s.ID(), fastDlReferenceState{state: "queued"})

	go func() {
		err := s.generateFastDlReferenceManifest()
		now := time.Now()
		ref := fastDlReferenceState{state: "idle", finished: &now}
		if err != nil {
			ref.err = err.Error()
			s.Log().WithField("error", err).Error("fastdl: reference manifest failed")
		}
		fastDlReferenceStates.Store(s.ID(), ref)
	}()
	return true
}

func (s *Server) generateFastDlReferenceManifest() error {
	ctx, cancel := context.WithTimeout(s.Context(), fastDlReferenceTimeout)
	defer cancel()

	select {
	case fastDlReferenceSlot <- struct{}{}:
	case <-ctx.Done():
		return errors.New("fastdl: gave up waiting for another reference install to finish")
	}
	defer func() { <-fastDlReferenceSlot }()

	fastDlReferenceStates.Store(s.ID(), fastDlReferenceState{state: "running"})

	if s.IsInProtectedState() {
		return errors.New("fastdl: server is installing, transferring or restoring")
	}

	// The reference needs about as much space as the server itself.
	refRoot := fastDlReferenceRoot()
	if err := os.MkdirAll(refRoot, 0o700); err != nil {
		return errors.WrapIf(err, "fastdl: could not create reference directory")
	}
	usage, _ := s.Filesystem().DiskUsage(true)
	var stat unix.Statfs_t
	if err := unix.Statfs(refRoot, &stat); err == nil {
		free := int64(stat.Bavail) * int64(stat.Bsize)
		if need := usage + usage/5 + 1<<30; free < need {
			return errors.Errorf("fastdl: not enough free disk for a reference install (free %d MiB, need ~%d MiB)", free>>20, need>>20)
		}
	}

	refDir, err := os.MkdirTemp(refRoot, s.ID()+"-")
	if err != nil {
		return errors.WrapIf(err, "fastdl: could not create reference directory")
	}
	// Always remove the scratch install, whatever happens below.
	defer func() {
		if err := os.RemoveAll(refDir); err != nil {
			s.Log().WithField("path", refDir).WithField("error", err).Error("fastdl: could not remove reference install directory")
		}
	}()

	script, err := s.client.GetInstallationScript(ctx, s.ID())
	if err != nil {
		return errors.WrapIf(err, "fastdl: could not fetch installation script")
	}
	c, err := environment.Docker()
	if err != nil {
		return err
	}
	ip := &InstallationProcess{Server: s, Script: &script, client: c, refDir: refDir, refCtx: ctx}
	defer func() {
		_ = ip.RemoveContainer()
		_ = os.RemoveAll(ip.tempDir())
	}()

	s.Log().WithField("path", refDir).Info("fastdl: running reference install for manifest")
	if err := ip.BeforeExecute(); err != nil {
		return err
	}
	cID, err := ip.Execute()
	if err != nil {
		return errors.WrapIf(err, "fastdl: reference install failed")
	}
	if err := ip.AfterExecute(cID); err != nil {
		s.Log().WithField("error", err).Warn("fastdl: could not save reference install log")
	}
	if ip.exitCode != 0 {
		return errors.Errorf("fastdl: reference install script exited with code %d (see %s)", ip.exitCode, ip.GetLogPath())
	}
	if err := ctx.Err(); err != nil {
		return errors.WrapIf(err, "fastdl: reference install interrupted")
	}

	manifest, compared, err := compareWithFastDlReference(ctx, s.Filesystem().Path(), refDir)
	if err != nil {
		return err
	}
	if len(manifest) == 0 {
		return errors.Errorf("fastdl: no server file matched the reference install (%d files compared); manifest not saved", compared)
	}
	if err := s.saveFastDlManifest(manifest); err != nil {
		return err
	}

	s.Log().WithField("original_files", len(manifest)).WithField("compared_files", compared).
		Info("fastdl: reference manifest saved")
	return nil
}

// compareWithFastDlReference returns the server files that are byte-identical to
// the reference install, keyed with the server file's own size and mtime.
func compareWithFastDlReference(ctx context.Context, serverRoot, refDir string) (fastDlManifest, int, error) {
	reference, err := scanFastDlFiles(refDir)
	if err != nil {
		return nil, 0, errors.WrapIf(err, "fastdl: could not scan reference install")
	}

	manifest := make(fastDlManifest)
	compared := 0
	err = filepath.WalkDir(serverRoot, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(serverRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		ref, ok := reference[rel]
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() != ref[0] {
			return nil
		}
		compared++
		if same, _ := sameFileContent(path, filepath.Join(refDir, filepath.FromSlash(rel))); same {
			manifest[rel] = fastDlEntry(info)
		}
		return nil
	})
	if err != nil {
		return nil, compared, err
	}
	return manifest, compared, nil
}

func sameFileContent(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()

	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		endA := errA == io.EOF || errA == io.ErrUnexpectedEOF
		endB := errB == io.EOF || errB == io.ErrUnexpectedEOF
		if endA || endB {
			return endA && endB, nil
		}
		if errA != nil {
			return false, errA
		}
		if errB != nil {
			return false, errB
		}
	}
}
