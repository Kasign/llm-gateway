package server

// 本文件只基于当前请求已采集的证据生成排查方向，不把单次观测伪装成已确认瓶颈。
// 主瓶颈必须由后续同条件 Benchmark 对照或明确 SLA 支持；本阶段不自动调整生产参数。

type PerformanceDiagnosis struct {
	Status            string               `json:"status"`
	PrimaryBottleneck *PerformanceFinding  `json:"primary_bottleneck,omitempty"`
	Candidates        []PerformanceFinding `json:"candidates,omitempty"`
	Limitations       []string             `json:"limitations,omitempty"`
}

type PerformanceFinding struct {
	Category         string                 `json:"category"`
	Status           string                 `json:"status"`
	Title            string                 `json:"title"`
	Summary          string                 `json:"summary"`
	Evidence         []PerformanceEvidence  `json:"evidence"`
	NextSteps        []string               `json:"next_steps,omitempty"`
}

type PerformanceEvidence struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Source string  `json:"source"`
	Scope  string  `json:"scope"`
	Note   string  `json:"note,omitempty"`
}

func buildPerformanceDiagnosis(in analysisInput) *PerformanceDiagnosis {
	out := &PerformanceDiagnosis{
		Status:      "insufficient_data",
		Candidates:  make([]PerformanceFinding, 0),
		Limitations: make([]string, 0, 3),
	}

	if in.PrepMS > 0 || in.ModelLoadMS > 0 {
		out.Candidates = append(out.Candidates, PerformanceFinding{
			Category: "gateway_pre_inference",
			Status:   "candidate",
			Title:    "Gateway 推理前阶段",
			Summary:  "已观察到请求准备或就绪等待耗时；现有 model_load_ms 实际是 PreparedAt 到 ReadyAt 的区间，包含请求级/全局 Guard 等工作，不等同于纯模型加载耗时。",
			Evidence: []PerformanceEvidence{
				{Metric: "prep_ms", Value: float64(in.PrepMS), Unit: "ms", Source: "gateway_pipeline", Scope: "request", Note: "Resolver/Mutator 等请求准备阶段"},
				{Metric: "model_load_ms", Value: float64(in.ModelLoadMS), Unit: "ms", Source: "gateway_pipeline", Scope: "request", Note: "PreparedAt 到 ReadyAt；不可直接解释为模型加载"},
			},
			NextSteps: []string{"用相同请求条件重复测量该阶段，再判断是否稳定偏高", "不要仅凭 model_load_ms 调整模型加载策略"},
		})
	}

	if in.ProviderQueueWaitMS != nil && *in.ProviderQueueWaitMS > 0 {
		switch in.ProviderQueueWaitStatus {
		case "granted", "timeout", "cancelled":
			summary := "本次请求确实等待了 Provider 并发许可；单次正值只能证明发生过排队，不能单独证明它是整体性能主瓶颈。"
			nextSteps := []string{"在受控并发负载下比较队列等待分布", "先确认上游模型吞吐和硬件余量，再决定是否调整并发上限"}
			switch in.ProviderQueueWaitStatus {
			case "timeout":
				summary = "本次请求在 Provider 并发许可队列中等待超时；这是明确的排队失败证据，但仍需结合并发负载判断根因。"
				nextSteps = []string{"检查 Provider 并发上限、队列容量和等待超时配置", "在受控负载下确认是否持续饱和；不要未经验证直接提高并发上限"}
			case "cancelled":
				summary = "本次请求在等待 Provider 并发许可期间被取消；它说明请求在排队阶段结束，不代表 Provider 本身故障。"
				nextSteps = []string{"结合调用方取消时间与队列等待时长判断是否由客户端超时触发", "若持续发生，再在受控并发负载下检查队列容量与等待时间"}
			}
			out.Candidates = append(out.Candidates, PerformanceFinding{
				Category: "provider_queue",
				Status:   "candidate",
				Title:    "Provider 并发许可排队",
				Summary:  summary,
				Evidence: []PerformanceEvidence{
					{Metric: "provider_queue_wait_ms", Value: float64(*in.ProviderQueueWaitMS), Unit: "ms", Source: "provider_admission_callback", Scope: "request", Note: "queue_status=" + in.ProviderQueueWaitStatus},
				},
				NextSteps: nextSteps,
			})
		}
	}

	if mt := in.ModelTimings; mt != nil {
		if mt.PromptMS != nil && *mt.PromptMS >= 0 && mt.PromptN != nil && *mt.PromptN > 0 {
			evidence := []PerformanceEvidence{
				{Metric: "prompt_ms", Value: *mt.PromptMS, Unit: "ms", Source: "llama.cpp_response_timings", Scope: "request"},
				{Metric: "prompt_n", Value: float64(*mt.PromptN), Unit: "tokens", Source: "llama.cpp_response_timings", Scope: "request", Note: "本次实际 Prompt Eval Token，不等同于输入总 Token"},
			}
			if mt.PromptPerSecond != nil && *mt.PromptPerSecond >= 0 {
				evidence = append(evidence, PerformanceEvidence{Metric: "prompt_per_second", Value: *mt.PromptPerSecond, Unit: "tokens/s", Source: "llama.cpp_response_timings", Scope: "request"})
			}
			out.Candidates = append(out.Candidates, PerformanceFinding{
				Category: "prompt_eval",
				Status:   "candidate",
				Title:    "Prompt Eval",
				Summary:  "已取得 llama.cpp 原生 Prompt Eval 计时；是否偏慢必须与相同模型、输入规模和缓存状态的样本比较。",
				Evidence: evidence,
				NextSteps: []string{"按相同模型、Backend、输入 Token 数和缓存分类建立对照", "确认 Prompt Eval 确实偏慢后，再评估上下文裁剪或缓存策略，并检查回答质量"},
			})
		}
		if mt.PredictedMS != nil && *mt.PredictedMS >= 0 && mt.PredictedN != nil && *mt.PredictedN > 0 {
			evidence := []PerformanceEvidence{
				{Metric: "predicted_ms", Value: *mt.PredictedMS, Unit: "ms", Source: "llama.cpp_response_timings", Scope: "request"},
				{Metric: "predicted_n", Value: float64(*mt.PredictedN), Unit: "tokens", Source: "llama.cpp_response_timings", Scope: "request"},
			}
			if mt.PredictedPerSecond != nil && *mt.PredictedPerSecond >= 0 {
				evidence = append(evidence, PerformanceEvidence{Metric: "predicted_per_second", Value: *mt.PredictedPerSecond, Unit: "tokens/s", Source: "llama.cpp_response_timings", Scope: "request"})
			}
			out.Candidates = append(out.Candidates, PerformanceFinding{
				Category: "decode",
				Status:   "candidate",
				Title:    "Decode 生成阶段",
				Summary:  "已取得 llama.cpp 原生 Decode 计时；是否偏慢必须与相同模型、Backend、输出规模和思考模式的样本比较。",
				Evidence: evidence,
				NextSteps: []string{"按相同模型、Backend、输出 Token 数和思考模式建立对照", "确认 Decode 吞吐偏低后，再逐项评估量化模型、线程或 batch 参数，不要一次修改多个变量"},
			})
		}
	}

	if dispatched, okDispatch := requestTimelineOffset(in.Timeline, "upstream_dispatched"); okDispatch {
		if headers, okHeaders := requestTimelineOffset(in.Timeline, "upstream_headers"); okHeaders && headers >= dispatched {
			waitMS := headers - dispatched
			if waitMS > 0 {
				out.Candidates = append(out.Candidates, PerformanceFinding{
					Category: "upstream_headers_wait",
					Status:   "candidate",
					Title:    "上游响应头等待",
					Summary:  "从 Gateway 发出上游请求到收到响应头的时间可由两个真实 Timeline 锚点计算；该区间仍包含连接与上游处理，不能单独归因为模型计算。",
					Evidence: []PerformanceEvidence{
						{Metric: "upstream_dispatch_to_headers_ms", Value: float64(waitMS), Unit: "ms", Source: "gateway_request_timeline", Scope: "request"},
					},
					NextSteps: []string{"与同一请求的 llama.cpp Prompt/Decode timings 对照，区分上游首包等待和模型计算", "检查同一时间窗口是否有并发请求；不要把该区间直接等同于 Prompt Eval"},
				})
			}
		}
	}

	if headers, okHeaders := requestTimelineOffset(in.Timeline, "upstream_headers"); okHeaders {
		if firstByte, okFirstByte := requestTimelineOffset(in.Timeline, "upstream_first_byte"); okFirstByte && firstByte >= headers {
			waitMS := firstByte - headers
			if waitMS > 0 {
				out.Candidates = append(out.Candidates, PerformanceFinding{
					Category: "upstream_body_first_byte_wait",
					Status:   "candidate",
					Title:    "上游响应头到首字节等待",
					Summary:  "已收到上游响应头后，直到读取到首个响应体字节仍存在等待；该区间可能包含上游响应体准备或传输等待，不能直接解释为模型 Decode。",
					Evidence: []PerformanceEvidence{
						{Metric: "upstream_headers_to_first_byte_ms", Value: float64(waitMS), Unit: "ms", Source: "gateway_request_timeline", Scope: "request"},
					},
					NextSteps: []string{"与 llama.cpp 原生 Prompt/Decode timings 对照，确认等待是否发生在模型计时之外", "对比同一 Provider 的其他请求，排查连接、代理和上游响应体首块延迟"},
				})
			}
		}
	}

	if len(out.Candidates) > 0 {
		out.Status = "observed_only"
		out.Limitations = append(out.Limitations, "当前没有关联的同条件优化前基线或明确性能 SLA，因此所有方向仅为候选排查项，不输出 primary_bottleneck。")
	} else {
		out.Limitations = append(out.Limitations, "当前请求缺少可用于性能归因的正向阶段证据；不把缺失字段或真实零值解释为性能正常。")
	}
	if in.RuntimeSnapshot != nil {
		out.Limitations = append(out.Limitations, "Runtime Snapshot 是请求收尾时最近的后台快照，不保证与本次请求执行区间重叠；CPU/内存只作环境上下文，不归因到单请求。")
	}
	out.Limitations = append(out.Limitations, "进程级 Metrics Counter Delta 不会自动分摊给单请求；需要隔离窗口和一致的进程/model scope 才能用于对账。")
	return out
}

func requestTimelineOffset(events []requestTimelineEvent, name string) (int64, bool) {
	for _, event := range events {
		if event.Name == name && event.Status == timelineStatusMeasured && event.OffsetMS != nil {
			return *event.OffsetMS, true
		}
	}
	return 0, false
}
