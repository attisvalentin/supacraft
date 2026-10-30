package pipeline

import (
	"context"
	"fmt"

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

	whereSql, whereArgs, err := BuildWhereRaw(resolvedWhere, table, 0)
	if err != nil {
		return fmt.Errorf("db.select: build where: %w", err)
	}

	sql, args := query.BuildSelect(table.Name, columns, whereSql, whereArgs, "", "", nil)

	rows, err := deps.DBPool.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("db.select: query failed: %w", err)
	}
	defer rows.Close()

	results, err := query.ScanAllRowsToMaps(rows, deps.Timezone)
	if err != nil {
		return err
	}

	if step.Assign != "" {
		pCtx.Set(step.Assign, results)
	}
	return nil
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
