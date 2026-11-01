package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// FilesTable records every upload in the tenant's own database. Remote
// drivers store files at the bucket (or directory) root with nothing that
// marks them as the tenant's, so purging a tenant deletes exactly these.
// Project validation reserves the "_supacraft_" table prefix.
const FilesTable = "_supacraft_files"

// Fixed identifiers, quoted like query.QuoteIdent would (importing it here
// would be an import cycle).
const (
	filesTableSQL    = `"` + FilesTable + `"`
	createFilesTable = `CREATE TABLE IF NOT EXISTS ` + filesTableSQL + ` (
	"path" TEXT NOT NULL,
	"destination" TEXT NOT NULL,
	"created_at" TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY ("destination", "path")
)`
)

// Upload is one recorded file.
type Upload struct {
	Path        string
	Destination string
}

// Execer is the part of *sql.DB (and *tenant.DB) tracking needs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Destination identifies where a driver configuration stores files, so a
// purge after the storage settings changed knows which records it can
// delete. It is the driver name plus a hash of the location (bucket, host,
// root...): the location may come from a secret and is not stored.
func Destination(driver string, cfg map[string]any) string {
	var loc string
	switch driver {
	case "", "local":
		return "local"
	case "s3":
		loc = configString(cfg, "endpoint", "") + "|" + configString(cfg, "region", "") + "|" + configString(cfg, "bucket", "")
	case "gcs":
		loc = configString(cfg, "bucket", "")
	case "azure":
		loc = configString(cfg, "name", "") + "|" + configString(cfg, "container", "")
	case "ftp", "sftp":
		port := 21
		if driver == "sftp" {
			port = 22
		}
		loc = fmt.Sprintf("%s|%d|%s|%s", configString(cfg, "host", ""), configInt(cfg, "port", port), configString(cfg, "root", ""), configString(cfg, "username", ""))
	}
	sum := sha256.Sum256([]byte(driver + "|" + loc))
	return driver + ":" + hex.EncodeToString(sum[:8])
}

// DriverOf is the driver name of a destination.
func DriverOf(destination string) string {
	d, _, _ := strings.Cut(destination, ":")
	return d
}

// TrackUpload records path before the file is written, so a file can't
// exist in storage without a record.
func TrackUpload(ctx context.Context, db Execer, path, destination string) error {
	if _, err := db.ExecContext(ctx, createFilesTable); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO `+filesTableSQL+` ("path", "destination") VALUES (?, ?)`, path, destination)
	return err
}

// UntrackUpload forgets path after it is deleted (or failed to upload).
func UntrackUpload(ctx context.Context, db Execer, path, destination string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM `+filesTableSQL+` WHERE "path" = ? AND "destination" = ?`, path, destination)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return nil
	}
	return err
}

// TrackedUploads lists the recorded files (none if nothing was uploaded).
func TrackedUploads(ctx context.Context, db *sql.DB) ([]Upload, error) {
	rows, err := db.QueryContext(ctx, `SELECT "path", "destination" FROM `+filesTableSQL+` ORDER BY "destination", "path"`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var u Upload
		if err := rows.Scan(&u.Path, &u.Destination); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
