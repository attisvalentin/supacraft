package query

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type Cursor struct {
	Column    string `json:"col"`
	Value     any    `json:"val"`
	Direction string `json:"dir"`
}

func EncodeCursor(column string, value any, direction string) string {
	c := Cursor{Column: column, Value: value, Direction: direction}
	data, _ := json.Marshal(c)
	return base64.StdEncoding.EncodeToString(data)
}

func DecodeCursor(cursor string) (*Cursor, error) {
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	var c Cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("invalid cursor format")
	}
	if c.Column == "" {
		return nil, fmt.Errorf("invalid cursor: missing column")
	}
	dir := strings.ToUpper(c.Direction)
	if dir != "ASC" && dir != "DESC" {
		c.Direction = "ASC"
	} else {
		c.Direction = dir
	}
	return &c, nil
}

func ApplyCursor(cursor *Cursor, paramOffset int) (string, []any) {
	op := ">"
	if cursor.Direction == "DESC" {
		op = "<"
	}
	_ = paramOffset
	sql := fmt.Sprintf("%s %s ?", QuoteIdent(cursor.Column), op)
	return sql, []any{cursor.Value}
}

const MaxLimit = 100

func ApplyPagination(limit, offset, paramOffset int) (string, []any) {
	if limit <= 0 {
		return "", nil
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	_ = paramOffset
	return " LIMIT ? OFFSET ?", []any{limit, offset}
}
