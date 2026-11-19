package server

import "strings"

const benchmarkAcceptanceRulesVersion = "1"

// benchmarkAcceptanceCase 描述一个项目内置的工具调用验收用例。
// Prompt 不属于用例身份；同一用例允许按部署模型能力调整提示词，但判定规则保持稳定。
type benchmarkAcceptanceCase struct {
	ID             string
	Title          string
	Description    string
	Scenario       string
	ToolChoiceMode string
	ManualOnly     bool
}

var benchmarkAcceptanceCases = []benchmarkAcceptanceCase{
	{ID: "TM-01", Title: "自然决策并调用", Description: "模型自主判断后发起工具调用，工具成功执行并完成回答。", Scenario: "tool_performance_auto", ToolChoiceMode: "auto"},
	{ID: "TM-02", Title: "自然决策不调用", Description: "面对无需外部信息的问题，模型不调用工具并直接完成回答。", Scenario: "tool_performance_auto", ToolChoiceMode: "auto"},
	{ID: "TM-03", Title: "工具执行失败", Description: "隔离验收环境中模型发起工具调用，工具执行失败或超时。", Scenario: "tool_performance_auto", ToolChoiceMode: "auto", ManualOnly: true},
	{ID: "TM-04", Title: "工具结果回传后生成回答", Description: "工具成功执行，模型收到工具结果后再次生成最终回答。", Scenario: "tool_performance_auto", ToolChoiceMode: "auto"},
	{ID: "TM-05", Title: "强制调用诊断", Description: "强制工具调用模式下，模型按协议调用工具并完成回答。", Scenario: "tool_performance", ToolChoiceMode: "required"},
}

// benchmarkAcceptanceResult 是服务端验收判定结果；ReasonCode 可供 UI 和自动化稳定消费。
type benchmarkAcceptanceResult struct {
	CaseID       string `json:"case_id"`
	Status       string `json:"status"`
	ReasonCode   string `json:"reason_code"`
	Message      string `json:"message"`
	RulesVersion string `json:"rules_version"`
}

// benchmarkAcceptanceCaseByID 返回内置用例定义的副本，避免调用方修改全局目录。
func benchmarkAcceptanceCaseByID(id string) (benchmarkAcceptanceCase, bool) {
	id = strings.ToUpper(strings.TrimSpace(id))
	for _, item := range benchmarkAcceptanceCases {
		if item.ID == id {
			return item, true
		}
	}
	return benchmarkAcceptanceCase{}, false
}

func benchmarkAcceptanceCatalog() []benchmarkAcceptanceCase {
	out := make([]benchmarkAcceptanceCase, len(benchmarkAcceptanceCases))
	copy(out, benchmarkAcceptanceCases)
	return out
}

// evaluateBenchmarkAcceptance 只根据服务端采集的请求级证据与工具指标判定，不读取客户端结论。
// 证据不足统一返回 inconclusive；任务未进入终态则返回 not_run。
func evaluateBenchmarkAcceptance(caseID string, job benchmarkJobSnapshot, metrics benchmarkToolMetrics) benchmarkAcceptanceResult {
	result := benchmarkAcceptanceResult{
		CaseID: strings.ToUpper(strings.TrimSpace(caseID)),
		Status: "inconclusive",
		ReasonCode: "evidence_insufficient",
		Message: "验收证据不足，不能得出通过或失败结论。",
		RulesVersion: benchmarkAcceptanceRulesVersion,
	}
	item, ok := benchmarkAcceptanceCaseByID(caseID)
	if !ok {
		result.ReasonCode = "unknown_case"
		result.Message = "未知的验收用例 ID。"
		return result
	}
	if job.Status == "" || job.Status == "queued" || job.Status == "running" || job.Status == "cancelling" {
		result.Status = "not_run"
		result.ReasonCode = "task_not_finished"
		result.Message = "任务尚未完成，暂不判定。"
		return result
	}
	if job.Status == "interrupted" || job.Status == "cancelled" {
		result.ReasonCode = "task_interrupted"
		result.Message = "任务未正常完成，无法形成有效验收结论。"
		return result
	}
	if job.Scenario != item.Scenario || metrics.ToolChoiceMode != item.ToolChoiceMode {
		result.Status = "fail"
		result.ReasonCode = "scenario_mismatch"
		result.Message = "任务场景或工具选择模式与验收用例不匹配。"
		return result
	}

	switch item.ID {
	case "TM-01", "TM-05":
		if job.Status != "completed" {
			result.Status, result.ReasonCode, result.Message = "fail", "task_not_completed", "任务未成功完成。"
			return result
		}
		if metrics.ToolCallDecision == "not_called" {
			result.Status, result.ReasonCode, result.Message = "fail", "expected_tool_call_missing", "模型没有发起预期的工具调用。"
			return result
		}
		if metrics.ToolCallDecision != "called" || metrics.ToolExecutionStatus != "measured" {
			return result
		}
		if metrics.ToolExecutionAttempts < 1 {
			return result
		}
		if metrics.ToolExecutionSuccesses < 1 {
			result.Status, result.ReasonCode, result.Message = "fail", "tool_execution_not_successful", "工具调用已发起，但没有成功执行。"
			return result
		}
		result.Status, result.ReasonCode, result.Message = "pass", "tool_call_succeeded", "模型发起工具调用，工具执行成功，任务完成。"
	case "TM-02":
		if job.Status != "completed" {
			result.Status, result.ReasonCode, result.Message = "fail", "task_not_completed", "任务未成功完成。"
			return result
		}
		if metrics.ToolCallDecision == "called" {
			result.Status, result.ReasonCode, result.Message = "fail", "unexpected_tool_call", "模型对无需外部信息的问题仍发起了工具调用。"
			return result
		}
		if metrics.ToolCallDecision != "not_called" || job.ToolCallEventCount != 0 {
			return result
		}
		if metrics.ToolExecutionStatus != "not_applicable" || metrics.ModelFinalGenerationStatus != "combined_with_decision" {
			return result
		}
		result.Status, result.ReasonCode, result.Message = "pass", "tool_call_not_needed", "模型未调用工具并直接完成回答。"
	case "TM-03":
		if metrics.ToolCallDecision == "not_called" {
			result.Status, result.ReasonCode, result.Message = "fail", "expected_tool_call_missing", "模型没有发起预期的工具调用。"
			return result
		}
		if metrics.ToolCallDecision != "called" || metrics.ToolExecutionStatus != "measured" {
			return result
		}
		if metrics.ToolExecutionAttempts < 1 {
			return result
		}
		if metrics.ToolExecutionFailures < 1 {
			result.Status, result.ReasonCode, result.Message = "fail", "tool_failure_not_observed", "未观察到工具执行失败或超时。"
			return result
		}
		result.Status, result.ReasonCode, result.Message = "pass", "tool_failure_observed", "模型发起了工具调用，且审计证据确认工具执行失败或超时。"
	case "TM-04":
		if job.Status != "completed" {
			result.Status, result.ReasonCode, result.Message = "fail", "task_not_completed", "任务未成功完成。"
			return result
		}
		if metrics.ToolCallDecision == "not_called" {
			result.Status, result.ReasonCode, result.Message = "fail", "expected_tool_call_missing", "模型没有发起预期的工具调用。"
			return result
		}
		if metrics.ToolCallDecision != "called" || metrics.ToolExecutionStatus != "measured" {
			return result
		}
		if metrics.ToolExecutionAttempts < 1 || metrics.ToolExecutionSuccesses < 1 {
			if metrics.ToolExecutionFailures > 0 {
				result.Status, result.ReasonCode, result.Message = "fail", "tool_execution_not_successful", "工具执行失败，无法验证结果回传后的最终生成。"
			}
			return result
		}
		if metrics.ModelRounds < 2 || metrics.ModelFinalGenerationStatus != "measured" {
			return result
		}
		result.Status, result.ReasonCode, result.Message = "pass", "tool_result_returned", "工具执行成功，模型在后续轮次生成了最终回答。"
	}
	return result
}
