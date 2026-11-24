package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeResearchJSONStrictlyRejectsUnknownAndTrailingValues(t *testing.T) {
	for _, body := range []string{
		`{"status":"running","unexpected":true}`,
		`{"status":"running"} {"status":"completed"}`,
		`{"status":`,
	} {
		req:=httptest.NewRequest("PATCH","/api/admin/research/experiments/x/state",strings.NewReader(body))
		var input benchmarkExperimentStateRequest
		if err:=decodeResearchJSON(httptest.NewRecorder(),req,&input);err==nil {
			t.Fatalf("decodeResearchJSON accepted invalid body %q",body)
		}
	}
}

func TestDecodeResearchJSONAcceptsSingleKnownObject(t *testing.T) {
	req:=httptest.NewRequest("PATCH","/api/admin/research/experiments/x/state",strings.NewReader(`{"status":"running"}`))
	var input benchmarkExperimentStateRequest
	if err:=decodeResearchJSON(httptest.NewRecorder(),req,&input);err!=nil{t.Fatalf("decode valid request: %v",err)}
	if input.Status!="running"{t.Fatalf("status=%q",input.Status)}
}
