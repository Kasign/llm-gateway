package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

type benchmarkExperimentCreateRequest struct {
	Name string `json:"name"`
	ChangedParameter string `json:"changed_parameter"`
	BaselineValue json.RawMessage `json:"baseline_value"`
	CandidateValue json.RawMessage `json:"candidate_value"`
	BaselineCondition json.RawMessage `json:"baseline_condition"`
	CandidateCondition json.RawMessage `json:"candidate_condition"`
	PlannedSamples int `json:"planned_samples"`
}
type benchmarkExperimentStateRequest struct {
	Status string `json:"status"`
	Report json.RawMessage `json:"report"`
	ErrorCode string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	StartedAt *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}
type benchmarkExperimentJobRequest struct {
	Group string `json:"group"`
	JobID string `json:"job_id"`
}
func decodeResearchJSON(w http.ResponseWriter,r *http.Request,dst interface{}) error {
	decoder:=json.NewDecoder(http.MaxBytesReader(w,r.Body,256*1024));decoder.DisallowUnknownFields()
	if err:=decoder.Decode(dst);err!=nil{return err}
	var extra interface{};if err:=decoder.Decode(&extra);err!=io.EOF{return errors.New("request must contain exactly one JSON value")}
	return nil
}
func validateBenchmarkExperimentCreateRequest(in benchmarkExperimentCreateRequest) error {
	if len(in.BaselineValue)==0||len(in.CandidateValue)==0{return errors.New("baseline and candidate values are required")}
	if (len(in.BaselineCondition)==0)!=(len(in.CandidateCondition)==0){return errors.New("optional condition snapshots must be supplied as a pair")}
	var baselineValue,candidateValue interface{}
	if err:=json.Unmarshal(in.BaselineValue,&baselineValue);err!=nil{return errors.New("baseline value must be valid JSON")}
	if err:=json.Unmarshal(in.CandidateValue,&candidateValue);err!=nil{return errors.New("candidate value must be valid JSON")}
	if reflect.DeepEqual(baselineValue,candidateValue){return errors.New("baseline and candidate values must differ")}
	if len(in.BaselineCondition)>0 {
		var baselineCondition,candidateCondition map[string]interface{}
		if err:=json.Unmarshal(in.BaselineCondition,&baselineCondition);err!=nil||baselineCondition==nil{return errors.New("baseline condition must be a JSON object")}
		if err:=json.Unmarshal(in.CandidateCondition,&candidateCondition);err!=nil||candidateCondition==nil{return errors.New("candidate condition must be a JSON object")}
		if !reflect.DeepEqual(baselineCondition,candidateCondition){return errors.New("baseline and candidate conditions must be identical; only changed_parameter may differ")}
	}
	return nil
}

func benchmarkExperimentView(e *store.BenchmarkExperiment) map[string]interface{} {
	var bv,cv,bc,cc,report json.RawMessage;var bj,cj []string
	_ = json.Unmarshal([]byte(e.BaselineValueJSON),&bv);_ = json.Unmarshal([]byte(e.CandidateValueJSON),&cv)
	_ = json.Unmarshal([]byte(e.BaselineConditionJSON),&bc);_ = json.Unmarshal([]byte(e.CandidateConditionJSON),&cc)
	_ = json.Unmarshal([]byte(e.BaselineJobIDsJSON),&bj);_ = json.Unmarshal([]byte(e.CandidateJobIDsJSON),&cj)
	if e.ReportJSON!=""{_ = json.Unmarshal([]byte(e.ReportJSON),&report)}
	return map[string]interface{}{"experiment_id":e.ExperimentID,"name":e.Name,"status":e.Status,"changed_parameter":e.ChangedParameter,
		"baseline_value":bv,"candidate_value":cv,"baseline_condition":bc,"candidate_condition":cc,
		"baseline_job_ids":bj,"candidate_job_ids":cj,"planned_samples":e.PlannedSamples,"report":report,
		"error_code":e.ErrorCode,"error_message":e.ErrorMessage,"started_at":e.StartedAt,"finished_at":e.FinishedAt,
		"created_at":e.CreatedAt,"updated_at":e.UpdatedAt}
}

// benchmarkExperimentProfileParameter 是研究实验允许变更的 llama.cpp 档案字段。
// 只接受有明确来源的 BackendRuntimeProfile 参数，不接受任意 JSON 路径。
func benchmarkExperimentProfileParameter(name string) bool {
	switch strings.TrimSpace(name) {
	case "threads", "threads_batch", "batch_size", "ubatch_size", "ctx_size",
		"n_gpu_layers", "flash_attn", "cache_type_k", "cache_type_v":
		return true
	default:
		return false
	}
}

func benchmarkExperimentActualSnapshot(task *store.BenchmarkTask, changedParameter string) (map[string]interface{}, interface{}, error) {
	if task == nil || strings.TrimSpace(task.ConfigJSON) == "" { return nil, nil, errors.New("benchmark condition snapshot is unavailable") }
	if !benchmarkExperimentProfileParameter(changedParameter) { return nil, nil, errors.New("changed_parameter must be a supported llama.cpp runtime profile field") }
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(task.ConfigJSON), &snapshot); err != nil || snapshot == nil { return nil, nil, errors.New("benchmark condition snapshot is invalid") }
	profile, ok := snapshot["backend_runtime_profile"].(map[string]interface{})
	if !ok { return nil, nil, errors.New("benchmark job has no declared Backend runtime profile") }
	parameters, ok := profile["parameters"].(map[string]interface{})
	if !ok { return nil, nil, errors.New("benchmark runtime profile parameters are unavailable") }
	actualValue, ok := parameters[changedParameter]
	if !ok { return nil, nil, errors.New("benchmark job does not declare the experiment parameter value") }
	delete(parameters, changedParameter)
	// 档案 ID、版本、名称、来源和更新时间用于追溯，不是运行条件；只比较 Backend、Model 与实际参数。
	snapshot["backend_runtime_profile"] = map[string]interface{}{
		"backend": profile["backend"], "model_id": profile["model_id"], "parameters": parameters,
	}
	delete(snapshot, "condition_sha256")
	delete(snapshot, "condition_status")
	delete(snapshot, "condition_missing")
	delete(snapshot, "model_key")
	if identity, ok := snapshot["model_identity"].(map[string]interface{}); ok { delete(identity, "model_key") }
	// Prompt 正文不落入 ConfigJSON；其 SHA256 是实际运行条件的一部分，必须纳入核对。
	snapshot["prompt_sha256"] = task.PromptSHA256
	return snapshot, actualValue, nil
}

func benchmarkExperimentExpectedValue(raw string) (interface{}, error) {
	var value interface{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil { return nil, errors.New("experiment parameter value is invalid") }
	return value, nil
}

// validateBenchmarkExperimentJob 以服务端持久化的 Task.ConfigJSON 为准核对新 Job。
// 只有声明参数的值符合分组，且剔除该参数后所有关联 Job 的完整快照一致，才允许关联。
func validateBenchmarkExperimentJob(ctx context.Context, repo *store.BenchmarkRepo, experiment *store.BenchmarkExperiment, group string, task *store.BenchmarkTask) error {
	if experiment == nil || task == nil { return errors.New("experiment or benchmark job is unavailable") }
	if group != "baseline" && group != "candidate" { return errors.New("experiment group must be baseline or candidate") }
	actual, actualValue, err := benchmarkExperimentActualSnapshot(task, experiment.ChangedParameter)
	if err != nil { return err }
	expectedRaw := experiment.BaselineValueJSON
	if group == "candidate" { expectedRaw = experiment.CandidateValueJSON }
	expected, err := benchmarkExperimentExpectedValue(expectedRaw)
	if err != nil { return err }
	if !reflect.DeepEqual(actualValue, expected) { return errors.New("benchmark job runtime parameter does not match the selected experiment group") }
	var baselineIDs, candidateIDs []string
	if err := json.Unmarshal([]byte(experiment.BaselineJobIDsJSON), &baselineIDs); err != nil { return errors.New("experiment baseline job associations are invalid") }
	if err := json.Unmarshal([]byte(experiment.CandidateJobIDsJSON), &candidateIDs); err != nil { return errors.New("experiment candidate job associations are invalid") }
	for _, existing := range append(append([]string{}, baselineIDs...), candidateIDs...) {
		previous, err := repo.GetTask(ctx, existing)
		if err != nil { return errors.New("an existing experiment job cannot be read; refusing to mix unverifiable samples") }
		previousSnapshot, previousValue, err := benchmarkExperimentActualSnapshot(previous, experiment.ChangedParameter)
		if err != nil { return errors.New("an existing experiment job has incomplete runtime parameter evidence") }
		previousExpectedRaw := experiment.BaselineValueJSON
		for _, candidateID := range candidateIDs { if candidateID == existing { previousExpectedRaw = experiment.CandidateValueJSON; break } }
		previousExpected, err := benchmarkExperimentExpectedValue(previousExpectedRaw)
		if err != nil || !reflect.DeepEqual(previousValue, previousExpected) { return errors.New("an existing experiment job does not match its declared group") }
		if !reflect.DeepEqual(actual, previousSnapshot) { return errors.New("benchmark job conditions differ beyond the declared experiment parameter") }
	}
	return nil
}

func (s *Server) benchmarkExperimentsAPI(w http.ResponseWriter,r *http.Request) {
	if !s.requireAdmin(w,r){return};if s.benchmarkExperimentRepo==nil{jsonError(w,503,"research_unavailable","research experiment storage is unavailable");return}
	w.Header().Set("Cache-Control","no-store")
	switch r.Method {
	case http.MethodGet:
		limit,offset:=50,0;var err error
		if raw:=r.URL.Query().Get("limit");raw!=""{limit,err=strconv.Atoi(raw);if err!=nil{jsonError(w,400,"invalid_pagination","limit must be an integer");return}}
		if raw:=r.URL.Query().Get("offset");raw!=""{offset,err=strconv.Atoi(raw);if err!=nil{jsonError(w,400,"invalid_pagination","offset must be an integer");return}}
		items,total,err:=s.benchmarkExperimentRepo.List(r.Context(),r.URL.Query().Get("status"),limit,offset)
		if err!=nil{jsonError(w,400,"invalid_research_query",err.Error());return}
		views:=make([]map[string]interface{},0,len(items));for i:=range items{views=append(views,benchmarkExperimentView(&items[i]))}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items":views,"total":total,"limit":limit,"offset":offset})
	case http.MethodPost:
		var in benchmarkExperimentCreateRequest;if err:=decodeResearchJSON(w,r,&in);err!=nil{jsonError(w,400,"invalid_request","invalid research experiment JSON");return}
		if err:=validateBenchmarkExperimentCreateRequest(in);err!=nil{jsonError(w,400,"invalid_research_experiment",err.Error());return}
		planned:=in.PlannedSamples;if planned==0{planned=10}
		e:=store.BenchmarkExperiment{ExperimentID:newID(),Name:strings.TrimSpace(in.Name),Status:"draft",ChangedParameter:strings.TrimSpace(in.ChangedParameter),
			BaselineValueJSON:string(in.BaselineValue),CandidateValueJSON:string(in.CandidateValue),BaselineConditionJSON:string(in.BaselineCondition),CandidateConditionJSON:string(in.CandidateCondition),
			BaselineJobIDsJSON:"[]",CandidateJobIDsJSON:"[]",PlannedSamples:planned}
		if err:=s.benchmarkExperimentRepo.Create(r.Context(),e);err!=nil{jsonError(w,400,"invalid_research_experiment",err.Error());return}
		w.WriteHeader(http.StatusCreated);_ = json.NewEncoder(w).Encode(map[string]interface{}{"experiment":benchmarkExperimentView(&e)})
	default:w.Header().Set("Allow","GET, POST");jsonError(w,405,"method_not_allowed","GET or POST required")
	}
}
func (s *Server) benchmarkExperimentActionAPI(w http.ResponseWriter,r *http.Request) {
	if !s.requireAdmin(w,r){return};if s.benchmarkExperimentRepo==nil{jsonError(w,503,"research_unavailable","research experiment storage is unavailable");return}
	w.Header().Set("Cache-Control","no-store");path:=strings.Trim(strings.TrimPrefix(r.URL.Path,"/api/admin/research/experiments/"),"/");parts:=strings.Split(path,"/")
	if len(parts)==0||parts[0]==""||len(parts[0])>64{jsonError(w,400,"invalid_request","experiment id is required");return};id:=parts[0]
	if len(parts)==1&&r.Method==http.MethodDelete{
		if err:=s.benchmarkExperimentRepo.Delete(r.Context(),id);err!=nil{
			switch{
			case errors.Is(err,store.ErrBenchmarkExperimentActive):
				jsonError(w,http.StatusConflict,"experiment_active","queued or running research experiments cannot be deleted")
			case errors.Is(err,sql.ErrNoRows):
				jsonError(w,http.StatusNotFound,"not_found","research experiment not found")
			default:
				jsonError(w,http.StatusInternalServerError,"database_error","failed to delete research experiment")
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"success":true})
		return
	}
	if len(parts)==1&&r.Method==http.MethodGet{
		e,err:=s.benchmarkExperimentRepo.Get(r.Context(),id);if errors.Is(err,sql.ErrNoRows){jsonError(w,404,"not_found","research experiment not found");return}
		if err!=nil{jsonError(w,500,"research_read_failed","failed to read research experiment");return};_ = json.NewEncoder(w).Encode(map[string]interface{}{"experiment":benchmarkExperimentView(e)});return
	}
	if len(parts)==2&&parts[1]=="state"&&r.Method==http.MethodPatch{
		var in benchmarkExperimentStateRequest;if err:=decodeResearchJSON(w,r,&in);err!=nil{jsonError(w,400,"invalid_request","invalid experiment state JSON");return}
		report:="";if len(in.Report)>0{report=string(in.Report)}
		if err:=s.benchmarkExperimentRepo.UpdateState(r.Context(),id,in.Status,report,in.ErrorCode,in.ErrorMessage,in.StartedAt,in.FinishedAt);err!=nil{
			if errors.Is(err,sql.ErrNoRows){jsonError(w,404,"not_found","research experiment not found")}else{jsonError(w,400,"invalid_experiment_state",err.Error())};return}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok":true});return
	}
	if len(parts)==2&&parts[1]=="compare"{
		if r.Method!=http.MethodPost{w.Header().Set("Allow",http.MethodPost);jsonError(w,405,"method_not_allowed","research comparison requires POST");return}
		e,err:=s.benchmarkExperimentRepo.Get(r.Context(),id)
		if errors.Is(err,sql.ErrNoRows){jsonError(w,404,"not_found","research experiment not found");return}
		if err!=nil{jsonError(w,500,"research_read_failed","failed to read research experiment");return}
		if e.Status=="draft"||e.Status=="queued"||e.Status=="running"{jsonError(w,409,"experiment_not_finished","finish or stop both experiment groups before comparison");return}
		comparison,err:=s.buildBenchmarkExperimentComparison(r.Context(),e)
		if err!=nil{jsonError(w,500,"research_comparison_failed","failed to build research comparison");return}
		report:=map[string]interface{}{}
		if strings.TrimSpace(e.ReportJSON)!=""{if json.Unmarshal([]byte(e.ReportJSON),&report)!=nil{report=map[string]interface{}{"sampling_report_raw":e.ReportJSON}}}
		if report==nil{report=map[string]interface{}{}}
		report["comparison"]=comparison
		encoded,marshalErr:=json.Marshal(report);if marshalErr!=nil{jsonError(w,500,"research_comparison_failed","failed to encode research comparison");return}
		if err:=s.benchmarkExperimentRepo.UpdateState(r.Context(),id,e.Status,string(encoded),e.ErrorCode,e.ErrorMessage,e.StartedAt,e.FinishedAt);err!=nil{jsonError(w,500,"research_persist_failed","failed to persist research comparison");return}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"comparison":comparison});return
	}
	if len(parts)==2&&parts[1]=="jobs"&&r.Method==http.MethodPost{
		var in benchmarkExperimentJobRequest;if err:=decodeResearchJSON(w,r,&in);err!=nil{jsonError(w,400,"invalid_request","invalid experiment job JSON");return}
		experiment, loadErr := s.benchmarkExperimentRepo.Get(r.Context(), id)
		if loadErr != nil {
			if errors.Is(loadErr, sql.ErrNoRows) { jsonError(w,404,"not_found","research experiment not found") } else { jsonError(w,500,"research_read_failed","failed to read research experiment") }
			return
		}
		if s.benchmarkRepo == nil { jsonError(w,503,"benchmark_store_unavailable","benchmark task storage is unavailable"); return }
		task, taskErr := s.benchmarkRepo.GetTask(r.Context(), in.JobID)
		if taskErr != nil {
			if errors.Is(taskErr, sql.ErrNoRows) { jsonError(w,404,"benchmark_job_not_found","benchmark job not found") } else { jsonError(w,500,"benchmark_read_failed","failed to read benchmark job") }
			return
		}
		if task.Status != "running" && task.Status != "completed" { jsonError(w,400,"benchmark_job_not_comparable","benchmark job is not in a usable state"); return }
		if err := validateBenchmarkExperimentJob(r.Context(), s.benchmarkRepo, experiment, in.Group, task); err != nil {
			jsonError(w,400,"benchmark_job_not_comparable",err.Error()); return
		}
		if err:=s.benchmarkExperimentRepo.AddJob(r.Context(),id,in.Group,in.JobID);err!=nil{
			if errors.Is(err,sql.ErrNoRows){jsonError(w,404,"not_found","research experiment not found")}else{jsonError(w,400,"invalid_experiment_job",err.Error())};return}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok":true});return
	}
	w.Header().Set("Allow","GET, PATCH, POST");jsonError(w,405,"method_not_allowed","unsupported research experiment action")
}
