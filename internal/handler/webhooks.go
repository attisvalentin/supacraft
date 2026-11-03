package handler

import (
	"context"

	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// queryRows runs a query and reads its rows; with UPDATE/DELETE ...
// RETURNING * they are exactly the rows the statement changed.
func queryRows(ctx context.Context, q tenant.Querier, cfg *tenant.TenantConfig, stmt string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return query.ScanAllRowsToMaps(rows, tenant.LoadTimezone(cfg.APISettings.Timezone))
}
