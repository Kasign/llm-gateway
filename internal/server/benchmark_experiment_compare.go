package server

import (
 "context"
 "encoding/json"
 "errors"
 "math"
 "sort"
 "github.com/whl/llm-gateway/internal/store"
)

const ( benchmarkExperimentMinimumSamples = 5; benchmarkExperimentMaterialDeltaPercent = 5.0 )
type benchmarkExperimentObservedSample struct { JobID string; Metrics map[string]float64 }
type benchmarkExperimentMetricStats struct { Count int `json:"count"`; Min float64 `json:"min"`; Median float64 `json:"median"`; P95 float64 `json:"p95"`; Max float64 `json:"max"` }

func benchmarkExperimentCacheState(t *RequestModelTimings,inputTokens *int64) string {
 if t==nil||t.CacheN==nil||t.PromptN==nil||inputTokens==nil||*inputTokens<=0{return "unknown"}
 cache,prompt,input:=*t.CacheN,*t.PromptN,*inputTokens
 if cache<0||prompt<0{return "inconsistent"}
 if cache==0&&prompt==input{return "cold"}
 if cache>0&&prompt==0&&cache==input{return "full"}
 if cache>0&&prompt>0&&cache+prompt==input{return "partial"}
 return "inconsistent"
}
func benchmarkExperimentTraceabilityOnlyMissing(missing []string) bool {
 if len(missing)==0{return false}
 allowed:=map[string]bool{"model_sha256_unknown":true,"gateway_revision_unknown":true,"gateway_build_cleanliness_unknown":true}
 for _,item:=range missing{if !allowed[item]{return false}}
 return true
}
func benchmarkExperimentPercentile(values []float64,p float64) float64 {
 if len(values)==0{return math.NaN()}
 ordered:=append([]float64(nil),values...);sort.Float64s(ordered)
 index:=int(math.Ceil(p*float64(len(ordered))))-1
 if index<0{index=0};if index>=len(ordered){index=len(ordered)-1}
 return ordered[index]
}
func benchmarkExperimentStats(values []float64) benchmarkExperimentMetricStats {
 ordered:=append([]float64(nil),values...);sort.Float64s(ordered)
 if len(ordered)==0{return benchmarkExperimentMetricStats{}}
 mid:=len(ordered)/2;median:=ordered[mid]
 if len(ordered)%2==0{median=(ordered[mid-1]+ordered[mid])/2}
 return benchmarkExperimentMetricStats{Count:len(ordered),Min:ordered[0],Median:median,P95:benchmarkExperimentPercentile(ordered,.95),Max:ordered[len(ordered)-1]}
}
func benchmarkExperimentMetricConclusion(base,candidate benchmarkExperimentMetricStats,lower bool)(string,float64,float64){
 if base.Count<benchmarkExperimentMinimumSamples||candidate.Count<benchmarkExperimentMinimumSamples||base.Median<=0||base.P95<=0{return "insufficient_data",0,0}
 md:=(candidate.Median-base.Median)/math.Abs(base.Median)*100
 pd:=(candidate.P95-base.P95)/math.Abs(base.P95)*100
 threshold:=benchmarkExperimentMaterialDeltaPercent
 if lower {if md<=-threshold&&pd<=-threshold{return "improved",md,pd};if md>=threshold&&pd>=threshold{return "regressed",md,pd}} else {if md>=threshold&&pd>=threshold{return "improved",md,pd};if md<=-threshold&&pd<=-threshold{return "regressed",md,pd}}
 if math.Abs(md)<threshold&&math.Abs(pd)<threshold{return "no_material_change",md,pd}
 return "inconclusive",md,pd
}
func benchmarkExperimentMetricsForRun(task *store.BenchmarkTask,run store.BenchmarkRun,analysis *RequestAnalysis)(map[string]float64,string){
 metrics:=map[string]float64{}
 if task.LatencyMeasured{metrics["latency_ms"]=float64(task.LatencyMS)}
 if run.TTFTStatus=="measured"&&run.TTFTMS!=nil{metrics["ttft_ms"]=float64(*run.TTFTMS)}
 if run.InferenceLatencyStatus=="measured"&&run.InferenceLatencyMS!=nil{metrics["inference_latency_ms"]=float64(*run.InferenceLatencyMS)}
 var timings *RequestModelTimings
 if analysis!=nil{
  if analysis.Throughput.OutputTokensPerSec!=nil{metrics["output_tokens_per_sec"]=*analysis.Throughput.OutputTokensPerSec}
  timings=analysis.Throughput.ModelTimings
  if timings!=nil{
   if timings.PromptMS!=nil{metrics["prompt_ms"]=*timings.PromptMS}
   if timings.PredictedMS!=nil{metrics["decode_ms"]=*timings.PredictedMS}
   if timings.PromptPerSecond!=nil{metrics["prompt_tokens_per_sec"]=*timings.PromptPerSecond}
   if timings.PredictedPerSecond!=nil{metrics["decode_tokens_per_sec"]=*timings.PredictedPerSecond}
  }
 }
 return metrics,benchmarkExperimentCacheState(timings,run.InputTokens)
}
func (s *Server) buildBenchmarkExperimentComparison(ctx context.Context,e *store.BenchmarkExperiment)(map[string]interface{},error){
 if s.benchmarkRepo==nil{return nil,errors.New("benchmark repository is unavailable")}
 groups:=map[string][]string{}
 var baselineIDs,candidateIDs []string
 if err:=json.Unmarshal([]byte(e.BaselineJobIDsJSON),&baselineIDs);err!=nil{return nil,err}
 if err:=json.Unmarshal([]byte(e.CandidateJobIDsJSON),&candidateIDs);err!=nil{return nil,err}
 groups["baseline"],groups["candidate"]=baselineIDs,candidateIDs
 if len(groups["baseline"])==0||len(groups["candidate"])==0{return map[string]interface{}{"status":"insufficient_data","conclusion":"insufficient_data","comparability_level":"not_comparable","reason":"both baseline and candidate groups require associated Jobs"},nil}
 byCache:=map[string]map[string][]benchmarkExperimentObservedSample{"baseline":{},"candidate":{}}
 excluded:=make([]map[string]string,0);attempted:=map[string]int{"baseline":0,"candidate":0};limited:=false
 for _,group:=range []string{"baseline","candidate"}{
  for _,jobID:=range groups[group]{
   attempted[group]++
   task,err:=s.benchmarkRepo.GetTask(ctx,jobID)
   if err!=nil{excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"benchmark_task_unavailable"});continue}
   if err:=validateBenchmarkExperimentJob(ctx,s.benchmarkRepo,e,group,task);err!=nil{excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"condition_mismatch: "+err.Error()});continue}
   if task.Status!="completed"{excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"task_not_completed"});continue}
   _,conditionStatus,missing:=benchmarkConditionFromConfigJSON(task.ConfigJSON)
   if conditionStatus=="partial"{if !benchmarkExperimentTraceabilityOnlyMissing(missing){excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"condition_incomplete"});continue};limited=true
   }else if conditionStatus!="complete"{excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"condition_unknown"});continue}
   runs,err:=s.benchmarkRepo.ListRuns(ctx,jobID)
   if err!=nil||len(runs)==0{excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"benchmark_run_unavailable"});continue}
   run:=runs[len(runs)-1]
   if run.Status!="completed"{excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"run_not_completed"});continue}
   var analysis *RequestAnalysis
   if s.logs!=nil{if raw,readErr:=s.logs.GetRequestAnalysis(ctx,run.RequestID);readErr==nil&&raw!=""{analysis,_=decodeRequestAnalysis(raw)}}
   metrics,cache:=benchmarkExperimentMetricsForRun(task,run,analysis)
   if cache!="cold"&&cache!="partial"&&cache!="full"{excluded=append(excluded,map[string]string{"job_id":jobID,"group":group,"reason":"cache_state_"+cache});continue}
   byCache[group][cache]=append(byCache[group][cache],benchmarkExperimentObservedSample{JobID:jobID,Metrics:metrics})
  }
 }
 cacheGroups:=map[string]interface{}{}
 directions:=map[string]bool{"latency_ms":true,"inference_latency_ms":true,"ttft_ms":true,"prompt_ms":true,"decode_ms":true,"output_tokens_per_sec":false,"prompt_tokens_per_sec":false,"decode_tokens_per_sec":false}
 conclusions:=make([]string,0)
 for _,cache:=range []string{"cold","partial","full"}{
  baseSamples,candSamples:=byCache["baseline"][cache],byCache["candidate"][cache]
  if len(baseSamples)==0&&len(candSamples)==0{continue}
  bv,cv:=map[string][]float64{},map[string][]float64{}
  for _,sample:=range baseSamples{for k,v:=range sample.Metrics{bv[k]=append(bv[k],v)}}
  for _,sample:=range candSamples{for k,v:=range sample.Metrics{cv[k]=append(cv[k],v)}}
  results:=map[string]interface{}{}
  for key,lower:=range directions{
   bs,cs:=benchmarkExperimentStats(bv[key]),benchmarkExperimentStats(cv[key])
   if bs.Count==0&&cs.Count==0{continue}
   conclusion,md,pd:=benchmarkExperimentMetricConclusion(bs,cs,lower);conclusions=append(conclusions,conclusion)
   direction:="higher_is_better";if lower{direction="lower_is_better"}
   var medianDeltaValue,p95DeltaValue interface{}=md,pd
   if conclusion=="insufficient_data"{medianDeltaValue=nil;p95DeltaValue=nil}
   results[key]=map[string]interface{}{"baseline":bs,"candidate":cs,"median_delta_percent":medianDeltaValue,"p95_delta_percent":p95DeltaValue,"conclusion":conclusion,"direction":direction}
  }
  cacheGroups[cache]=map[string]interface{}{"baseline_sample_count":len(baseSamples),"candidate_sample_count":len(candSamples),"metrics":results}
 }
 improved,regressed,noChange,inconclusive,insufficient:=false,false,false,false,false
 for _,v:=range conclusions{switch v{case "improved":improved=true;case "regressed":regressed=true;case "no_material_change":noChange=true;case "inconclusive":inconclusive=true;case "insufficient_data":insufficient=true}}
 conclusion:="insufficient_data"
 switch{case improved&&regressed:conclusion="inconclusive";case insufficient&&(improved||regressed||noChange):conclusion="inconclusive";case improved:conclusion="improved";case regressed:conclusion="regressed";case inconclusive:conclusion="inconclusive";case noChange&&!insufficient:conclusion="no_material_change"}
 comparability:="strict";if limited||len(excluded)>0{comparability="limited"};if len(cacheGroups)==0{comparability="not_comparable"}
 status:="completed";if len(excluded)>0||conclusion=="insufficient_data"||conclusion=="inconclusive"{status="partial"}
 return map[string]interface{}{"status":status,"conclusion":conclusion,"comparability_level":comparability,"changed_parameter":e.ChangedParameter,"baseline_value":json.RawMessage(e.BaselineValueJSON),"candidate_value":json.RawMessage(e.CandidateValueJSON),"minimum_samples_per_metric":benchmarkExperimentMinimumSamples,"material_delta_threshold_percent":benchmarkExperimentMaterialDeltaPercent,"attempted":attempted,"cache_groups":cacheGroups,"excluded_samples":excluded},nil
}
