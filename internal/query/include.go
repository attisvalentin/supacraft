package query

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

type IncludeRequest struct {
	TableID string   `json:"table_id"`
	Select  []string `json:"select"`
}

// IncludeAuthorizer decides whether a related table may be included. It
// returns the extra WHERE fragment (e.g. the table's row policy) to apply, or
// an error when the caller may not read the table.
type IncludeAuthorizer func(table *tenant.SchemaTable) (policySQL string, policyArgs []any, err error)

// ErrIncludeForbidden is returned when the authorizer rejects a table.
var ErrIncludeForbidden = fmt.Errorf("included table is not accessible")

func ResolveIncludes(
	ctx context.Context,
	dbPool *tenant.DB,
	mainTableUUID string,
	mainResults []map[string]any,
	includes []IncludeRequest,
	sch *tenant.Schema,
	loc *time.Location,
	authorize IncludeAuthorizer,
) error {
	for _, inc := range includes {
		relatedTable, err := schema.FindTable(sch, inc.TableID)
		if err != nil {
			return fmt.Errorf("included table not found: %s", inc.TableID)
		}
		if err := schema.ValidateSelectColumns(inc.Select, relatedTable); err != nil {
			return err
		}
		policySQL, policyArgs, err := authorize(relatedTable)
		if err != nil {
			return err
		}

		rel, fromMain, err := findRelation(sch, mainTableUUID, inc.TableID)
		if err != nil {
			return err
		}

		var mainColName, relatedColName string
		if fromMain {
			mainColName = rel.From.Column
			relatedColName = rel.To.Column
		} else {
			mainColName = rel.To.Column
			relatedColName = rel.From.Column
		}

		mainTable, _ := schema.FindTable(sch, mainTableUUID)
		if _, err := schema.FindColumn(mainTable, mainColName); err != nil {
			return fmt.Errorf("relation column not found in main table: %s", mainColName)
		}
		if _, err := schema.FindColumn(relatedTable, relatedColName); err != nil {
			return fmt.Errorf("relation column not found in related table: %s", relatedColName)
		}

		columns := inc.Select
		if len(columns) == 0 {
			readable := schema.GetReadableColumns(relatedTable)
			columns = make([]string, len(readable))
			for i, c := range readable {
				columns[i] = c.Name
			}
		}
		hasFk := false
		for _, c := range columns {
			if c == relatedColName {
				hasFk = true
				break
			}
		}
		if !hasFk {
			columns = append(columns, relatedColName)
		}

		valuesSet := map[any]bool{}
		for _, row := range mainResults {
			if v, ok := row[mainColName]; ok && v != nil {
				valuesSet[v] = true
			}
		}
		if len(valuesSet) == 0 {
			continue
		}
		linkValues := make([]any, 0, len(valuesSet))
		for v := range valuesSet {
			linkValues = append(linkValues, v)
		}

		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = QuoteIdent(c)
		}
		placeholders := make([]string, len(linkValues))
		for i := range linkValues {
			placeholders[i] = "?"
		}

		sql := fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s)",
			strings.Join(quoted, ", "),
			QuoteIdent(relatedTable.Name),
			QuoteIdent(relatedColName),
			strings.Join(placeholders, ", "),
		)
		args := linkValues
		if policySQL != "" {
			sql += " AND (" + policySQL + ")"
			args = append(args, policyArgs...)
		}

		rows, err := dbPool.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("include query failed for %s: %w", relatedTable.Name, err)
		}

		all, err := ScanAllRowsToMaps(rows, loc)
		rows.Close()
		if err != nil {
			return fmt.Errorf("scan included rows: %w", err)
		}

		grouped := map[any][]map[string]any{}
		for _, row := range all {
			row = schema.FilterNonReadableFromRow(row, relatedTable)
			fkVal := row[relatedColName]
			grouped[fkVal] = append(grouped[fkVal], row)
		}

		for _, mainRow := range mainResults {
			linkVal := mainRow[mainColName]
			if related, ok := grouped[linkVal]; ok {
				mainRow[relatedTable.Name] = related
			} else {
				mainRow[relatedTable.Name] = []map[string]any{}
			}
		}
	}

	return nil
}

func findRelation(sch *tenant.Schema, tableAUUID, tableBUUID string) (*tenant.Relation, bool, error) {
	tableA, err := schema.FindTable(sch, tableAUUID)
	if err != nil {
		return nil, false, fmt.Errorf("table not found: %s", tableAUUID)
	}
	tableB, err := schema.FindTable(sch, tableBUUID)
	if err != nil {
		return nil, false, fmt.Errorf("table not found: %s", tableBUUID)
	}

	for i := range sch.Relations {
		r := &sch.Relations[i]
		if r.From.Table == tableA.Name && r.To.Table == tableB.Name {
			return r, true, nil
		}
		if r.From.Table == tableB.Name && r.To.Table == tableA.Name {
			return r, false, nil
		}
	}
	return nil, false, fmt.Errorf("no relation found between tables")
}
