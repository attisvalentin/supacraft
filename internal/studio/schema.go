package studio

import "encoding/json"

// The config schema mirrors the "schema" object of the compiled tenant config
// ({DataDir}/tenants/{id}.json).

type cfgSchema struct {
	Tables    []cfgTable    `json:"tables"`
	Relations []cfgRelation `json:"relations"`
}

type cfgTable struct {
	Name    string      `json:"name"`
	Exposed bool        `json:"exposed"`
	Columns []cfgColumn `json:"columns"`
}

type cfgColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Readable *bool  `json:"readable"`
}

type cfgRelation struct {
	From cfgRelEnd `json:"from"`
	To   cfgRelEnd `json:"to"`
	Type string    `json:"type"`
}

type cfgRelEnd struct {
	Table  string `json:"table"`
	Column string `json:"column"`
}

// parseCfgSchema decodes the raw "schema" value from a tenant payload. A nil or
// malformed value yields an empty schema rather than an error.
func parseCfgSchema(raw any) cfgSchema {
	var s cfgSchema
	if raw == nil {
		return s
	}
	if b, err := json.Marshal(raw); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// readableLookup maps "table.column" to the config schema's readable flag so the
// live-introspected columns can be enriched with API visibility.
func readableLookup(cfg cfgSchema) map[string]*bool {
	m := map[string]*bool{}
	for _, t := range cfg.Tables {
		for _, c := range t.Columns {
			if c.Readable != nil {
				m[t.Name+"."+c.Name] = c.Readable
			}
		}
	}
	return m
}

// relationsFrom converts config schema relations to the API shape.
func relationsFrom(cfg cfgSchema) []Relation {
	out := make([]Relation, 0, len(cfg.Relations))
	for _, r := range cfg.Relations {
		out = append(out, Relation{
			FromTable: r.From.Table, FromColumn: r.From.Column,
			ToTable: r.To.Table, ToColumn: r.To.Column, Type: r.Type,
		})
	}
	return out
}
