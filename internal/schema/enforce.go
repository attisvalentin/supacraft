package schema

import (
	"fmt"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

func ValidateSelectColumns(requested []string, table *tenant.SchemaTable) error {
	for _, name := range requested {
		col, err := FindColumn(table, name)
		if err != nil {
			return fmt.Errorf("invalid select column: %s", name)
		}
		if !col.IsReadable() {
			return fmt.Errorf("column is not accessible: %s", name)
		}
	}
	return nil
}

func ValidateCreateColumns(columns []string, table *tenant.SchemaTable) error {
	for _, name := range columns {
		col, err := FindColumn(table, name)
		if err != nil {
			return fmt.Errorf("invalid column: %s", name)
		}
		if !col.IsCreatable() {
			return fmt.Errorf("column is not creatable: %s", name)
		}
	}
	return nil
}

func ValidateUpdateColumns(columns []string, table *tenant.SchemaTable) error {
	for _, name := range columns {
		col, err := FindColumn(table, name)
		if err != nil {
			return fmt.Errorf("invalid column: %s", name)
		}
		if !col.IsUpdatable() {
			return fmt.Errorf("column is not updatable: %s", name)
		}
	}
	return nil
}

func FilterNonReadableFromRow(row map[string]any, table *tenant.SchemaTable) map[string]any {
	for _, col := range table.Columns {
		if !col.IsReadable() {
			delete(row, col.Name)
		}
	}
	return row
}
