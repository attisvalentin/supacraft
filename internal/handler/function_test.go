package handler

import (
	"net/http"
	"strings"
	"testing"
)

// shopProject exercises path params, input validation, richer selects,
// aggregates, transactions and fail/try.
const shopProject = `{
  "name": "shop",
  "schema": {
    "tables": [
      {"name": "products", "columns": [
        {"name": "id", "type": "uuid", "primary": true},
        {"name": "name", "type": "varchar"},
        {"name": "price", "type": "real"},
        {"name": "stock", "type": "int"}]},
      {"name": "orders", "columns": [
        {"name": "id", "type": "uuid", "primary": true},
        {"name": "product_id", "type": "uuid"},
        {"name": "qty", "type": "int"}]}
    ],
    "relations": [
      {"from": {"table": "orders", "column": "product_id"}, "to": {"table": "products", "column": "id"}, "type": "one-to-many"}
    ]
  },
  "functions": {
    "list_products": {
      "route": {"path": "/fn/products", "method": "GET",
        "input": {"limit": {"type": "integer", "min": 1, "max": 50, "default": 2}}},
      "steps": [
        {"type": "db.select", "table": "products", "columns": ["name", "price"], "order_by": ["price desc", "name"], "limit": "{{query.limit}}", "assign": "rows"},
        {"type": "response", "data": {"rows": "{{rows}}"}}
      ]
    },
    "get_product": {
      "route": {"path": "/fn/products/{id}", "method": "GET"},
      "steps": [
        {"type": "db.select", "table": "products", "where": {"id": "{{params.id}}"}, "include": [{"table": "orders", "columns": ["qty"]}], "single": true, "assign": "product"},
        {"type": "if", "condition": "{{product}} == null", "then": [
          {"type": "fail", "status_code": 404, "message": "product {{params.id}} not found"}
        ]},
        {"type": "response", "data": {"product": "{{product}}"}}
      ]
    },
    "top_product": {
      "route": {"path": "/fn/products/top", "method": "GET"},
      "steps": [{"type": "response", "data": {"top": true}}]
    },
    "order": {
      "route": {"path": "/fn/orders", "method": "POST",
        "input": {
          "product_id": {"type": "string", "format": "uuid", "required": true},
          "qty": {"type": "integer", "min": 1, "required": true}}},
      "steps": [
        {"type": "transaction", "steps": [
          {"type": "db.insert", "table": "orders", "data": {"id": "{{request.product_id}}-{{request.qty}}", "product_id": "{{request.product_id}}", "qty": "{{request.qty}}"}},
          {"type": "db.select", "table": "products", "where": {"id": "{{request.product_id}}"}, "single": true, "assign": "product"},
          {"type": "if", "condition": "{{product.stock}} < {{request.qty}}", "then": [
            {"type": "fail", "status_code": 409, "message": "out of stock", "data": {"available": "{{product.stock}}"}}
          ]},
          {"type": "transform.math", "expression": "{{product.stock}} - {{request.qty}}", "assign": "left"},
          {"type": "db.update", "table": "products", "where": {"id": "{{request.product_id}}"}, "data": {"stock": "{{left}}"}}
        ]},
        {"type": "response", "status_code": 201, "data": {"left": "{{left}}"}}
      ]
    },
    "stats": {
      "route": {"path": "/fn/stats", "method": "GET"},
      "steps": [
        {"type": "db.aggregate", "table": "orders", "group_by": ["product_id"], "aggregates": {"orders": "count", "units": "sum:qty"}, "order_by": "units desc", "assign": "per_product"},
        {"type": "db.aggregate", "table": "orders", "aggregates": {"units": "sum:qty", "biggest": "max:qty"}, "assign": "total"},
        {"type": "response", "data": {"per_product": "{{per_product}}", "total": "{{total}}"}}
      ]
    },
    "risky": {
      "route": {"path": "/fn/risky", "method": "POST"},
      "steps": [
        {"type": "try", "as": "err", "steps": [
          {"type": "fail", "status_code": 418, "message": "teapot"}
        ], "catch": [
          {"type": "response", "data": {"caught": "{{err.message}}", "status": "{{err.status}}"}}
        ]}
      ]
    }
  }
}`

const (
	widget = "11111111-1111-1111-1111-111111111111"
	gadget = "22222222-2222-2222-2222-222222222222"
	gizmo  = "33333333-3333-3333-3333-333333333333"
)

func newShopAPI(t *testing.T) *testAPI {
	t.Helper()
	api := newTestAPIWith(t, shopProject)
	execSQL(t, api.dbPath, `INSERT INTO products (id, name, price, stock) VALUES (?, 'widget', 5, 3), (?, 'gadget', 20, 10), (?, 'gizmo', 20, 0)`, widget, gadget, gizmo)
	return api
}

func names(t *testing.T, out map[string]any) string {
	t.Helper()
	var got []string
	for _, r := range out["rows"].([]any) {
		got = append(got, r.(map[string]any)["name"].(string))
	}
	return strings.Join(got, ",")
}

func TestSelect_OrderByAndLimitFromInput(t *testing.T) {
	api := newShopAPI(t)

	code, out, raw := api.do("GET", "/fn/products", "")
	if code != http.StatusOK || names(t, out) != "gadget,gizmo" {
		t.Fatalf("default limit: %d %s", code, raw)
	}
	code, out, raw = api.do("GET", "/fn/products?limit=3", "")
	if code != http.StatusOK || names(t, out) != "gadget,gizmo,widget" {
		t.Fatalf("limit=3: %d %s", code, raw)
	}
	for _, bad := range []string{"0", "51", "abc", "1.5"} {
		if code, _, raw := api.do("GET", "/fn/products?limit="+bad, ""); code != http.StatusBadRequest {
			t.Errorf("limit=%s accepted: %d %s", bad, code, raw)
		}
	}
}

func TestPathParams(t *testing.T) {
	api := newShopAPI(t)
	execSQL(t, api.dbPath, `INSERT INTO orders (id, product_id, qty) VALUES ('o1', ?, 2)`, widget)

	code, out, raw := api.do("GET", "/fn/products/"+widget, "")
	if code != http.StatusOK {
		t.Fatalf("get product: %d %s", code, raw)
	}
	p := out["product"].(map[string]any)
	if p["name"] != "widget" || len(p["orders"].([]any)) != 1 {
		t.Fatalf("single row with include expected: %s", raw)
	}

	// A literal route segment wins over a parameter.
	if code, out, raw := api.do("GET", "/fn/products/top", ""); code != http.StatusOK || out["top"] != true {
		t.Fatalf("literal route not preferred: %d %s", code, raw)
	}

	code, out, raw = api.do("GET", "/fn/products/nope", "")
	if code != http.StatusNotFound || out["error"] != "product nope not found" {
		t.Fatalf("fail step: %d %s", code, raw)
	}
	if code, _, _ := api.do("GET", "/fn/products/"+widget+"/extra", ""); code != http.StatusNotFound {
		t.Fatalf("extra segment matched: %d", code)
	}
}

func TestInput_RejectsInvalidBody(t *testing.T) {
	api := newShopAPI(t)
	cases := map[string]string{
		`{}`:                         "product_id",
		`{"product_id":"x","qty":1}`: "product_id",
		`{"product_id":"` + widget + `","qty":0}`:             "qty",
		`{"product_id":"` + widget + `","qty":"2"}`:           "qty",
		`{"product_id":"` + widget + `","qty":1,"id":"mine"}`: "id",
	}
	for body, field := range cases {
		code, out, raw := api.do("POST", "/fn/orders", body)
		fields, _ := out["fields"].(map[string]any)
		if code != http.StatusBadRequest || fields[field] == nil {
			t.Errorf("%s: want 400 on %s, got %d %s", body, field, code, raw)
		}
	}
}

func TestTransaction_CommitsAndRollsBack(t *testing.T) {
	api := newShopAPI(t)

	code, out, raw := api.do("POST", "/fn/orders", `{"product_id":"`+widget+`","qty":2}`)
	if code != http.StatusCreated || out["left"] != float64(1) {
		t.Fatalf("order: %d %s", code, raw)
	}

	// Asking for more than the stock fails after the order row was inserted;
	// the rollback must remove it again.
	code, out, raw = api.do("POST", "/fn/orders", `{"product_id":"`+widget+`","qty":5}`)
	if code != http.StatusConflict || out["error"] != "out of stock" || out["available"] != float64(1) {
		t.Fatalf("out of stock: %d %s", code, raw)
	}
	if n := countRows(t, api.dbPath, "orders"); n != 1 {
		t.Fatalf("rolled-back order persisted: %d orders", n)
	}

	// The connection is usable again after a rollback.
	if code, _, raw := api.do("POST", "/fn/orders", `{"product_id":"`+widget+`","qty":1}`); code != http.StatusCreated {
		t.Fatalf("order after rollback: %d %s", code, raw)
	}
}

func TestAggregate(t *testing.T) {
	api := newShopAPI(t)
	execSQL(t, api.dbPath, `INSERT INTO orders (id, product_id, qty) VALUES ('a', ?, 1), ('b', ?, 4), ('c', ?, 2)`, widget, gadget, gadget)

	code, out, raw := api.do("GET", "/fn/stats", "")
	if code != http.StatusOK {
		t.Fatalf("stats: %d %s", code, raw)
	}
	per := out["per_product"].([]any)
	first := per[0].(map[string]any)
	if len(per) != 2 || first["product_id"] != gadget || first["units"] != float64(6) || first["orders"] != float64(2) {
		t.Fatalf("per product: %s", raw)
	}
	total := out["total"].(map[string]any)
	if total["units"] != float64(7) || total["biggest"] != float64(4) {
		t.Fatalf("total: %s", raw)
	}
}

func TestTry_CatchesFail(t *testing.T) {
	api := newShopAPI(t)
	code, out, raw := api.do("POST", "/fn/risky", `{}`)
	if code != http.StatusOK || out["caught"] != "teapot" || out["status"] != float64(418) {
		t.Fatalf("try/catch: %d %s", code, raw)
	}
}

func countRows(t *testing.T, dbPath, table string) int {
	t.Helper()
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
