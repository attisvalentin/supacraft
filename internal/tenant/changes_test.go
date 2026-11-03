package tenant

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestEmitChanges(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db := &DB{DB: sqlDB}
	cfg := &TenantConfig{DBName: "c", APISettings: APISettings{Realtime: &RealtimeSettings{Tables: []string{"messages"}}}}

	// Nothing logged yet: no table, bounds start at 1.
	if oldest, last, err := ChangeBounds(ctx, db); err != nil || oldest != 1 || last != 0 {
		t.Fatalf("empty bounds: %d %d %v", oldest, last, err)
	}
	if !cfg.WantsChanges("messages", OpDelete) || cfg.WantsChanges("notes", OpInsert) {
		t.Fatal("WantsChanges")
	}
	var told []string
	ChangesRecorded = func(db string) { told = append(told, db) }
	t.Cleanup(func() { ChangesRecorded = nil })

	if err := EmitChanges(ctx, db, cfg, "notes", OpInsert, []map[string]any{{"id": "n"}}); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{{"id": "a", "n": int64(7), "f": 1.5}, {"id": "b", "n": int64(9007199254740993)}}
	if err := EmitChanges(ctx, db, cfg, "messages", OpInsert, rows); err != nil {
		t.Fatal(err)
	}
	if len(told) != 1 || told[0] != "c" {
		t.Fatalf("notified %v", told)
	}
	list, err := ReadChanges(ctx, db, 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	// Only the streamed table is logged; numbers keep their type.
	if len(list) != 2 || list[0].Table != "messages" || list[0].Op != OpInsert || list[0].Seq != 1 {
		t.Fatalf("logged %+v", list)
	}
	if list[0].Row["n"] != int64(7) || list[0].Row["f"] != 1.5 || list[1].Row["n"] != int64(9007199254740993) {
		t.Fatalf("row values %+v %+v", list[0].Row, list[1].Row)
	}
	if after, _ := ReadChanges(ctx, db, 1, "messages", 10); len(after) != 1 || after[0].Seq != 2 {
		t.Fatalf("after 1: %+v", after)
	}

	// Expired changes are pruned on a later write; seq keeps growing.
	if _, err := sqlDB.Exec(`UPDATE "_supacraft_changes" SET "created_at" = ?`, time.Now().Add(-ChangeRetention-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	lastPrune.Delete("c")
	if err := EmitChanges(ctx, db, cfg, "messages", OpDelete, []map[string]any{{"id": "a"}}); err != nil {
		t.Fatal(err)
	}
	if oldest, last, err := ChangeBounds(ctx, db); err != nil || oldest != 3 || last != 3 {
		t.Fatalf("bounds after prune: %d %d %v", oldest, last, err)
	}
}
