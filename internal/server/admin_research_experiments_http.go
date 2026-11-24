package server

import (
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
	if len(in.BaselineValue)==0||len(in.CandidateValue)==0||len(in.BaselineCondition)==0||len(in.CandidateCondition)==0{return errors.New("baseline/candidate values and condition snapshots are required")}
	var baselineValue,candidateValue interface{}
	if err:=json.Unmarshal(in.BaselineValue,&baselineValue);err!=nil{return errors.New("baseline value must be valid JSON")}
	if err:=json.Unmarshal(in.CandidateValue,&candidateValue);err!=nil{return errors.New("candidate value must be valid JSON")}
	if reflect.DeepEqual(baselineValue,candidateValue){return errors.New("baseline and candidate values must differ")}
	var baselineCondition,candidateCondition map[string]interface{}
	if err:=json.Unmarshal(in.BaselineCondition,&baselineCondition);err!=nil||baselineCondition==nil{return errors.New("baseline condition must be a JSON object")}
	if err:=json.Unmarshal(in.CandidateCondition,&candidateCondition);err!=nil||candidateCondition==nil{return errors.New("candidate condition must be a JSON object")}
	if !reflect.DeepEqual(baselineCondition,candidateCondition){return errors.New("baseline and candidate conditions must be identical; only changed_parameter may differ")}
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
	if len(parts)==2&&parts[1]=="jobs"&&r.Method==http.MethodPost{
		var in benchmarkExperimentJobRequest;if err:=decodeResearchJSON(w,r,&in);err!=nil{jsonError(w,400,"invalid_request","invalid experiment job JSON");return}
		if err:=s.benchmarkExperimentRepo.AddJob(r.Context(),id,in.Group,in.JobID);err!=nil{
			if errors.Is(err,sql.ErrNoRows){jsonError(w,404,"not_found","research experiment not found")}else{jsonError(w,400,"invalid_experiment_job",err.Error())};return}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok":true});return
	}
	w.Header().Set("Allow","GET, PATCH, POST");jsonError(w,405,"method_not_allowed","unsupported research experiment action")
}
