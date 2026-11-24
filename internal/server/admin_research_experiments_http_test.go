package server

import (
	"net/http/httptest"
	"encoding/json"
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


func TestValidateBenchmarkExperimentCreateRequestEnforcesSingleVariable(t *testing.T) {
	base:=benchmarkExperimentCreateRequest{
		Name:"threads comparison",ChangedParameter:"threads",
		BaselineValue:json.RawMessage(`4`),CandidateValue:json.RawMessage(`6`),
		BaselineCondition:json.RawMessage(`{"model":"qwen","prompt_sha":"abc","max_tokens":128}`),
		CandidateCondition:json.RawMessage(`{"max_tokens":128,"prompt_sha":"abc","model":"qwen"}`),
		PlannedSamples:10,
	}
	if err:=validateBenchmarkExperimentCreateRequest(base);err!=nil{t.Fatalf("equivalent condition objects should be accepted: %v",err)}
	changed:=base;changed.CandidateCondition=json.RawMessage(`{"model":"qwen","prompt_sha":"different","max_tokens":128}`)
	if err:=validateBenchmarkExperimentCreateRequest(changed);err==nil{t.Fatal("different non-target conditions must be rejected")}
	same:=base;same.CandidateValue=json.RawMessage(`4`)
	if err:=validateBenchmarkExperimentCreateRequest(same);err==nil{t.Fatal("identical baseline and candidate values must be rejected")}
	array:=base;array.BaselineCondition=json.RawMessage(`[]`)
	if err:=validateBenchmarkExperimentCreateRequest(array);err==nil{t.Fatal("condition snapshots must be objects")}
}
