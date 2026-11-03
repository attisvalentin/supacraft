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

// queryRows runs a statement with RETURNING * and reads the rows it
// changed: an UPDATE's rows after the change, a DELETE's removed rows.
// Reading them in the same statement keeps them exactly the rows written.
func queryRows(ctx context.Context, q tenant.Querier, cfg *tenant.TenantConfig, stmt string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(ctx, stmt+" RETURNING *", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return query.ScanAllRowsToMaps(rows, tenant.LoadTimezone(cfg.APISettings.Timezone))
}
