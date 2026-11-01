package storage

import (
	"context"
	"database/sql"
	"strings"
)

// FilesTable records every upload in the tenant's own database. Remote
// drivers store files at the bucket (or directory) root with nothing that
// marks them as the tenant's, so purging a tenant deletes exactly these.
// Project validation reserves the "_supacraft_" table prefix.
const FilesTable = "_supacraft_files"

const createFilesTable = `CREATE TABLE IF NOT EXISTS ` + FilesTable + ` (
	path TEXT PRIMARY KEY,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

// Execer is the part of *sql.DB (and *tenant.DB) tracking needs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// TrackUpload records path before the file is written, so a file can't
// exist in storage without a record.
func TrackUpload(ctx context.Context, db Execer, path string) error {
	if _, err := db.ExecContext(ctx, createFilesTable); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO `+FilesTable+` (path) VALUES (?)`, path)
	return err
}

// UntrackUpload forgets path after it is deleted (or failed to upload).
func UntrackUpload(ctx context.Context, db Execer, path string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM `+FilesTable+` WHERE path = ?`, path)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return nil
	}
	return err
}

// TrackedUploads lists the recorded paths (none if nothing was uploaded).
func TrackedUploads(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT path FROM `+FilesTable+` ORDER BY path`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
