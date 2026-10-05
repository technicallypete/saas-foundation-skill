package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3Config configures the S3-compatible adapter. The credential is the
// scoped, non-root service credential (object operations on one bucket).
type S3Config struct {
	Endpoint  string // e.g. http://minio:9000
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// S3Store is an ObjectStore on any S3-compatible service.
type S3Store struct {
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
}

var _ ObjectStore = (*S3Store)(nil)

// NewS3Store builds the adapter. Presigning is computed offline; no network
// call is made until an object operation runs.
func NewS3Store(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" || cfg.Region == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("storage: incomplete S3 configuration")
	}
	client := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		// Compatible services do not all implement the newest default
		// integrity checksums; send them only when an operation requires it.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3Store{bucket: cfg.Bucket, client: client, presign: s3.NewPresignClient(client)}, nil
}

// Client exposes the underlying SDK client (tests use it to prove what the
// scoped credential cannot do).
func (s *S3Store) Client() *s3.Client { return s.client }

// Bucket returns the configured bucket name.
func (s *S3Store) Bucket() string { return s.bucket }

func (s *S3Store) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ContentType:   aws.String(contentType),
	})
	return err
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, mapMissing(err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

func (s *S3Store) Metadata(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectInfo{}, mapMissing(err)
	}
	return ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType), ETag: aws.ToString(out.ETag)}, nil
}

// SignedGetURL presigns a GET for exactly this key. The signature covers the
// path, so the URL cannot be reused for a sibling key.
func (s *S3Store) SignedGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if ttl <= 0 || ttl > MaxSignedURLTTL {
		return "", fmt.Errorf("%w: %s", ErrInvalidTTL, ttl)
	}
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)},
		s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func mapMissing(err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return fmt.Errorf("%w: %v", ErrObjectMissing, err)
		}
	}
	return err
}
