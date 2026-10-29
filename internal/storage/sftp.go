package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPDriver struct {
	host     string
	username string
	password string
	port     int
	root     string
}

func NewSFTPDriver(cfg map[string]any) (*SFTPDriver, error) {
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

	return &SFTPDriver{
		host:     host,
		username: username,
		password: password,
		port:     configInt(cfg, "port", 22),
		root:     configString(cfg, "root", ""),
	}, nil
}

func (d *SFTPDriver) connect() (*sftp.Client, *ssh.Client, error) {
	addr := fmt.Sprintf("%s:%d", d.host, d.port)

	sshConfig := &ssh.ClientConfig{
		User: d.username,
		Auth: []ssh.AuthMethod{
			ssh.Password(d.password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	sshConn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial failed: %w", err)
	}

	client, err := sftp.NewClient(sshConn)
	if err != nil {
		sshConn.Close()
		return nil, nil, fmt.Errorf("sftp client failed: %w", err)
	}

	return client, sshConn, nil
}

func (d *SFTPDriver) fullPath(p string) string {
	if d.root != "" {
		return path.Join(d.root, p)
	}
	return p
}

func (d *SFTPDriver) Put(_ context.Context, filePath string, reader io.Reader, _ int64) error {
	client, sshConn, err := d.connect()
	if err != nil {
		return err
	}
	defer client.Close()
	defer sshConn.Close()

	full := d.fullPath(filePath)

	dir := path.Dir(full)
	if dir != "." && dir != "/" {
		client.MkdirAll(dir) // best-effort
	}

	f, err := client.Create(full)
	if err != nil {
		return fmt.Errorf("sftp create failed: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, reader); err != nil {
		return fmt.Errorf("sftp write failed: %w", err)
	}
	return nil
}

func (d *SFTPDriver) Get(_ context.Context, filePath string) (io.ReadCloser, error) {
	client, sshConn, err := d.connect()
	if err != nil {
		return nil, err
	}

	f, err := client.Open(d.fullPath(filePath))
	if err != nil {
		client.Close()
		sshConn.Close()
		return nil, fmt.Errorf("sftp open failed: %w", err)
	}

	// Read fully so we can close the SFTP/SSH connections
	data, err := io.ReadAll(f)
	f.Close()
	client.Close()
	sshConn.Close()
	if err != nil {
		return nil, fmt.Errorf("sftp read failed: %w", err)
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (d *SFTPDriver) Delete(_ context.Context, filePath string) error {
	client, sshConn, err := d.connect()
	if err != nil {
		return err
	}
	defer client.Close()
	defer sshConn.Close()

	if err := client.Remove(d.fullPath(filePath)); err != nil {
		return fmt.Errorf("sftp delete failed: %w", err)
	}
	return nil
}

func (d *SFTPDriver) Exists(_ context.Context, filePath string) (bool, error) {
	client, sshConn, err := d.connect()
	if err != nil {
		return false, err
	}
	defer client.Close()
	defer sshConn.Close()

	_, err = client.Stat(d.fullPath(filePath))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, nil
	}
	return true, nil
}
