package server

import (
	"context"
	stderrors "errors"
	"mime"
	"os"
	"path/filepath"
	"strings"

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

func (s *Server) syncFastDlS3(ctx context.Context, req FastDlSyncRequest) error {
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
	localFiles := make(map[string]struct{})

	err = filepath.Walk(localRoot, func(filePath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if info.IsDir() {
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

		key := prefix + rel
		localFiles[key] = struct{}{}

		f, err := os.Open(filePath)
		if err != nil {
			return errors.WrapIf(err, "fastdl: failed to open local file")
		}
		defer f.Close()

		contentType := mime.TypeByExtension(filepath.Ext(filePath))
		if contentType == "" {
			if mt, err := mimetype.DetectFile(filePath); err == nil {
				contentType = mt.String()
			} else {
				contentType = "application/octet-stream"
			}
		}

		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(req.Bucket),
			Key:         aws.String(key),
			Body:        f,
			ContentType: aws.String(contentType),
		})
		if err != nil {
			return errors.WrapIf(err, "fastdl: failed to upload object")
		}

		return nil
	})
	if err != nil {
		return err
	}

	remoteKeys, err := s.listS3Keys(ctx, client, req.Bucket, prefix)
	if err != nil {
		return err
	}

	if len(localFiles) == 0 {
		s.Log().Warn("fastdl: no local files matched sync patterns; nothing uploaded")
	}

	var toDelete []types.ObjectIdentifier
	for _, key := range remoteKeys {
		if _, ok := localFiles[key]; ok {
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
	}

	return nil
}

func (s *Server) listS3Keys(ctx context.Context, client *s3.Client, bucket, prefix string) ([]string, error) {
	var keys []string
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
				keys = append(keys, *obj.Key)
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
