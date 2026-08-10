package targets

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config stores S3-compatible (MinIO / R2 / B2 / AWS) connection details. The
// endpoint may carry a scheme ("https://..." / "http://..."); without one the
// Secure flag decides TLS. Secrets live in the plain config JSON, matching the
// existing GitHub-token / OneDrive-refresh-token pattern.
type S3Config struct {
	Endpoint  string `json:"endpoint"` // e.g. https://minio.example.com or 1.2.3.4:9000
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`     // optional key prefix (e.g. "nodepanel")
	Region    string `json:"region"`     // optional; MinIO ignores it
	PathStyle bool   `json:"path_style"` // true for MinIO / most self-hosted stores
	Secure    bool   `json:"secure"`     // TLS when Endpoint has no scheme
	// InsecureSkipVerify accepts a self-signed/unverifiable TLS cert — for a
	// point-to-point link to a self-hosted MinIO whose cert isn't in the trust
	// store. Credentials are still S3-signed over the TLS channel.
	InsecureSkipVerify bool `json:"insecure_skip_verify"`
}

type S3 struct{ cfg S3Config }

func (s *S3) client() (*minio.Client, error) {
	if s.cfg.Endpoint == "" || s.cfg.Bucket == "" {
		return nil, errors.New("s3: endpoint and bucket are required")
	}
	endpoint := s.cfg.Endpoint
	secure := s.cfg.Secure
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "https://"), true
	case strings.HasPrefix(endpoint, "http://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "http://"), false
	}
	opts := &minio.Options{
		Creds:        credentials.NewStatic(s.cfg.AccessKey, s.cfg.SecretKey, "", credentials.SignatureDefault),
		Secure:       secure,
		Region:       s.cfg.Region,
		BucketLookup: bucketLookup(s.cfg.PathStyle),
	}
	if s.cfg.InsecureSkipVerify {
		// Accept a self-signed / unverifiable cert for a point-to-point link to a
		// self-hosted MinIO whose cert isn't trusted. Credentials stay S3-signed.
		//
		// A BARE http.Transport has no dial/response timeouts, so a black-holed
		// MinIO hangs every request ~75s on TCP SYN retries. Worse, every S3 push
		// to the same host shares a cap-1 single-flight slot (reliable.go), so one
		// hung dial serializes every container's S3 backup behind it for minutes.
		// Bounded timeouts make a dead/slow MinIO fail fast so pushArchiveReliable's
		// 5× retry (and the next scheduled run) actually run within a useful window.
		// (When InsecureSkipVerify is false we leave minio-go's default transport —
		// which already sets DialContext/TLS timeouts — in place.)
		opts.Transport = &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          100,
		}
	}
	cli, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	return cli, nil
}

func bucketLookup(pathStyle bool) minio.BucketLookupType {
	if pathStyle {
		return minio.BucketLookupPath
	}
	return minio.BucketLookupAuto
}

// object joins the optional prefix with remoteName into a bucket key. path.Join
// always uses "/" and cleans the result; remoteName is "nodeID/backupID.tar.gz".
func (s *S3) object(remoteName string) string {
	return S3ObjectName(s.cfg, remoteName)
}

// S3ObjectName joins cfg.Prefix and remoteName exactly as the S3 target does.
// Backup scheduling uses this to tell a 2.2+ agent which object to write when
// it streams directly to MinIO/S3 and bypasses master staging.
func S3ObjectName(cfg S3Config, remoteName string) string {
	if cfg.Prefix == "" {
		return remoteName
	}
	return path.Join(cfg.Prefix, remoteName)
}

// Push uploads a local archive to the bucket as one object, streaming the file
// in PartSize pieces (multipart for large objects) so memory stays flat. Each
// part is well under Cloudflare's 100 MB body cap when the endpoint is fronted
// by a tunnel.
func (s *S3) Push(ctx context.Context, localPath, remoteName string) error {
	cli, err := s.client()
	if err != nil {
		return err
	}
	if err := ensureBucket(ctx, cli, s.cfg.Bucket); err != nil {
		return err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = cli.PutObject(ctx, s.cfg.Bucket, s.object(remoteName), f, fi.Size(), minio.PutObjectOptions{
		PartSize:    16 << 20, // 16 MB parts
		ContentType: "application/gzip",
	})
	return err
}

// ensureBucket creates the bucket if it doesn't already exist, so a freshly
// deployed MinIO needs no manual mc/console bucket step — the first Test or
// Push provisions it. Idempotent.
func ensureBucket(ctx context.Context, cli *minio.Client, bucket string) error {
	exists, err := cli.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: ""})
}

// Pull streams an object to a local file (used by restore's ensureStaged).
func (s *S3) Pull(ctx context.Context, remoteName, localPath string) error {
	cli, err := s.client()
	if err != nil {
		return err
	}
	obj, err := cli.GetObject(ctx, s.cfg.Bucket, s.object(remoteName), minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path.Dir(localPath), 0o755); err != nil {
		obj.Close()
		return err
	}
	out, err := os.Create(localPath)
	if err != nil {
		obj.Close()
		return err
	}
	_, copyErr := io.Copy(out, obj)
	closeErr := out.Close()
	objCloseErr := obj.Close() // surfaces a 404 / read error on a missing object
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return objCloseErr
}

// List returns object keys under prefix.
func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	cli, err := s.client()
	if err != nil {
		return nil, err
	}
	var out []string
	for obj := range cli.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{Prefix: s.object(prefix), Recursive: true}) {
		if obj.Err != nil {
			return out, obj.Err
		}
		out = append(out, obj.Key)
	}
	return out, nil
}

// Delete removes one object (used by retention).
func (s *S3) Delete(ctx context.Context, remoteName string) error {
	cli, err := s.client()
	if err != nil {
		return err
	}
	return cli.RemoveObject(ctx, s.cfg.Bucket, s.object(remoteName), minio.RemoveObjectOptions{})
}

// Test verifies the endpoint and credentials, and creates the bucket if it
// doesn't exist (so configuring a fresh MinIO is one click — no manual bucket
// step).
func (s *S3) Test(ctx context.Context) error {
	cli, err := s.client()
	if err != nil {
		return err
	}
	return ensureBucket(ctx, cli, s.cfg.Bucket)
}
