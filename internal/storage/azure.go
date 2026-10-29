package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

type AzureDriver struct {
	client    *azblob.Client
	container string
}

func NewAzureDriver(cfg map[string]any) (*AzureDriver, error) {
	name, err := requireString(cfg, "name")
	if err != nil {
		return nil, err
	}
	key, err := requireString(cfg, "key")
	if err != nil {
		return nil, err
	}
	container, err := requireString(cfg, "container")
	if err != nil {
		return nil, err
	}

	cred, err := azblob.NewSharedKeyCredential(name, key)
	if err != nil {
		return nil, fmt.Errorf("azure credential failed: %w", err)
	}

	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", name)
	client, err := azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure client failed: %w", err)
	}

	return &AzureDriver{client: client, container: container}, nil
}

func (d *AzureDriver) Put(ctx context.Context, path string, reader io.Reader, _ int64) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("azure read input failed: %w", err)
	}

	_, err = d.client.UploadBuffer(ctx, d.container, path, data, nil)
	if err != nil {
		return fmt.Errorf("azure upload failed: %w", err)
	}
	return nil
}

func (d *AzureDriver) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	resp, err := d.client.DownloadStream(ctx, d.container, path, nil)
	if err != nil {
		return nil, fmt.Errorf("azure download failed: %w", err)
	}
	return resp.Body, nil
}

func (d *AzureDriver) Delete(ctx context.Context, path string) error {
	_, err := d.client.DeleteBlob(ctx, d.container, path, nil)
	if err != nil {
		return fmt.Errorf("azure delete failed: %w", err)
	}
	return nil
}

func (d *AzureDriver) Exists(ctx context.Context, path string) (bool, error) {
	_, err := d.client.DownloadStream(ctx, d.container, path, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("azure check failed: %w", err)
	}
	return true, nil
}

// azureReadCloser wraps a bytes buffer as io.ReadCloser.
type azureReadCloser struct {
	*bytes.Reader
}

func (azureReadCloser) Close() error { return nil }
