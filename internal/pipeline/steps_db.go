package pipeline

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// ExecuteDBSelect executes a db.select step.
func ExecuteDBSelect(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	resolvedWhere, err := resolveWhereMap(step.Where, pCtx)
	if err != nil {
		return fmt.Errorf("db.select: resolve where: %w", err)
	}

	table, err := schema.FindTableByName(deps.Schema, step.Table)
	if err != nil {
		return fmt.Errorf("db.select: %w", err)
	}

	columns := step.Columns
	if len(columns) == 0 {
		columns = allColumnNames(table)
	}
	for _, c := range columns {
		if _, err := schema.FindColumn(table, c); err != nil {
			return fmt.Errorf("db.select: unknown column %q in table %q", c, table.Name)
		}
	}

	whereSql, whereArgs, err := BuildWhereRaw(resolvedWhere, table, 0)
	if err != nil {
		return fmt.Errorf("db.select: build where: %w", err)
	}

	orderSQL, err := buildOrderBy(step.OrderBy, func(c string) bool { return hasColumn(table, c) })
	if err != nil {
		return fmt.Errorf("db.select: %w", err)
	}
	limit, offset, err := resolveLimitOffset(step, pCtx)
	if err != nil {
		return fmt.Errorf("db.select: %w", err)
	}
	if step.Single {
		limit = 1
	}
	pageSQL, pageArgs := limitOffsetSQL(limit, offset)

	sql, args := query.BuildSelect(table.Name, columns, whereSql, whereArgs, orderSQL, pageSQL, pageArgs)

	rows, err := deps.DBPool.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("db.select: query failed: %w", err)
	}
	results, err := query.ScanAllRowsToMaps(rows, deps.Timezone)
	rows.Close()
	if err != nil {
		return err
	}

	if len(step.Include) > 0 && len(results) > 0 {
		if err := includeRelated(ctx, step.Include, table, results, deps); err != nil {
			return fmt.Errorf("db.select: %w", err)
		}
	}

	if step.Assign != "" {
		if step.Single {
			if len(results) == 0 {
				pCtx.Set(step.Assign, nil)
			} else {
				pCtx.Set(step.Assign, results[0])
			}
		} else {
			pCtx.Set(step.Assign, results)
		}
	}
	return nil
}

// includeRelated attaches related rows under each row's related-table key.
// Pipelines are trusted config, so no access checks apply; columns default to
// the related table's readable columns.
func includeRelated(ctx context.Context, includes []tenant.StepInclude, table *tenant.SchemaTable, rows []map[string]any, deps *StepDeps) error {
	reqs := make([]query.IncludeRequest, len(includes))
	for i, inc := range includes {
		related, err := schema.FindTableByName(deps.Schema, inc.Table)
		if err != nil {
			return fmt.Errorf("include: %w", err)
		}
		reqs[i] = query.IncludeRequest{TableID: related.UUID, Select: inc.Columns}
	}
	allow := func(*tenant.SchemaTable) (string, []any, error) { return "", nil, nil }
	return query.ResolveIncludes(ctx, deps.DBPool, table.UUID, rows, reqs, deps.Schema, deps.Timezone, allow)
}

// ExecuteDBAggregate executes a db.aggregate step: count/sum/avg/min/max over
// the matching rows, optionally per group_by group.
func ExecuteDBAggregate(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	resolvedWhere, err := resolveWhereMap(step.Where, pCtx)
	if err != nil {
		return fmt.Errorf("db.aggregate: resolve where: %w", err)
	}

	table, err := schema.FindTableByName(deps.Schema, step.Table)
	if err != nil {
		return fmt.Errorf("db.aggregate: %w", err)
	}

	whereSql, whereArgs, err := BuildWhereRaw(resolvedWhere, table, 0)
	if err != nil {
		return fmt.Errorf("db.aggregate: build where: %w", err)
	}

	var selects, groups []string
	outputs := map[string]bool{}
	for _, g := range step.GroupBy {
		if !hasColumn(table, g) {
			return fmt.Errorf("db.aggregate: unknown group_by column %q in table %q", g, table.Name)
		}
		selects = append(selects, query.QuoteIdent(g))
		groups = append(groups, query.QuoteIdent(g))
		outputs[g] = true
	}
	aliases := make([]string, 0, len(step.Aggregates))
	for alias := range step.Aggregates {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	if len(aliases) == 0 {
		return fmt.Errorf("db.aggregate: aggregates is required")
	}
	for _, alias := range aliases {
		expr, err := aggregateSQL(step.Aggregates[alias], table)
		if err != nil {
			return fmt.Errorf("db.aggregate: aggregates.%s: %w", alias, err)
		}
		selects = append(selects, expr+" AS "+query.QuoteIdent(alias))
		outputs[alias] = true
	}

	sql := "SELECT " + strings.Join(selects, ", ") + " FROM " + query.QuoteIdent(table.Name)
	args := whereArgs
	if whereSql != "" {
		sql += " WHERE " + whereSql
	}
	if len(groups) > 0 {
		sql += " GROUP BY " + strings.Join(groups, ", ")
	}
	orderSQL, err := buildOrderBy(step.OrderBy, func(c string) bool { return outputs[c] })
	if err != nil {
		return fmt.Errorf("db.aggregate: %w", err)
	}
	limit, offset, err := resolveLimitOffset(step, pCtx)
	if err != nil {
		return fmt.Errorf("db.aggregate: %w", err)
	}
	pageSQL, pageArgs := limitOffsetSQL(limit, offset)
	sql += orderSQL + pageSQL
	args = append(args, pageArgs...)

	rows, err := deps.DBPool.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("db.aggregate: query failed: %w", err)
	}
	results, err := query.ScanAllRowsToMaps(rows, deps.Timezone)
	rows.Close()
	if err != nil {
		return err
	}

	if step.Assign != "" {
		if len(step.GroupBy) == 0 && len(results) == 1 {
			pCtx.Set(step.Assign, results[0])
		} else {
			pCtx.Set(step.Assign, results)
		}
	}
	return nil
}

// ParseAggregate splits "sum:amount" into its function and column. count may
// omit the column.
func ParseAggregate(spec string) (fn, column string, err error) {
	fn, column, _ = strings.Cut(strings.TrimSpace(spec), ":")
	fn = strings.ToLower(strings.TrimSpace(fn))
	column = strings.TrimSpace(column)
	switch fn {
	case "count":
	case "sum", "avg", "min", "max":
		if column == "" {
			return "", "", fmt.Errorf("%s needs a column (\"%s:column\")", fn, fn)
		}
	default:
		return "", "", fmt.Errorf("unknown aggregate %q (valid: count, sum, avg, min, max)", fn)
	}
	return fn, column, nil
}

func aggregateSQL(spec string, table *tenant.SchemaTable) (string, error) {
	fn, column, err := ParseAggregate(spec)
	if err != nil {
		return "", err
	}
	if column == "" {
		return "COUNT(*)", nil
	}
	if !hasColumn(table, column) {
		return "", fmt.Errorf("unknown column %q in table %q", column, table.Name)
	}
	if fn == "sum" {
		// TOTAL returns 0.0 instead of NULL for no rows.
		return "TOTAL(" + query.QuoteIdent(column) + ")", nil
	}
	return strings.ToUpper(fn) + "(" + query.QuoteIdent(column) + ")", nil
}

// ParseOrderBy splits "created_at desc" into its column and SQL direction.
func ParseOrderBy(spec string) (column, dir string, err error) {
	fields := strings.Fields(spec)
	switch len(fields) {
	case 1:
		return fields[0], "ASC", nil
	case 2:
		dir = strings.ToUpper(fields[1])
		if dir == "ASC" || dir == "DESC" {
			return fields[0], dir, nil
		}
	}
	return "", "", fmt.Errorf("invalid order_by %q (use \"column\" or \"column desc\")", spec)
}

func buildOrderBy(specs []string, known func(string) bool) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	clauses := make([]string, len(specs))
	for i, spec := range specs {
		col, dir, err := ParseOrderBy(spec)
		if err != nil {
			return "", err
		}
		if !known(col) {
			return "", fmt.Errorf("unknown order_by column %q", col)
		}
		clauses[i] = query.QuoteIdent(col) + " " + dir
	}
	return " ORDER BY " + strings.Join(clauses, ", "), nil
}

// resolveLimitOffset returns -1 for an unset limit.
func resolveLimitOffset(step *tenant.FunctionStep, pCtx *PipelineContext) (limit, offset int, err error) {
	limit, err = resolveCount(step.Limit, "limit", pCtx)
	if err != nil {
		return 0, 0, err
	}
	offset, err = resolveCount(step.Offset, "offset", pCtx)
	if err != nil {
		return 0, 0, err
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset, nil
}

func resolveCount(v any, name string, pCtx *PipelineContext) (int, error) {
	if v == nil {
		return -1, nil
	}
	resolved, err := ResolveValue(v, pCtx)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", name, err)
	}
	if resolved == nil || resolved == "" {
		return -1, nil
	}
	f, ok := toFloat64(resolved)
	if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %v", name, resolved)
	}
	return int(f), nil
}

func limitOffsetSQL(limit, offset int) (string, []any) {
	switch {
	case limit >= 0:
		return " LIMIT ? OFFSET ?", []any{limit, offset}
	case offset > 0:
		return " LIMIT -1 OFFSET ?", []any{offset}
	}
	return "", nil
}

func hasColumn(table *tenant.SchemaTable, name string) bool {
	_, err := schema.FindColumn(table, name)
	return err == nil
}

// ExecuteDBInsert executes a db.insert step. SQLite has no portable RETURNING,
// so we read the inserted row back via last_insert_rowid().
func ExecuteDBInsert(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	resolvedData, err := ResolveMap(step.Data, pCtx)
	if err != nil {
		return fmt.Errorf("db.insert: resolve data: %w", err)
	}

	table, err := schema.FindTableByName(deps.Schema, step.Table)
	if err != nil {
		return fmt.Errorf("db.insert: %w", err)
	}

	insertSQL, args := query.BuildInsert(table.Name, resolvedData)
	res, err := deps.DBPool.Exec(ctx, insertSQL, args...)
	if err != nil {
		return fmt.Errorf("db.insert: exec failed: %w", err)
	}

	lastID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("db.insert: last_insert_rowid: %w", err)
	}

	selectSQL := "SELECT * FROM " + query.QuoteIdent(table.Name) + " WHERE rowid = ?"
	rows, err := deps.DBPool.Query(ctx, selectSQL, lastID)
	if err != nil {
		return fmt.Errorf("db.insert: select inserted: %w", err)
	}
	defer rows.Close()

	results, err := query.ScanAllRowsToMaps(rows, deps.Timezone)
	if err != nil {
		return err
	}

	if step.Assign != "" {
		if len(results) == 1 {
			pCtx.Set(step.Assign, results[0])
		} else {
			pCtx.Set(step.Assign, results)
		}
	}
	return nil
}

// ExecuteDBUpdate executes a db.update step. SQLite has no portable RETURNING,
// so we re-SELECT matching rows after the UPDATE. The same WHERE is reused.
func ExecuteDBUpdate(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	resolvedWhere, err := resolveWhereMap(step.Where, pCtx)
	if err != nil {
		return fmt.Errorf("db.update: resolve where: %w", err)
	}

	resolvedData, err := ResolveMap(step.Data, pCtx)
	if err != nil {
		return fmt.Errorf("db.update: resolve data: %w", err)
	}

	table, err := schema.FindTableByName(deps.Schema, step.Table)
	if err != nil {
		return fmt.Errorf("db.update: %w", err)
	}

	whereSql, whereArgs, err := BuildWhereRaw(resolvedWhere, table, 0)
	if err != nil {
		return fmt.Errorf("db.update: build where: %w", err)
	}

	updateSQL, args := query.BuildUpdate(table.Name, resolvedData, whereSql, whereArgs)
	if _, err := deps.DBPool.Exec(ctx, updateSQL, args...); err != nil {
		return fmt.Errorf("db.update: exec failed: %w", err)
	}

	selectSQL, selectArgs := query.BuildSelect(table.Name, allColumnNames(table), whereSql, whereArgs, "", "", nil)
	rows, err := deps.DBPool.Query(ctx, selectSQL, selectArgs...)
	if err != nil {
		return fmt.Errorf("db.update: select updated: %w", err)
	}
	defer rows.Close()

	results, err := query.ScanAllRowsToMaps(rows, deps.Timezone)
	if err != nil {
		return err
	}

	if step.Assign != "" {
		if len(results) == 1 {
			pCtx.Set(step.Assign, results[0])
		} else {
			pCtx.Set(step.Assign, results)
		}
	}
	return nil
}

// ExecuteDBDelete executes a db.delete step.
func ExecuteDBDelete(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	resolvedWhere, err := resolveWhereMap(step.Where, pCtx)
	if err != nil {
		return fmt.Errorf("db.delete: resolve where: %w", err)
	}

	table, err := schema.FindTableByName(deps.Schema, step.Table)
	if err != nil {
		return fmt.Errorf("db.delete: %w", err)
	}

	whereSql, whereArgs, err := BuildWhereRaw(resolvedWhere, table, 0)
	if err != nil {
		return fmt.Errorf("db.delete: build where: %w", err)
	}

	sql, args := query.BuildDelete(table.Name, whereSql, whereArgs)

	res, err := deps.DBPool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("db.delete: query failed: %w", err)
	}

	if step.Assign != "" {
		affected, _ := res.RowsAffected()
		pCtx.Set(step.Assign, map[string]any{
			"deleted": affected,
		})
	}
	return nil
}

// resolveWhereMap resolves {{...}} references in where clause values. A
// column compared to a template must resolve to a plain value: if
// {"id": "{{request.id}}"} could resolve to {"$ne": ""}, a client would turn
// an equality check into an operator and match every row.
func resolveWhereMap(where map[string]any, pCtx *PipelineContext) (map[string]any, error) {
	if len(where) == 0 {
		return where, nil
	}
	resolved, err := ResolveMap(where, pCtx)
	if err != nil {
		return nil, err
	}
	for col, v := range where {
		if _, isTemplate := v.(string); !isTemplate {
			continue
		}
		if _, isObject := resolved[col].(map[string]any); isObject {
			return nil, fmt.Errorf("where.%s: value must be a string, number, boolean or null, not an object", col)
		}
	}
	return resolved, nil
}

func allColumnNames(table *tenant.SchemaTable) []string {
	names := make([]string, len(table.Columns))
	for i, c := range table.Columns {
		names[i] = c.Name
	}
	return names
}
