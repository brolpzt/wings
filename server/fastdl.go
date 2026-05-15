package server

import (
	"context"
	"strings"
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

// SyncFastDL synchronizes server files to a remote FastDL target (SSH or S3-compatible).
func (s *Server) SyncFastDL(ctx context.Context, req FastDlSyncRequest) error {
	s.Log().Info("starting FastDL synchronization")

	storageType := strings.ToLower(strings.TrimSpace(req.StorageType))
	if storageType == "" {
		storageType = "ssh"
	}

	var err error
	switch storageType {
	case "s3":
		err = s.syncFastDlS3(ctx, req)
	default:
		err = s.syncFastDlSSH(ctx, req)
	}

	if err != nil {
		return err
	}

	s.Log().Info("FastDL synchronization completed successfully")
	return nil
}
