package probe

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yoockh/sebab/internal/config"
)

func TestAssertions(t *testing.T) {
	body := []byte(`{"status":"ok","count":3,"ready":true,"items":[{"name":"a","state":"ready"},{"name":"b"}]}`)
	h := http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}
	cases := []struct {
		a    config.Assertion
		fail string // "" means it passes
	}{
		{config.Assertion{Type: "body", Value: `"status":"ok"`}, ""},
		{config.Assertion{Type: "body", Value: "Maintenance", Negate: true}, ""},
		{config.Assertion{Type: "body", Value: "ready", Negate: true}, `body contains "ready"`},
		{config.Assertion{Type: "body", Value: "nope"}, `body does not contain "nope"`},
		{config.Assertion{Type: "json", Path: "status", Value: "ok"}, ""},
		{config.Assertion{Type: "json", Path: "count", Value: "3"}, ""},
		{config.Assertion{Type: "json", Path: "ready", Value: "true"}, ""},
		{config.Assertion{Type: "json", Path: "items[0].state", Value: "ready"}, ""},
		{config.Assertion{Type: "json", Path: "items[1].state"}, "items[1].state is missing"},
		{config.Assertion{Type: "json", Path: "items[5]"}, "items[5] is missing"},
		{config.Assertion{Type: "json", Path: "status", Value: "degraded"}, `status is "ok", expected "degraded"`},
		{config.Assertion{Type: "json", Path: "items"}, ""},
		{config.Assertion{Type: "header", Name: "content-type", Value: "JSON"}, ""},
		{config.Assertion{Type: "header", Name: "X-Missing"}, "header X-Missing is missing"},
		{config.Assertion{Type: "header", Name: "Content-Type", Value: "xml"}, "expected it to contain \"xml\""},
	}
	for _, tc := range cases {
		desc, _ := checkAssertions([]config.Assertion{tc.a}, h, body)
		if tc.fail == "" && desc != "" || tc.fail != "" && !strings.Contains(desc, tc.fail) {
			t.Errorf("%+v: got %q want %q", tc.a, desc, tc.fail)
		}
	}
	if desc, _ := checkAssertions([]config.Assertion{{Type: "json", Path: "a"}}, h, []byte("<html>")); desc != "response is not valid JSON" {
		t.Errorf("non-JSON body: %q", desc)
	}
	if got := splitPath("a.b[0][2].c"); strings.Join(got, ",") != "a,b,#0,#2,c" {
		t.Errorf("splitPath %v", got)
	}
}
