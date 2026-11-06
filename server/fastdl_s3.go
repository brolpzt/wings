package server

import (
	"context"
	stderrors "errors"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"emperror.dev/errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gabriel-vasile/mimetype"
)

func (s *Server) syncFastDlS3(ctx context.Context, req FastDlSyncRequest, progress *fastDlProgress) error {
	if req.Bucket == "" || req.Endpoint == "" || req.AccessKey == "" || req.SecretKey == "" {
		return errors.New("fastdl: incomplete S3 configuration")
	}

	region := req.Region
	if region == "" {
		region = "auto"
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(req.AccessKey, req.SecretKey, "")),
	)
	if err != nil {
		return errors.WrapIf(err, "fastdl: failed to load S3 config")
	}

	usePathStyle := req.UsePathStyleEndpoint
	if !usePathStyle && strings.Contains(strings.ToLower(req.Endpoint), "r2.cloudflarestorage.com") {
		usePathStyle = true
		s.Log().Debug("fastdl: auto-enabled path-style endpoint for Cloudflare R2")
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(strings.TrimRight(req.Endpoint, "/"))
		o.UsePathStyle = usePathStyle
	})

	shortID := s.ID()[:8]
	prefix := strings.Trim(req.RemotePath, "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += shortID + "/"

	localRoot := s.Filesystem().Path()
	matcher := newFastDlPatternMatcher(req.SyncPatterns)
	manifest := s.loadFastDlManifest()
	skippedOriginal := 0

	// Phase 1: collect the files that match the sync patterns.
	var files []fastDlLocalFile
	var totalBytes int64
	err = filepath.Walk(localRoot, func(filePath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			s.Log().WithField("path", filePath).WithField("error", walkErr).Warn("fastdl: skipping unreadable path")
			return nil
		}

		if info.IsDir() {
			return nil
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Stat(filePath)
			if err != nil || !target.Mode().IsRegular() {
				return nil
			}
			info = target
		} else if !info.Mode().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(localRoot, filePath)
		if err != nil {
			return err
		}

		rel = filepath.ToSlash(rel)
		if !matcher.shouldSync(rel) {
			return nil
		}

		// Stock game files the install delivered: players already have them.
		if manifest.isOriginal(rel, info) && !matcher.isForced(rel) {
			skippedOriginal++
			return nil
		}

		files = append(files, fastDlLocalFile{path: filePath, key: prefix + rel, size: info.Size(), modTime: info.ModTime()})
		totalBytes += info.Size()
		return nil
	})
	if err != nil {
		return err
	}

	progress.update(func(st *FastDlSyncStatus) {
		st.MatchedFiles, st.SkippedOriginal, st.Phase = int64(len(files)), int64(skippedOriginal), "comparing"
	})

	// Nothing matched the patterns at all: likely a pattern mistake, so never wipe the remote copy.
	// (When everything matched was an original file, the cleanup below still removes stale objects.)
	if len(files) == 0 && skippedOriginal == 0 {
		s.Log().Warn("fastdl: no local files matched sync patterns; nothing uploaded and no remote files deleted")
		return nil
	}

	// Phase 2: index what already exists in the bucket so unchanged files are skipped.
	remote, err := s.listS3Objects(ctx, client, req.Bucket, prefix)
	if err != nil {
		return err
	}

	localKeys := make(map[string]struct{}, len(files))
	var pending []fastDlLocalFile
	var pendingBytes int64
	for _, f := range files {
		localKeys[f.key] = struct{}{}
		if r, ok := remote[f.key]; ok && r.size == f.size && !r.modTime.Before(f.modTime) {
			continue
		}
		pending = append(pending, f)
		pendingBytes += f.size
	}

	s.Log().WithField("matched_files", len(files)).WithField("matched_bytes", totalBytes).
		WithField("skipped_original_files", skippedOriginal).
		WithField("to_upload_files", len(pending)).WithField("to_upload_bytes", pendingBytes).
		Info("fastdl: S3 sync plan ready")
	progress.update(func(st *FastDlSyncStatus) {
		st.FilesTotal, st.BytesTotal, st.Phase = int64(len(pending)), pendingBytes, "uploading"
	})

	// Phase 3: upload with a small worker pool and periodic progress logs.
	var done, failed, uploadedBytes atomic.Int64
	jobs := make(chan fastDlLocalFile)
	var wg sync.WaitGroup
	for i := 0; i < fastDlS3Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if err := s.uploadFastDlFile(ctx, client, req.Bucket, f); err != nil {
					failed.Add(1)
					s.Log().WithField("key", f.key).WithField("error", err).Error("fastdl: failed to upload file")
					progress.update(func(st *FastDlSyncStatus) { st.FilesDone++; st.FilesFailed++ })
				} else {
					uploadedBytes.Add(f.size)
					progress.update(func(st *FastDlSyncStatus) { st.FilesDone++; st.BytesDone += f.size })
				}
				done.Add(1)
			}
		}()
	}

	stopProgress := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.Log().WithField("done", done.Load()).WithField("total", len(pending)).
					WithField("failed", failed.Load()).WithField("bytes", uploadedBytes.Load()).
					Info("fastdl: S3 upload progress")
			case <-stopProgress:
				return
			}
		}
	}()

feed:
	for _, f := range pending {
		select {
		case jobs <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(stopProgress)

	if err := ctx.Err(); err != nil {
		return errors.WrapIf(err, "fastdl: synchronization interrupted")
	}

	// Never delete remote files when some uploads failed; the next run retries them.
	if n := failed.Load(); n > 0 {
		return errors.Errorf("fastdl: %d of %d uploads failed; stale object cleanup skipped", n, len(pending))
	}

	progress.update(func(st *FastDlSyncStatus) { st.Phase = "cleaning" })

	var toDelete []types.ObjectIdentifier
	for key := range remote {
		if _, ok := localKeys[key]; ok {
			continue
		}
		toDelete = append(toDelete, types.ObjectIdentifier{Key: aws.String(key)})
	}

	for len(toDelete) > 0 {
		batch := toDelete
		if len(batch) > 1000 {
			batch = batch[:1000]
			toDelete = toDelete[1000:]
		} else {
			toDelete = nil
		}

		_, err = client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(req.Bucket),
			Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return errors.WrapIf(err, "fastdl: failed to delete stale objects")
		}
		n := int64(len(batch))
		progress.update(func(st *FastDlSyncStatus) { st.Deleted += n })
	}

	return nil
}

const fastDlS3Workers = 8

type fastDlLocalFile struct {
	path    string
	key     string
	size    int64
	modTime time.Time
}

type fastDlRemoteObject struct {
	size    int64
	modTime time.Time
}

func (s *Server) uploadFastDlFile(ctx context.Context, client *s3.Client, bucket string, f fastDlLocalFile) error {
	file, err := os.Open(f.path)
	if err != nil {
		return errors.WrapIf(err, "fastdl: failed to open local file")
	}
	defer file.Close()

	contentType := mime.TypeByExtension(filepath.Ext(f.path))
	if contentType == "" {
		if mt, err := mimetype.DetectFile(f.path); err == nil {
			contentType = mt.String()
		} else {
			contentType = "application/octet-stream"
		}
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(f.key),
		Body:        file,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return errors.WrapIf(err, "fastdl: failed to upload object")
	}

	return nil
}

func (s *Server) listS3Objects(ctx context.Context, client *s3.Client, bucket, prefix string) (map[string]fastDlRemoteObject, error) {
	keys := make(map[string]fastDlRemoteObject)
	var token *string

	for {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			if isS3ListNotFound(err) {
				s.Log().WithField("prefix", prefix).Debug("fastdl: remote prefix empty or not found, skipping stale object cleanup")
				return keys, nil
			}
			return nil, errors.WrapIf(err, "fastdl: failed to list remote objects")
		}

		for _, obj := range out.Contents {
			if obj.Key != nil {
				ro := fastDlRemoteObject{size: aws.ToInt64(obj.Size)}
				if obj.LastModified != nil {
					ro.modTime = *obj.LastModified
				}
				keys[*obj.Key] = ro
			}
		}

		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}

	return keys, nil
}

// isS3ListNotFound reports whether a ListObjects error indicates an empty or missing prefix.
// Cloudflare R2 may return NoSuchKey (404) when listing a prefix that does not exist yet.
func isS3ListNotFound(err error) bool {
	var apiErr smithy.APIError
	if stderrors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
	}

	var respErr *smithyhttp.ResponseError
	if stderrors.As(err, &respErr) {
		return respErr.HTTPStatusCode() == 404
	}

	return false
}
