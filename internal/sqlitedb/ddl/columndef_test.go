package ddl

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func strptr(s string) *string { return &s }

// TestColumnDef covers the Postgres→SQLite translations needed for tenants like
// Kid Quest: `primary` keys, NOW() defaults, and bare enum/string defaults.
func TestColumnDef(t *testing.T) {
	cases := []struct {
		name string
		col  Column
		want string
	}{
		{"primary autogen id", Column{Name: "id", Type: "primary", Nullable: true},
			`"id" TEXT PRIMARY KEY DEFAULT (` + uuidV4Expr + `)`},
		{"now timestamp", Column{Name: "created_at", Type: "timestamptz", Nullable: true, Default: strptr("NOW()")},
			`"created_at" TEXT DEFAULT CURRENT_TIMESTAMP`},
		{"enum default quoted", Column{Name: "status", Type: "enum", Nullable: true, Default: strptr("pending")},
			`"status" TEXT DEFAULT 'pending'`},
		{"int default raw", Column{Name: "xp_reward", Type: "int", Nullable: true, Default: strptr("10")},
			`"xp_reward" INTEGER DEFAULT 10`},
		{"bool default false", Column{Name: "done", Type: "boolean", Nullable: true, Default: strptr("false")},
			`"done" INTEGER DEFAULT 0`},
		{"varchar not null no default", Column{Name: "title", Type: "varchar", Nullable: false},
			`"title" TEXT NOT NULL`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := columnDef(tc.col); got != tc.want {
				t.Fatalf("columnDef\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestCreateTableSQL_ProvisionsAndInserts builds the Kid Quest `tasks` table via
// the real engine, runs the CREATE against SQLite, and confirms an insert that
// omits id/status/created_at gets an auto-generated UUID id and timestamp.
func TestCreateTableSQL_ProvisionsAndInserts(t *testing.T) {
	tasks := Table{Name: "tasks", Columns: []Column{
		{Name: "id", Type: "primary", Nullable: true},
		{Name: "title", Type: "varchar", Nullable: false},
		{Name: "xp_reward", Type: "int", Nullable: true, Default: strptr("10")},
		{Name: "status", Type: "enum", Nullable: true, Default: strptr("pending")},
		{Name: "created_at", Type: "timestamptz", Nullable: true, Default: strptr("NOW()")},
	}}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(createTableSQL(tasks)); err != nil {
		t.Fatalf("CREATE failed: %v\nSQL: %s", err, createTableSQL(tasks))
	}
	if _, err := db.Exec(`INSERT INTO "tasks"("title") VALUES('Clean room')`); err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	var id, status, createdAt string
	var xp int
	if err := db.QueryRow(`SELECT id, xp_reward, status, created_at FROM tasks`).
		Scan(&id, &xp, &status, &createdAt); err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("id is not a v4 uuid: %q", id)
	}
	if xp != 10 || status != "pending" || strings.TrimSpace(createdAt) == "" {
		t.Fatalf("defaults wrong: xp=%d status=%q created_at=%q", xp, status, createdAt)
	}
}
