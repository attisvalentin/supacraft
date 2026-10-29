package handler

import (
	"encoding/json"
	"net/http"

	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/schema"
)

func HandleMeta(w http.ResponseWriter, r *http.Request) {
	cfg := middleware.TenantFromContext(r.Context())

	type columnResponse struct {
		UUID      string `json:"uuid"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		Readable  bool   `json:"readable"`
		Creatable bool   `json:"creatable"`
		Updatable bool   `json:"updatable"`
	}

	type tableResponse struct {
		UUID    string           `json:"uuid"`
		Name    string           `json:"name"`
		Columns []columnResponse `json:"columns"`
	}

	var tables []tableResponse
	for _, t := range cfg.Schema.Tables {
		readable := schema.GetReadableColumns(&t)
		cols := make([]columnResponse, len(readable))
		for i, c := range readable {
			cols[i] = columnResponse{
				UUID:      c.UUID,
				Name:      c.Name,
				Type:      c.Type,
				Readable:  c.IsReadable(),
				Creatable: c.IsCreatable(),
				Updatable: c.IsUpdatable(),
			}
		}
		tables = append(tables, tableResponse{UUID: t.UUID, Name: t.Name, Columns: cols})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": tables})
}
