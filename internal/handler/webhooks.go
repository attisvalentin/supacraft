package handler

import (
	"context"

	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// queryRows is query.QueryMaps in the tenant's timezone.
func queryRows(ctx context.Context, q tenant.Querier, cfg *tenant.TenantConfig, stmt string, args ...any) ([]map[string]any, error) {
	return query.QueryMaps(ctx, q, tenant.LoadTimezone(cfg.APISettings.Timezone), stmt, args...)
}
