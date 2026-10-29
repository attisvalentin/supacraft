package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Driver struct {
	client *s3.Client
	bucket string
}

func NewS3Driver(cfg map[string]any) (*S3Driver, error) {
	key, err := requireString(cfg, "key")
	if err != nil {
		return nil, err
	}
	secret, err := requireString(cfg, "secret")
	if err != nil {
		return nil, err
	}
	region, err := requireString(cfg, "region")
	if err != nil {
		return nil, err
	}
	bucket, err := requireString(cfg, "bucket")
	if err != nil {
		return nil, err
	}

	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.Region = region
			o.Credentials = credentials.NewStaticCredentialsProvider(key, secret, "")
		},
	}

	endpoint := configString(cfg, "endpoint", "")
	if endpoint != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.New(s3.Options{}, opts...)

	return &S3Driver{client: client, bucket: bucket}, nil
}

func (d *S3Driver) Put(ctx context.Context, path string, reader io.Reader, size int64) error {
	input := &s3.PutObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
		Body:   reader,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}

	if _, err := d.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("s3 put failed: %w", err)
	}
	return nil
}

func (d *S3Driver) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	output, err := d.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get failed: %w", err)
	}
	return output.Body, nil
}

func (d *S3Driver) Delete(ctx context.Context, path string) error {
	if _, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	}); err != nil {
		return fmt.Errorf("s3 delete failed: %w", err)
	}
	return nil
}

func (d *S3Driver) Exists(ctx context.Context, path string) (bool, error) {
	_, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		// HeadObject also returns a generic smithy error for 404
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return false, nil
		}
		return false, fmt.Errorf("s3 head failed: %w", err)
	}
	return true, nil
}
