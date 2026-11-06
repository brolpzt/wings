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

	// Stock game files the install delivered are excluded first (rsync: first matching rule wins).
	excludeFile, skippedOriginal, err := s.writeFastDlOriginalExcludes(localPath)
	if err != nil {
		return err
	}
	if excludeFile != "" {
		defer os.Remove(excludeFile)
		syncArgs = append(syncArgs, "--exclude-from="+excludeFile)
		s.Log().WithField("skipped_original_files", skippedOriginal).Info("fastdl: excluding original install files")
	}

	if req.SyncPatterns != "" {
		includes, excludes := parseFastDlSyncPatternLists(req.SyncPatterns)

		// Only user excludes: rsync copies everything except those (no include filter).
		if len(includes) == 0 && len(excludes) > 0 {
			for _, ex := range excludes {
				ex = strings.TrimSpace(ex)
				if ex == "" {
					continue
				}
				syncArgs = append(syncArgs, fmt.Sprintf("--exclude=%s", ex))
			}
		} else if len(includes) > 0 {
			hasDirRule := false
			for _, p := range includes {
				p = strings.TrimSpace(p)
				if strings.HasSuffix(p, "/") || strings.HasPrefix(p, "/") {
					hasDirRule = true
					break
				}
			}

			if !hasDirRule {
				syncArgs = append(syncArgs, "--include=*/")
			}

			for _, p := range includes {
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

			for _, ex := range excludes {
				ex = strings.TrimSpace(ex)
				if ex == "" {
					continue
				}
				syncArgs = append(syncArgs, fmt.Sprintf("--exclude=%s", ex))
			}

			syncArgs = append(syncArgs, "--exclude=*")
		}
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

// writeFastDlOriginalExcludes writes an rsync exclude file with the original
// install files that are still untouched. Returns "" when there is nothing to exclude.
func (s *Server) writeFastDlOriginalExcludes(localRoot string) (string, int, error) {
	manifest := s.loadFastDlManifest()
	if len(manifest) == 0 {
		return "", 0, nil
	}

	var b strings.Builder
	count := 0
	for rel := range manifest {
		info, err := os.Lstat(filepath.Join(localRoot, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() || !manifest.isOriginal(rel, info) {
			continue
		}
		pattern, ok := rsyncLiteralPattern(rel)
		if !ok {
			continue
		}
		b.WriteString(pattern)
		b.WriteByte('\n')
		count++
	}
	if count == 0 {
		return "", 0, nil
	}

	f, err := os.CreateTemp("", "wings-fastdl-exclude-*")
	if err != nil {
		return "", 0, errors.WrapIf(err, "fastdl: could not create exclude file")
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", 0, errors.WrapIf(err, "fastdl: could not write exclude file")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", 0, errors.WrapIf(err, "fastdl: could not write exclude file")
	}
	return f.Name(), count, nil
}
