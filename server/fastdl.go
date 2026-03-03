package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"emperror.dev/errors"
)

type FastDlSyncRequest struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	RemotePath string `json:"remote_path"`
}

// SyncFastDL executes an rsync command to synchronize server files to a remote FastDL node.
func (s *Server) SyncFastDL(ctx context.Context, req FastDlSyncRequest) error {
	s.Log().Info("starting FastDL synchronization")

	localPath := s.Filesystem().Path()
	// Ensure the local path ends with a slash so rsync copies contents, not the folder itself
	if localPath[len(localPath)-1] != '/' {
		localPath += "/"
	}

	// The remote path should include the server UUID to keep files isolated.
	remotePath := filepath.Join(req.RemotePath, s.ID())

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
		cmd = exec.CommandContext(ctx, "rsync", "-avz", "--delete", "-e", sshArgs, localPath, fmt.Sprintf("%s@%s:%s/", req.User, req.Host, remotePath))
	} else if req.Password != "" {
		// Handle Password
		sshArgs = fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no", req.Port)
		cmd = exec.CommandContext(ctx, "sshpass", "-p", req.Password, "rsync", "-avz", "--delete", "-e", sshArgs, localPath, fmt.Sprintf("%s@%s:%s/", req.User, req.Host, remotePath))
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
