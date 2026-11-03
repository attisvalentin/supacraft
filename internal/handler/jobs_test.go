package handler

import (
	"reflect"
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestMaskJobs_Nested(t *testing.T) {
	list := []tenant.Job{{
		LastError: "POST https://x.co/hook?k=topsecret failed",
		Args: map[string]any{
			"token": "topsecret",
			"deep":  map[string]any{"list": []any{"a-topsecret-b", 1.0}},
		},
	}}
	MaskJobs(list, map[string]string{"KEY": "topsecret"})
	want := tenant.Job{
		LastError: "POST https://x.co/hook?k=[secret KEY] failed",
		Args: map[string]any{
			"token": "[secret KEY]",
			"deep":  map[string]any{"list": []any{"a-[secret KEY]-b", 1.0}},
		},
	}
	if !reflect.DeepEqual(list[0], want) {
		t.Errorf("got %+v", list[0])
	}
}
