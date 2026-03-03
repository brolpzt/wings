package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"emperror.dev/errors"
)

type FastDlSyncRequest struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	Password     string `json:"password"`
	PrivateKey   string `json:"private_key"`
	RemotePath   string `json:"remote_path"`
	SyncPatterns string `json:"sync_patterns"`
}

// SyncFastDL executes an rsync command to synchronize server files to a remote FastDL node.
func (s *Server) SyncFastDL(ctx context.Context, req FastDlSyncRequest) error {
	s.Log().Info("starting FastDL synchronization")

	localPath := s.Filesystem().Path()
	// Ensure the local path ends with a slash so rsync copies contents, not the folder itself
	if !strings.HasSuffix(localPath, "/") {
		localPath += "/"
	}

	// The remote path uses the first 8 characters of the UUID (uuidShort),
	// matching what the Panel displays as the FastDL URL path.
	uuidShort := s.ID()
	if len(uuidShort) > 8 {
		uuidShort = uuidShort[:8]
	}
	remotePath := filepath.Join(req.RemotePath, uuidShort)

	// Build rsync arguments
	syncArgs := []string{"-avz", "--delete", "--prune-empty-dirs"}

	if req.SyncPatterns != "" {
		hasDirRule := false
		patterns := strings.Split(req.SyncPatterns, ",")

		// First, pass: check if there are any directory rules
		for _, p := range patterns {
			p = strings.TrimSpace(p)
			if strings.HasSuffix(p, "/") || strings.HasPrefix(p, "/") {
				hasDirRule = true
				break
			}
		}

		// If no directory rule was passed, default to all directories traversal
		if !hasDirRule {
			syncArgs = append(syncArgs, "--include=*/")
		}

		// Split by comma and add each pattern as an include
		for _, p := range patterns {
			p = strings.TrimSpace(p)
			if p != "" {
				// If it's a directory rule, make sure we also traverse its subdirectories
				if strings.HasSuffix(p, "/") {
					syncArgs = append(syncArgs, fmt.Sprintf("--include=%s", p))
					syncArgs = append(syncArgs, fmt.Sprintf("--include=%s**/", p))
				} else {
					syncArgs = append(syncArgs, fmt.Sprintf("--include=%s", p))
				}
			}
		}

		// Exclude everything else
		syncArgs = append(syncArgs, "--exclude=*")
	}

	var sshArgs string
	var cmd *exec.Cmd
	var keyFile string

	if req.PrivateKey != "" {
		// Handle Private Key
		tmpKey, err := os.CreateTemp("", "wings-fastdl-*")
		if err != nil {
			return errors.WrapIf(err, "fastdl: could not create temporary key file")
		}
		keyFile = tmpKey.Name()
		defer os.Remove(keyFile)

		if _, err := tmpKey.WriteString(req.PrivateKey); err != nil {
			return errors.WrapIf(err, "fastdl: could not write private key to disk")
		}
		_ = tmpKey.Close()
		_ = os.Chmod(keyFile, 0600)

		sshArgs = fmt.Sprintf("ssh -i %s -p %d -o StrictHostKeyChecking=no", keyFile, req.Port)

		args := append(syncArgs, "-e", sshArgs, localPath, fmt.Sprintf("%s@%s:%s/", req.User, req.Host, remotePath))
		cmd = exec.CommandContext(ctx, "rsync", args...)
	} else if req.Password != "" {
		// Handle Password
		sshArgs = fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no", req.Port)

		args := []string{"-p", req.Password, "rsync"}
		args = append(args, syncArgs...)
		args = append(args, "-e", sshArgs, localPath, fmt.Sprintf("%s@%s:%s/", req.User, req.Host, remotePath))
		cmd = exec.CommandContext(ctx, "sshpass", args...)
	} else {
		return errors.New("fastdl: no password or private key provided for authentication")
	}

	s.Log().WithField("command", cmd.String()).Debug("executing rsync command for FastDL")

	output, err := cmd.CombinedOutput()
	if err != nil {
		s.Log().WithField("output", string(output)).WithError(err).Error("failed to sync FastDL files")
		return errors.WrapIf(err, "fastdl: rsync command failed")
	}

	s.Log().Info("FastDL synchronization completed successfully")
	return nil
}
