package server

import (
	"net/http/httptest"
	"encoding/json"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/store"
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


func TestBenchmarkExperimentActualSnapshotRemovesOnlyDeclaredParameter(t *testing.T) {
	base := map[string]interface{}{
		"backend":"llama.cpp","model_key":"alias","model_id":"qwen","scenario":"performance",
		"prompt_sha256":"prompt-a","condition_sha256":"fingerprint-a","condition_status":"complete",
		"backend_runtime_profile":map[string]interface{}{
			"profile_id":"baseline-id","profile_name":"Baseline","profile_version":1,"source":"user_declared","updated_at":"today",
			"backend":"llama.cpp","model_id":"qwen","parameters":map[string]interface{}{"threads":float64(4),"ctx_size":float64(8192)},
		},
		"model_identity":map[string]interface{}{"model_key":"alias","sha256":"model-sha"},
	}
	encoded,_:=json.Marshal(base)
	task:=&store.BenchmarkTask{ConfigJSON:string(encoded),PromptSHA256:"prompt-a"}
	actual,value,err:=benchmarkExperimentActualSnapshot(task,"threads")
	if err!=nil{t.Fatal(err)}
	if value!=float64(4){t.Fatalf("actual value=%v",value)}
	profile:=actual["backend_runtime_profile"].(map[string]interface{})
	params:=profile["parameters"].(map[string]interface{})
	if _,ok:=params["threads"];ok{t.Fatal("declared changed parameter must be excluded from baseline condition comparison")}
	if params["ctx_size"]!=float64(8192){t.Fatal("non-target runtime parameter must remain in condition snapshot")}
	if actual["prompt_sha256"]!="prompt-a"{t.Fatal("prompt SHA must be part of actual condition snapshot")}
	if _,ok:=actual["condition_sha256"];ok{t.Fatal("derived condition fingerprint must not be compared as a raw condition")}
}

func TestBenchmarkExperimentProfileParameterWhitelist(t *testing.T) {
	for _,name:=range []string{"threads","threads_batch","batch_size","ubatch_size","ctx_size","n_gpu_layers","flash_attn","cache_type_k","cache_type_v"}{
		if !benchmarkExperimentProfileParameter(name){t.Fatalf("expected supported parameter %q",name)}
	}
	for _,name:=range []string{"temperature","model_id","arbitrary.path",""}{
		if benchmarkExperimentProfileParameter(name){t.Fatalf("unexpected supported parameter %q",name)}
	}
}


func TestBenchmarkExperimentStatsAndP95(t *testing.T) {
 values:=[]float64{10,1,9,3,8,2,7,4,6,5}
 stats:=benchmarkExperimentStats(values)
 if stats.Count!=10||stats.Min!=1||stats.Max!=10||stats.Median!=5.5||stats.P95!=10{t.Fatalf("unexpected stats: %+v",stats)}
}
func TestBenchmarkExperimentMetricConclusionRequiresConsistentChange(t *testing.T) {
 base:=benchmarkExperimentMetricStats{Count:10,Median:100,P95:120}
 better:=benchmarkExperimentMetricStats{Count:10,Median:90,P95:108}
 if got,_,_:=benchmarkExperimentMetricConclusion(base,better,true);got!="improved"{t.Fatalf("latency conclusion=%s",got)}
 mixed:=benchmarkExperimentMetricStats{Count:10,Median:90,P95:130}
 if got,_,_:=benchmarkExperimentMetricConclusion(base,mixed,true);got!="inconclusive"{t.Fatalf("mixed conclusion=%s",got)}
 few:=benchmarkExperimentMetricStats{Count:4,Median:90,P95:108}
 if got,_,_:=benchmarkExperimentMetricConclusion(base,few,true);got!="insufficient_data"{t.Fatalf("small sample conclusion=%s",got)}
}
func TestBenchmarkExperimentCacheStateRequiresCompleteEvidence(t *testing.T) {
 zero,full,half:=int64(0),int64(100),int64(40)
 input,prompt:=int64(100),int64(0)
 if got:=benchmarkExperimentCacheState(&RequestModelTimings{CacheN:&zero,PromptN:&input},&input);got!="cold"{t.Fatalf("cold=%s",got)}
 if got:=benchmarkExperimentCacheState(&RequestModelTimings{CacheN:&full,PromptN:&prompt},&input);got!="full"{t.Fatalf("full=%s",got)}
 if got:=benchmarkExperimentCacheState(&RequestModelTimings{CacheN:&half,PromptN:&half},&input);got!="partial"{t.Fatalf("partial=%s",got)}
 if got:=benchmarkExperimentCacheState(nil,&input);got!="unknown"{t.Fatalf("unknown=%s",got)}
}
