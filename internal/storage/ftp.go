package storage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"path"
	"time"

	"github.com/jlaffaye/ftp"
)

type FTPDriver struct {
	host     string
	username string
	password string
	port     int
	root     string
	ssl      bool
}

func NewFTPDriver(cfg map[string]any) (*FTPDriver, error) {
	host, err := requireString(cfg, "host")
	if err != nil {
		return nil, err
	}
	username, err := requireString(cfg, "username")
	if err != nil {
		return nil, err
	}
	password, err := requireString(cfg, "password")
	if err != nil {
		return nil, err
	}

	return &FTPDriver{
		host:     host,
		username: username,
		password: password,
		port:     configInt(cfg, "port", 21),
		root:     configString(cfg, "root", ""),
		ssl:      configBool(cfg, "ssl", false),
	}, nil
}

func (d *FTPDriver) connect() (*ftp.ServerConn, error) {
	addr := fmt.Sprintf("%s:%d", d.host, d.port)

	opts := []ftp.DialOption{
		ftp.DialWithTimeout(30 * time.Second),
	}
	if d.ssl {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{MinVersion: tls.VersionTLS12}))
	}

	conn, err := ftp.Dial(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("ftp dial failed: %w", err)
	}

	if err := conn.Login(d.username, d.password); err != nil {
		conn.Quit()
		return nil, fmt.Errorf("ftp login failed: %w", err)
	}

	return conn, nil
}

func (d *FTPDriver) fullPath(p string) string {
	if d.root != "" {
		return path.Join(d.root, p)
	}
	return p
}

func (d *FTPDriver) Put(_ context.Context, filePath string, reader io.Reader, _ int64) error {
	conn, err := d.connect()
	if err != nil {
		return err
	}
	defer conn.Quit()

	full := d.fullPath(filePath)

	// Create parent directory
	dir := path.Dir(full)
	if dir != "." && dir != "/" {
		conn.MakeDir(dir) // best-effort, may already exist
	}

	if err := conn.Stor(full, reader); err != nil {
		return fmt.Errorf("ftp store failed: %w", err)
	}
	return nil
}

func (d *FTPDriver) Get(_ context.Context, filePath string) (io.ReadCloser, error) {
	conn, err := d.connect()
	if err != nil {
		return nil, err
	}

	resp, err := conn.Retr(d.fullPath(filePath))
	if err != nil {
		conn.Quit()
		return nil, fmt.Errorf("ftp retrieve failed: %w", err)
	}

	// Read fully so we can close the FTP connection
	data, err := io.ReadAll(resp)
	resp.Close()
	conn.Quit()
	if err != nil {
		return nil, fmt.Errorf("ftp read failed: %w", err)
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (d *FTPDriver) Delete(_ context.Context, filePath string) error {
	conn, err := d.connect()
	if err != nil {
		return err
	}
	defer conn.Quit()

	if err := conn.Delete(d.fullPath(filePath)); err != nil {
		return fmt.Errorf("ftp delete failed: %w", err)
	}
	return nil
}

func (d *FTPDriver) Exists(_ context.Context, filePath string) (bool, error) {
	conn, err := d.connect()
	if err != nil {
		return false, err
	}
	defer conn.Quit()

	full := d.fullPath(filePath)
	_, err = conn.FileSize(full)
	if err == nil {
		return true, nil
	}
	// 550 also means "access denied" or "size unavailable", so absence is
	// confirmed by listing the directory; anything else is not an answer.
	var te *textproto.Error
	if !errors.As(err, &te) || te.Code != ftp.StatusFileUnavailable {
		return false, err
	}
	names, err := conn.NameList(path.Dir(full))
	if err != nil {
		return false, fmt.Errorf("check %s: %w", filePath, err)
	}
	for _, n := range names {
		if path.Base(n) == path.Base(full) {
			return true, nil
		}
	}
	return false, nil
}
