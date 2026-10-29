package storage

import (
	"context"
	"fmt"
	"io"

	gcStorage "cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

type GCSDriver struct {
	client *gcStorage.Client
	bucket string
}

func NewGCSDriver(cfg map[string]any) (*GCSDriver, error) {
	keyFile, err := requireString(cfg, "key_file")
	if err != nil {
		return nil, err
	}
	bucket, err := requireString(cfg, "bucket")
	if err != nil {
		return nil, err
	}

	client, err := gcStorage.NewClient(context.Background(), option.WithCredentialsJSON([]byte(keyFile)))
	if err != nil {
		return nil, fmt.Errorf("gcs client failed: %w", err)
	}

	return &GCSDriver{client: client, bucket: bucket}, nil
}

func (d *GCSDriver) Put(ctx context.Context, path string, reader io.Reader, _ int64) error {
	w := d.client.Bucket(d.bucket).Object(path).NewWriter(ctx)

	if _, err := io.Copy(w, reader); err != nil {
		w.Close()
		return fmt.Errorf("gcs write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("gcs close failed: %w", err)
	}
	return nil
}

func (d *GCSDriver) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	r, err := d.client.Bucket(d.bucket).Object(path).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs read failed: %w", err)
	}
	return r, nil
}

func (d *GCSDriver) Delete(ctx context.Context, path string) error {
	if err := d.client.Bucket(d.bucket).Object(path).Delete(ctx); err != nil {
		return fmt.Errorf("gcs delete failed: %w", err)
	}
	return nil
}

func (d *GCSDriver) Exists(ctx context.Context, path string) (bool, error) {
	_, err := d.client.Bucket(d.bucket).Object(path).Attrs(ctx)
	if err != nil {
		if err == gcStorage.ErrObjectNotExist {
			return false, nil
		}
		return false, fmt.Errorf("gcs attrs failed: %w", err)
	}
	return true, nil
}
