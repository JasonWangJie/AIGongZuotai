package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type config struct {
	Endpoint string `json:"TEST_S3_ENDPOINT"`
	Region   string `json:"TEST_S3_REGION"`
	Bucket   string `json:"TEST_S3_BUCKET"`
	Access   string `json:"TEST_S3_ACCESS_KEY_ID"`
	Secret   string `json:"TEST_S3_SECRET_ACCESS_KEY"`
}
type version struct {
	Key  string `json:"key"`
	ID   string `json:"version_id"`
	Hash string `json:"sha256"`
	Size int64  `json:"size"`
}

func run() error {
	if len(os.Args) != 4 {
		return errors.New("usage: s3probe init|ready|seed|verify config.json versions.json")
	}
	b, e := os.ReadFile(os.Args[2])
	if e != nil {
		return e
	}
	var c config
	if e = json.Unmarshal(b, &c); e != nil {
		return errors.New("invalid local S3 config")
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.Access == "" || c.Secret == "" || c.Bucket == "" {
		return errors.New("invalid loopback S3 configuration")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}
	client := s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.Access, c.Secret, ""), RetryMaxAttempts: 1, HTTPClient: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, func(o *s3.Options) { o.UsePathStyle = true; o.BaseEndpoint = &c.Endpoint })
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	switch os.Args[1] {
	case "init", "ready":
		for {
			_, e = client.ListBuckets(ctx, &s3.ListBucketsInput{})
			if e == nil {
				break
			}
			select {
			case <-ctx.Done():
				return errors.New("local S3 service did not become ready")
			case <-time.After(250 * time.Millisecond):
			}
		}
		if os.Args[1] == "init" {
			if _, e = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &c.Bucket}); e != nil {
				return fmt.Errorf("create new drill bucket: %w", e)
			}
			if _, e = client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: &c.Bucket, VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); e != nil {
				return fmt.Errorf("enable new bucket versioning: %w", e)
			}
		}
		status, e := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &c.Bucket})
		if e != nil {
			return fmt.Errorf("read bucket versioning: %w", e)
		}
		if status.Status != types.BucketVersioningStatusEnabled {
			return errors.New("original versioned bucket is unavailable")
		}
	case "seed":
		// Exclusive creation records an attempted seed before any network write.
		receipt, e := os.OpenFile(os.Args[3], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		defer receipt.Close()
		var token [16]byte
		if _, e = rand.Read(token[:]); e != nil {
			return e
		}
		key := "cold-backup-drill/" + hex.EncodeToString(token[:])
		items := []version{}
		for _, size := range []int{1027, 5141} {
			payload := make([]byte, size)
			if _, e = rand.Read(payload); e != nil {
				return e
			}
			out, e := client.PutObject(ctx, &s3.PutObjectInput{Bucket: &c.Bucket, Key: &key, Body: bytes.NewReader(payload)})
			if e != nil {
				return fmt.Errorf("seed local object: %w", e)
			}
			v := version{Key: key, ID: aws.ToString(out.VersionId), Hash: fmt.Sprintf("%x", sha256.Sum256(payload)), Size: int64(len(payload))}
			if v.ID == "" || v.ID == "null" || len(items) > 0 && v.ID == items[0].ID {
				return errors.New("server did not return distinct immutable version IDs")
			}
			items = append(items, v)
		}
		if e = verify(ctx, client, c.Bucket, items); e != nil {
			return e
		}
		if e = json.NewEncoder(receipt).Encode(items); e != nil {
			return e
		}
		return receipt.Sync()
	case "verify":
		f, e := os.Open(os.Args[3])
		if e != nil {
			return e
		}
		defer f.Close()
		var items []version
		dec := json.NewDecoder(io.LimitReader(f, (4<<20)+1))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&items); e != nil {
			return errors.New("invalid original version manifest")
		}
		if dec.Decode(new(any)) != io.EOF {
			return errors.New("invalid trailing manifest data")
		}
		return verify(ctx, client, c.Bucket, items)
	default:
		return errors.New("unknown local probe action")
	}
	return nil
}
func verify(ctx context.Context, client *s3.Client, bucket string, items []version) error {
	if len(items) < 1 || len(items) > 10000 {
		return errors.New("version manifest count out of bounds")
	}
	for i, v := range items {
		if len(v.Key) == 0 || len(v.Key) > 1024 || v.ID == "" || v.ID == "null" || len(v.ID) > 1024 || len(v.Hash) != 64 || v.Size < 0 || v.Size > 1<<30 {
			return fmt.Errorf("invalid manifest entry %d", i)
		}
		out, e := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &v.Key, VersionId: &v.ID})
		if e != nil {
			return fmt.Errorf("original object version %d unavailable: %w", i, e)
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(out.Body, v.Size+1))
		closeErr := out.Body.Close()
		if e != nil || closeErr != nil || n != v.Size || aws.ToString(out.VersionId) != v.ID || hex.EncodeToString(h.Sum(nil)) != v.Hash {
			return fmt.Errorf("original object version %d content or identity mismatch", i)
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
