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

func (s *Server) syncFastDlSSH(ctx context.Context, req FastDlSyncRequest) error {
	localPath := s.Filesystem().Path()
	if !strings.HasSuffix(localPath, "/") {
		localPath += "/"
	}

	shortID := s.ID()[:8]
	remotePath := filepath.Join(req.RemotePath, shortID)

	syncArgs := []string{"-avz", "--delete", "--prune-empty-dirs", "--chmod=ugo+rX", "--perms"}

	if req.SyncPatterns != "" {
		hasDirRule := false
		patterns := strings.Split(req.SyncPatterns, ",")

		for _, p := range patterns {
			p = strings.TrimSpace(p)
			if strings.HasSuffix(p, "/") || strings.HasPrefix(p, "/") {
				hasDirRule = true
				break
			}
		}

		if !hasDirRule {
			syncArgs = append(syncArgs, "--include=*/")
		}

		for _, p := range patterns {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}

			if strings.HasSuffix(p, "/") {
				syncArgs = append(syncArgs, fmt.Sprintf("--include=%s", p))
				syncArgs = append(syncArgs, fmt.Sprintf("--include=%s**/", p))
			} else {
				syncArgs = append(syncArgs, fmt.Sprintf("--include=%s", p))
			}
		}

		syncArgs = append(syncArgs, "--exclude=*")
	}

	var sshArgs string
	var cmd *exec.Cmd
	var keyFile string

	if req.PrivateKey != "" {
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

	return nil
}
