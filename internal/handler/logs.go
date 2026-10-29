package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/attisvalentin/supacraft/internal/errorlog"
)

func HandleLogs(errLog *errorlog.ErrorLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")

		limit := 50
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		entries, total, err := errLog.Query(r.Context(), tenantID, limit, offset)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to query logs: %v", err),
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"data": entries,
			"meta": map[string]any{
				"total":  total,
				"limit":  limit,
				"offset": offset,
			},
		})
	}
}
