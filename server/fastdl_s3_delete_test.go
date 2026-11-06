package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/server/filesystem"
)

// Needs an S3 endpoint: FASTDL_S3_TEST_ENDPOINT=http://host:8333 (bucket "fastdl-test", any keys).
func TestFastDlS3RemovesDeletedCustomFiles(t *testing.T) {
	endpoint := os.Getenv("FASTDL_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("FASTDL_S3_TEST_ENDPOINT not set")
	}
	root := t.TempDir()
	cfg := &config.Configuration{AuthenticationToken: "test-token"}
	cfg.System.RootDirectory = filepath.Join(root, "wings")
	config.Set(cfg)

	data := filepath.Join(root, "data")
	_ = os.MkdirAll(filepath.Join(data, "main"), 0o755)
	_ = os.WriteFile(filepath.Join(data, "main", "stock.bin"), []byte("stock"), 0o644)
	_ = os.WriteFile(filepath.Join(data, "main", "custom.iwd"), []byte("custom"), 0o644)

	s, err := New(&fastDlWatchTestClient{})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Uuid = "abcdef12-0000-0000-0000-000000000000"
	if s.fs, err = filesystem.New(data, 0, nil); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(data, "main", "stock.bin"))
	_ = s.saveFastDlManifest(fastDlManifest{"main/stock.bin": fastDlEntry(info)})

	req := FastDlSyncRequest{StorageType: "s3", RemotePath: "/", SyncPatterns: "*.iwd", Bucket: "fastdl-test",
		Endpoint: endpoint, Region: "us-east-1", AccessKey: "k", SecretKey: "s", UsePathStyleEndpoint: true}
	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("k", "s", "")))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	_, _ = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("fastdl-test")})

	keys := func() map[string]bool {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("fastdl-test"), Prefix: aws.String("abcdef12/")})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, o := range out.Contents {
			m[*o.Key] = true
		}
		return m
	}

	if err := s.SyncFastDL(ctx, req); err != nil {
		t.Fatal(err)
	}
	if k := keys(); !k["abcdef12/main/custom.iwd"] || len(k) != 1 {
		t.Fatalf("after first sync: %v", k)
	}

	// The user deletes their only custom file: the bucket copy must go too.
	_ = os.Remove(filepath.Join(data, "main", "custom.iwd"))
	if err := s.SyncFastDL(ctx, req); err != nil {
		t.Fatal(err)
	}
	if k := keys(); len(k) != 0 {
		t.Fatalf("deleted file still in bucket: %v", k)
	}
}
