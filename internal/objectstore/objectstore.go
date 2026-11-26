// Package objectstore reads and writes files in S3-compatible storage:
// SeaweedFS locally, or Google Cloud Storage through its S3 API.
package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"obsfabric/internal/config"
)

type Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	PathStyle bool
}

// ConfigFromEnv reads the S3_* settings (LLD-001 §7.1).
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Region:    config.String("S3_REGION", "us-east-1"),
		PathStyle: config.String("S3_USE_PATH_STYLE", "true") == "true",
	}
	for key, dst := range map[string]*string{
		"S3_ENDPOINT":          &cfg.Endpoint,
		"S3_BUCKET":            &cfg.Bucket,
		"S3_ACCESS_KEY_ID":     &cfg.AccessKey,
		"S3_SECRET_ACCESS_KEY": &cfg.SecretKey,
	} {
		v, err := config.Required(key)
		if err != nil {
			return cfg, err
		}
		*dst = v
	}
	return cfg, nil
}

type Store struct {
	client *s3.Client
	bucket string
}

func New(cfg Config) *Store {
	client := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: cfg.PathStyle,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		// Only send checksums when the API requires them; S3-compatible
		// stores do not all support the newer checksum headers.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client, bucket: cfg.Bucket}
}

// URI returns the s3:// address of key.
func (s *Store) URI(key string) string {
	return "s3://" + s.bucket + "/" + key
}

// PutFile uploads the local file at path to key.
func (s *Store) PutFile(ctx context.Context, key, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	return err
}

// Ping fails if the bucket cannot be reached.
func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}

func (s *Store) Put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	return err
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// Size returns the size of the object at key in bytes.
func (s *Store) Size(ctx context.Context, key string) (int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return 0, err
	}
	return aws.ToInt64(out.ContentLength), nil
}

// DeletePrefix deletes every object whose key starts with prefix. Deleting
// an empty prefix is not an error.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	if prefix == "" {
		return errors.New("refusing to delete the whole bucket")
	}
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			if err := s.Delete(ctx, aws.ToString(obj.Key)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Delete deletes one object. A missing object is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	var nf *types.NoSuchKey
	if errors.As(err, &nf) {
		return nil
	}
	return err
}

// Key returns the object key of an s3:// URI in this bucket.
func (s *Store) Key(uri string) (string, error) {
	prefix := "s3://" + s.bucket + "/"
	if !strings.HasPrefix(uri, prefix) {
		return "", errors.New("uri " + uri + " is not in bucket " + s.bucket)
	}
	return strings.TrimPrefix(uri, prefix), nil
}
