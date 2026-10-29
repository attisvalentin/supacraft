package schema

import (
	"fmt"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

func FindTable(schema *tenant.Schema, tableID string) (*tenant.SchemaTable, error) {
	for i := range schema.Tables {
		if schema.Tables[i].UUID == tableID {
			return &schema.Tables[i], nil
		}
	}
	return nil, fmt.Errorf("table not found: %s", tableID)
}

func FindTableByName(schema *tenant.Schema, tableName string) (*tenant.SchemaTable, error) {
	for i := range schema.Tables {
		if schema.Tables[i].Name == tableName {
			return &schema.Tables[i], nil
		}
	}
	return nil, fmt.Errorf("table not found: %s", tableName)
}

func FindColumn(table *tenant.SchemaTable, colName string) (*tenant.SchemaColumn, error) {
	for i := range table.Columns {
		if table.Columns[i].Name == colName {
			return &table.Columns[i], nil
		}
	}
	return nil, fmt.Errorf("column not found: %s", colName)
}

func GetReadableColumns(table *tenant.SchemaTable) []tenant.SchemaColumn {
	var readable []tenant.SchemaColumn
	for _, col := range table.Columns {
		if col.IsReadable() {
			readable = append(readable, col)
		}
	}
	return readable
}
