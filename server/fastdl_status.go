package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pterodactyl/wings/config"
)

// FastDlSyncStatus is the progress of the running FastDL synchronization or
// the result of the last one (kept on disk so it survives Wings restarts).
type FastDlSyncStatus struct {
	State           string     `json:"state"`           // never | running | completed | failed
	Phase           string     `json:"phase,omitempty"` // scanning | comparing | uploading | cleaning
	StorageType     string     `json:"storage_type,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	MatchedFiles    int64      `json:"matched_files"`
	SkippedOriginal int64      `json:"skipped_original_files"`
	FilesTotal      int64      `json:"files_total"` // files that need uploading
	FilesDone       int64      `json:"files_done"`
	FilesFailed     int64      `json:"files_failed"`
	BytesTotal      int64      `json:"bytes_total"`
	BytesDone       int64      `json:"bytes_done"`
	Deleted         int64      `json:"deleted_files"`
	Error           string     `json:"error,omitempty"`
}

type fastDlProgress struct {
	mu sync.Mutex
	st FastDlSyncStatus
}

func (p *fastDlProgress) update(fn func(st *FastDlSyncStatus)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	fn(&p.st)
	p.mu.Unlock()
}

func (p *fastDlProgress) snapshot() FastDlSyncStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

var fastDlProgresses sync.Map // server uuid -> *fastDlProgress

func fastDlStatusPath(uuid string) string {
	return filepath.Join(config.Get().System.RootDirectory, "fastdl", uuid+".sync.json")
}

func (s *Server) startFastDlProgress(storageType string) *fastDlProgress {
	now := time.Now()
	p := &fastDlProgress{st: FastDlSyncStatus{State: "running", Phase: "scanning", StorageType: storageType, StartedAt: &now}}
	fastDlProgresses.Store(s.ID(), p)
	return p
}

// finishFastDlProgress records the outcome and keeps it on disk.
func (s *Server) finishFastDlProgress(p *fastDlProgress, err error) {
	now := time.Now()
	p.update(func(st *FastDlSyncStatus) {
		st.FinishedAt, st.Phase = &now, ""
		if err != nil {
			st.State, st.Error = "failed", err.Error()
		} else {
			st.State = "completed"
		}
	})

	st := p.snapshot()
	path := fastDlStatusPath(s.ID())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(st); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, path)
		}
	}
}

// FastDlSyncStatus returns the running sync's progress, else the last result.
func (s *Server) FastDlSyncStatus() FastDlSyncStatus {
	if v, ok := fastDlProgresses.Load(s.ID()); ok {
		return v.(*fastDlProgress).snapshot()
	}
	if b, err := os.ReadFile(fastDlStatusPath(s.ID())); err == nil {
		var st FastDlSyncStatus
		if json.Unmarshal(b, &st) == nil {
			if st.State == "running" {
				st.State, st.Error = "failed", "interrupted (Wings restarted)"
			}
			return st
		}
	}
	return FastDlSyncStatus{State: "never"}
}

// FastDlSyncRunning reports whether a synchronization is running for the server.
func (s *Server) FastDlSyncRunning() bool {
	_, busy := fastDlRunning.Load(s.ID())
	return busy
}
