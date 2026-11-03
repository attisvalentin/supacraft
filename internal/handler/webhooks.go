package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// emitWebhooks queues the deliveries for a change the request made. The
// change is already saved, so a failure is logged, not returned.
func emitWebhooks(r *http.Request, db *tenant.DB, cfg *tenant.TenantConfig, table, op string, rows []map[string]any) {
	if err := tenant.EmitWebhooks(r.Context(), db, cfg, table, op, rows); err != nil {
		logError(r, http.StatusInternalServerError, fmt.Sprintf("queue webhook: %v", err))
	}
}

// rowsBeforeDelete reads the rows a delete will remove, when a webhook
// wants them; nil otherwise.
func rowsBeforeDelete(ctx context.Context, db *tenant.DB, cfg *tenant.TenantConfig, table *tenant.SchemaTable, whereSQL string, whereArgs []any) ([]map[string]any, error) {
	if !cfg.WantsWebhook(table.Name, tenant.OpDelete) {
		return nil, nil
	}
	cols := make([]string, len(table.Columns))
	for i, c := range table.Columns {
		cols[i] = c.Name
	}
	stmt, args := query.BuildSelect(table.Name, cols, whereSQL, whereArgs, "", "", nil)
	rows, err := db.Query(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return query.ScanAllRowsToMaps(rows, tenant.LoadTimezone(cfg.APISettings.Timezone))
}
