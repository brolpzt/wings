package server

import (
	"context"
	"strings"
	"sync"

	"emperror.dev/errors"
)

type FastDlSyncRequest struct {
	StorageType string `json:"storage_type"`

	// SSH / SFTP fields
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`

	// Shared fields
	RemotePath   string `json:"remote_path"`
	SyncPatterns string `json:"sync_patterns"`

	// S3-compatible fields
	Bucket                string `json:"bucket"`
	Endpoint              string `json:"endpoint"`
	Region                string `json:"region"`
	AccessKey             string `json:"access_key"`
	SecretKey             string `json:"secret_key"`
	UsePathStyleEndpoint  bool   `json:"use_path_style_endpoint"`
}

var fastDlRunning sync.Map

// SyncFastDL synchronizes server files to a remote FastDL target (SSH or S3-compatible).
// Only one synchronization per server runs at a time.
func (s *Server) SyncFastDL(ctx context.Context, req FastDlSyncRequest) error {
	if _, busy := fastDlRunning.LoadOrStore(s.ID(), struct{}{}); busy {
		s.Log().Warn("FastDL synchronization already running; ignoring new request")
		return errors.New("fastdl: synchronization already in progress")
	}
	defer fastDlRunning.Delete(s.ID())

	s.Log().Info("starting FastDL synchronization")

	storageType := strings.ToLower(strings.TrimSpace(req.StorageType))
	if storageType == "" {
		storageType = "ssh"
	}

	progress := s.startFastDlProgress(storageType)

	var err error
	switch storageType {
	case "s3":
		err = s.syncFastDlS3(ctx, req, progress)
	default:
		err = s.syncFastDlSSH(ctx, req, progress)
	}

	s.finishFastDlProgress(progress, err)
	if err != nil {
		return err
	}

	s.Log().Info("FastDL synchronization completed successfully")
	return nil
}
