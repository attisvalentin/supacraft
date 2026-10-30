package pipeline

import (
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestBuildWhereRaw_RejectsUnknownColumns(t *testing.T) {
	table := &tenant.SchemaTable{Name: "posts", Columns: []tenant.SchemaColumn{{UUID: "id", Name: "id"}}}
	if _, _, err := BuildWhereRaw(map[string]any{"id": "1"}, table, 0); err != nil {
		t.Fatalf("known column rejected: %v", err)
	}
	if _, _, err := BuildWhereRaw(map[string]any{"nope": "nope"}, table, 0); err == nil {
		t.Fatal("unknown column accepted")
	}
}
