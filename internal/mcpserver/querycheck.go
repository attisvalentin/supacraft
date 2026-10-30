package mcpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// query_db output goes straight into the agent's context, and data stored by
// app users can contain instructions aimed at the agent. Secrets therefore
// stay out of it: the JWT signing key (api_config) and password hashes.
var (
	protectedTables  = map[string]bool{"api_config": true}
	protectedColumns = map[string]map[string]bool{"users": {"password": true}}
	// Virtual tables that expose raw database pages, bypassing table checks.
	rawPageTables = []string{"sqlite_dbpage", "dbstat"}
)

var errProtected = errors.New("query reads protected data (api_config or users.password); list the columns you need instead of *")

// checkQuery rejects queries that would read protected data. It inspects the
// compiled bytecode (EXPLAIN), so aliases, functions such as hex(), CTEs and
// subqueries can't hide a read.
func checkQuery(ctx context.Context, db *sql.DB, query string) error {
	if !singleStatement(query) {
		return errors.New("query_db runs one statement at a time")
	}
	lower := strings.ToLower(query)
	for _, vt := range rawPageTables {
		if strings.Contains(lower, vt) {
			return fmt.Errorf("query_db cannot read %s", vt)
		}
	}

	roots, err := protectedRoots(ctx, db)
	if err != nil {
		return err
	}

	rows, err := db.QueryContext(ctx, "EXPLAIN "+query)
	if err != nil {
		return err
	}
	defer rows.Close()

	cursorRoot := map[int64]rootInfo{}
	for rows.Next() {
		var (
			addr, p1, p2, p3, p5 int64
			opcode               string
			p4, comment          any
		)
		if err := rows.Scan(&addr, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
			return err
		}
		switch opcode {
		case "OpenRead", "OpenWrite", "ReopenIdx":
			if p3 != 0 {
				return errors.New("query_db reads only the project database")
			}
			r, ok := roots[p2]
			if !ok {
				continue
			}
			if r.blocked {
				return errProtected
			}
			cursorRoot[p1] = r
		case "Column":
			if cursorRoot[p1].columns[p2] {
				return errProtected
			}
		}
	}
	return rows.Err()
}

type rootInfo struct {
	table   string
	blocked bool           // the whole b-tree is off limits
	columns map[int64]bool // record indexes of protected columns
}

// protectedRoots maps b-tree root pages to the protected tables they belong
// to. Indexes that contain a protected column are blocked outright.
func protectedRoots(ctx context.Context, db *sql.DB) (map[int64]rootInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, rootpage FROM sqlite_schema WHERE rootpage > 0`)
	if err != nil {
		return nil, err
	}
	type entry struct {
		typ, name, table string
		root             int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.typ, &e.name, &e.table, &e.root); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	rows.Close()

	roots := map[int64]rootInfo{}
	for _, e := range entries {
		switch {
		case protectedTables[e.table]:
			roots[e.root] = rootInfo{table: e.table, blocked: true}
		case protectedColumns[e.table] == nil:
		case e.typ == "table":
			idx, err := columnIndexes(ctx, db, e.table, protectedColumns[e.table])
			if err != nil {
				return nil, err
			}
			roots[e.root] = rootInfo{table: e.table, columns: idx}
		case e.typ == "index":
			covers, err := indexCovers(ctx, db, e.name, protectedColumns[e.table])
			if err != nil {
				return nil, err
			}
			roots[e.root] = rootInfo{table: e.table, blocked: covers}
		}
	}
	return roots, nil
}

func columnIndexes(ctx context.Context, db *sql.DB, table string, names map[string]bool) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT cid, name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var cid int64
		var name string
		if err := rows.Scan(&cid, &name); err != nil {
			return nil, err
		}
		if names[name] {
			out[cid] = true
		}
	}
	return out, rows.Err()
}

func indexCovers(ctx context.Context, db *sql.DB, index string, names map[string]bool) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_info(?)`, index)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if names[name.String] {
			return true, nil
		}
	}
	return false, rows.Err()
}

// singleStatement reports whether query holds one SQL statement (a trailing
// semicolon is fine). It skips string literals, quoted identifiers and
// comments when looking for ';'.
func singleStatement(query string) bool {
	q := query
	for i := 0; i < len(q); i++ {
		switch c := q[i]; {
		case c == '\'' || c == '"' || c == '`':
			j := strings.IndexByte(q[i+1:], c)
			if j < 0 {
				return true // unterminated; the parser will reject it
			}
			i += j + 1
		case c == '[':
			j := strings.IndexByte(q[i+1:], ']')
			if j < 0 {
				return true
			}
			i += j + 1
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				return true
			}
			i += j
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				return true
			}
			i += j + 3
		case c == ';':
			return strings.TrimSpace(stripComments(q[i+1:])) == ""
		}
	}
	return true
}

// stripComments removes -- and /* */ comments from the tail after a ';'.
func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '-' && i+1 < len(s) && s[i+1] == '-' {
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				break
			}
			i += j
			continue
		}
		if s[i] == '/' && i+1 < len(s) && s[i+1] == '*' {
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				break
			}
			i += j + 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
