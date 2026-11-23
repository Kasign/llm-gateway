// Package freecatalog 定义只读免费模型目录的数据契约。
package freecatalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

//go:embed catalog.json
var embeddedFiles embed.FS

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Catalog 是版本化的只读目录快照。
type Catalog struct {
	Version   int        `json:"version"`
	Providers []Provider `json:"providers"`
	Models    []Model    `json:"models"`
	Plans     []Plan     `json:"plans"`
	Evidence  []Evidence `json:"evidence"`
}

// Provider 描述目录中的厂商身份，不包含 API Key 或运行时连接配置。
type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Website string `json:"website"`
	DocsURL string `json:"docs_url,omitempty"`
}

// Model 只描述模型能力，不描述模型是否免费。
type Model struct {
	ProviderID        string   `json:"provider_id"`
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	URL               string   `json:"url,omitempty"`
	Protocols         []string `json:"protocols,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	ContextWindow     *int64   `json:"context_window_tokens,omitempty"`
	MaxOutputTokens   *int64   `json:"max_output_tokens,omitempty"`
	EvidenceIDs       []string `json:"evidence_ids"`
}

// Plan 描述一个厂商免费计划；额度既可以是计划级，也可以细化到具体模型。
type Plan struct {
	ID            string        `json:"id"`
	ProviderID    string        `json:"provider_id"`
	Name          string        `json:"name"`
	Type          PlanType      `json:"type"`
	Status        PlanStatus    `json:"status"`
	Eligibility   Eligibility   `json:"eligibility"`
	DataPolicy    DataPolicy    `json:"data_policy"`
	Quotas        []Quota       `json:"quotas,omitempty"`
	Models        []PlanModel   `json:"models,omitempty"`
	EffectiveFrom *time.Time    `json:"effective_from,omitempty"`
	EffectiveUntil *time.Time   `json:"effective_until,omitempty"`
	VerifiedAt    time.Time     `json:"verified_at"`
	NextReviewAt  time.Time     `json:"next_review_at"`
	EvidenceIDs   []string      `json:"evidence_ids"`
}

// PlanType 区分长期免费层、免费模型变体和一次性试用等不同性质。
type PlanType string

const (
	PlanTypeOngoingTier       PlanType = "ongoing_tier"
	PlanTypeDailyAllocation   PlanType = "daily_allocation"
	PlanTypeFreeModelVariant  PlanType = "free_model_variant"
	PlanTypeTrial             PlanType = "trial"
	PlanTypeSubscriptionCredit PlanType = "subscription_credit"
	PlanTypeOneTimeCredit     PlanType = "one_time_credit"
)

// PlanStatus 是目录中的人工核验状态；时间到期状态由 EffectiveStatus 推导。
type PlanStatus string

const (
	PlanStatusActive        PlanStatus = "active"
	PlanStatusReviewRequired PlanStatus = "review_required"
	PlanStatusChanged       PlanStatus = "changed"
	PlanStatusExpired       PlanStatus = "expired"
	PlanStatusWithdrawn     PlanStatus = "withdrawn"
)

// Eligibility 记录注册、地区和支付资格，不从未知值推断“全球可用”或“无需绑卡”。
type Eligibility struct {
	Regions                 RegionRule  `json:"regions"`
	PaymentMethod           Requirement `json:"payment_method"`
	VerificationRequirements []string   `json:"verification_requirements,omitempty"`
	AccountTypes            []string    `json:"account_types,omitempty"`
	Notes                   string      `json:"notes,omitempty"`
}

// RegionRule.Mode 为 unspecified 时，Codes 必须为空，表示官方范围尚未明确核实。
type RegionRule struct {
	Mode  RegionMode `json:"mode"`
	Codes []string   `json:"codes,omitempty"`
}

type RegionMode string

const (
	RegionModeAllowlist   RegionMode = "allowlist"
	RegionModeDenylist    RegionMode = "denylist"
	RegionModeUnspecified RegionMode = "unspecified"
)

// Requirement 表示是否需要某项账户条件。
type Requirement string

const (
	RequirementRequired    Requirement = "required"
	RequirementNotRequired Requirement = "not_required"
	RequirementConditional Requirement = "conditional"
	RequirementUnknown     Requirement = "unknown"
)

// DataPolicy 描述免费计划对应的数据训练和留存政策。
type DataPolicy struct {
	Training   DataUseState `json:"training"`
	Retention  DataUseState `json:"retention"`
	Notes      string       `json:"notes,omitempty"`
	EvidenceIDs []string    `json:"evidence_ids,omitempty"`
}

type DataUseState string

const (
	DataUseYes         DataUseState = "yes"
	DataUseNo          DataUseState = "no"
	DataUseConditional DataUseState = "conditional"
	DataUseUnknown     DataUseState = "unknown"
)

// PlanModel 将免费计划关联到模型；模型级限额不提升为 Provider 级限额。
type PlanModel struct {
	ModelID     string           `json:"model_id"`
	Availability ModelAvailability `json:"availability"`
	Quotas      []Quota          `json:"quotas,omitempty"`
	EvidenceIDs []string         `json:"evidence_ids"`
}

type ModelAvailability string

const (
	ModelAvailabilityIncluded    ModelAvailability = "included"
	ModelAvailabilityExcluded    ModelAvailability = "excluded"
	ModelAvailabilityRequiresPaid ModelAvailability = "requires_paid"
	ModelAvailabilityUnknown     ModelAvailability = "unknown"
)

// Quota 保留原始单位、周期和作用域。Value=nil 与 State=unknown 不等同于数值 0。
type Quota struct {
	Metric         string     `json:"metric"`
	Value          *float64   `json:"value,omitempty"`
	Unit           string     `json:"unit"`
	Period         string     `json:"period"`
	Scope          string     `json:"scope"`
	State          QuotaState `json:"state"`
	ResetTimezone  string     `json:"reset_timezone,omitempty"`
	EvidenceIDs    []string   `json:"evidence_ids"`
}

type QuotaState string

const (
	QuotaStateKnown         QuotaState = "known"
	QuotaStateUnknown       QuotaState = "unknown"
	QuotaStateUnlimited     QuotaState = "unlimited"
	QuotaStateNotPublished  QuotaState = "not_published"
	QuotaStateNotApplicable QuotaState = "not_applicable"
)

// Evidence 记录官方证据及其支持的字段，便于目录事实逐项追溯。
type Evidence struct {
	ID        string       `json:"id"`
	URL       string       `json:"url"`
	Kind      EvidenceKind `json:"kind"`
	CheckedAt time.Time    `json:"checked_at"`
	Supports  []string     `json:"supports"`
}

type EvidenceKind string

const (
	EvidenceOfficialDocument EvidenceKind = "official_document"
	EvidencePricingPage      EvidenceKind = "pricing_page"
	EvidenceOfficialConsole  EvidenceKind = "official_console"
	EvidenceAnnouncement     EvidenceKind = "announcement"
	EvidenceTerms            EvidenceKind = "terms"
)

// LoadEmbedded 读取随程序发布的只读目录。目录数据不依赖 MariaDB、Redis 或外部网络。
func LoadEmbedded() (Catalog, error) {
	raw, err := embeddedFiles.ReadFile("catalog.json")
	if err != nil {
		return Catalog{}, fmt.Errorf("读取免费模型目录失败: %w", err)
	}
	return Decode(raw)
}

// Decode 严格解析目录 JSON，避免拼写错误字段被静默忽略。
func Decode(raw []byte) (Catalog, error) {
	var catalog Catalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("解析免费模型目录失败: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Catalog{}, fmt.Errorf("免费模型目录包含多余 JSON 内容")
		}
		return Catalog{}, fmt.Errorf("读取免费模型目录尾部失败: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

// Validate 检查引用关系、证据和额度语义；空目录允许通过，供尚未录入官方事实的部署启动。
func (c Catalog) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("免费模型目录版本不支持: %d", c.Version)
	}

	providers := make(map[string]struct{}, len(c.Providers))
	for _, item := range c.Providers {
		if !validSlug(item.ID) || strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("Provider ID 或名称无效: %q", item.ID)
		}
		if _, exists := providers[item.ID]; exists {
			return fmt.Errorf("Provider ID 重复: %s", item.ID)
		}
		if !validHTTPSURL(item.Website) || (item.DocsURL != "" && !validHTTPSURL(item.DocsURL)) {
			return fmt.Errorf("Provider 官方链接必须使用 HTTPS: %s", item.ID)
		}
		providers[item.ID] = struct{}{}
	}

	evidence := make(map[string]struct{}, len(c.Evidence))
	for _, item := range c.Evidence {
		if !validSlug(item.ID) {
			return fmt.Errorf("证据 ID 无效: %q", item.ID)
		}
		if _, exists := evidence[item.ID]; exists {
			return fmt.Errorf("证据 ID 重复: %s", item.ID)
		}
		if !validHTTPSURL(item.URL) || !validEvidenceKind(item.Kind) || item.CheckedAt.IsZero() {
			return fmt.Errorf("证据字段无效: %s", item.ID)
		}
		if err := validateStringList("证据支持字段", item.Supports, true); err != nil {
			return fmt.Errorf("证据 %s: %w", item.ID, err)
		}
		evidence[item.ID] = struct{}{}
	}

	models := make(map[string]struct{}, len(c.Models))
	for _, item := range c.Models {
		if _, exists := providers[item.ProviderID]; !exists {
			return fmt.Errorf("模型引用了未知 Provider: %s/%s", item.ProviderID, item.ID)
		}
		if strings.TrimSpace(item.ID) == "" || strings.ContainsAny(item.ID, " \t\r\n") || strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("模型 ID 或名称无效: %q", item.ID)
		}
		key := item.ProviderID + "\x00" + item.ID
		if _, exists := models[key]; exists {
			return fmt.Errorf("模型重复: %s/%s", item.ProviderID, item.ID)
		}
		if item.URL != "" && !validHTTPSURL(item.URL) {
			return fmt.Errorf("模型官方链接必须使用 HTTPS: %s/%s", item.ProviderID, item.ID)
		}
		if item.ContextWindow != nil && *item.ContextWindow <= 0 {
			return fmt.Errorf("模型上下文窗口必须大于 0: %s", item.ID)
		}
		if item.MaxOutputTokens != nil && *item.MaxOutputTokens <= 0 {
			return fmt.Errorf("模型最大输出 Token 必须大于 0: %s", item.ID)
		}
		if err := validateStringList("模型协议", item.Protocols, false); err != nil {
			return fmt.Errorf("模型 %s: %w", item.ID, err)
		}
		if err := validateStringList("模型能力", item.Capabilities, false); err != nil {
			return fmt.Errorf("模型 %s: %w", item.ID, err)
		}
		if err := validateEvidenceRefs(item.EvidenceIDs, evidence, true); err != nil {
			return fmt.Errorf("模型 %s: %w", item.ID, err)
		}
		models[key] = struct{}{}
	}

	plans := make(map[string]struct{}, len(c.Plans))
	for _, item := range c.Plans {
		if !validSlug(item.ID) {
			return fmt.Errorf("免费计划 ID 无效: %q", item.ID)
		}
		if _, exists := plans[item.ID]; exists {
			return fmt.Errorf("免费计划 ID 重复: %s", item.ID)
		}
		if _, exists := providers[item.ProviderID]; !exists {
			return fmt.Errorf("免费计划引用了未知 Provider: %s", item.ProviderID)
		}
		if strings.TrimSpace(item.Name) == "" || !validPlanType(item.Type) || !validPlanStatus(item.Status) {
			return fmt.Errorf("免费计划字段无效: %s", item.ID)
		}
		if item.VerifiedAt.IsZero() || item.NextReviewAt.IsZero() {
			return fmt.Errorf("免费计划必须记录核验时间和下次复核时间: %s", item.ID)
		}
		if item.EffectiveFrom != nil && item.EffectiveUntil != nil && !item.EffectiveUntil.After(*item.EffectiveFrom) {
			return fmt.Errorf("免费计划有效期结束时间必须晚于开始时间: %s", item.ID)
		}
		if err := validateEligibility(item.Eligibility); err != nil {
			return fmt.Errorf("免费计划 %s: %w", item.ID, err)
		}
		if !validDataUseState(item.DataPolicy.Training) || !validDataUseState(item.DataPolicy.Retention) {
			return fmt.Errorf("免费计划数据政策状态无效: %s", item.ID)
		}
		if item.DataPolicy.Training != DataUseUnknown || item.DataPolicy.Retention != DataUseUnknown {
			if err := validateEvidenceRefs(item.DataPolicy.EvidenceIDs, evidence, true); err != nil {
				return fmt.Errorf("免费计划 %s 数据政策: %w", item.ID, err)
			}
		}
		if err := validateEvidenceRefs(item.EvidenceIDs, evidence, true); err != nil {
			return fmt.Errorf("免费计划 %s: %w", item.ID, err)
		}
		for _, quota := range item.Quotas {
			if err := validateQuota(quota, evidence); err != nil {
				return fmt.Errorf("免费计划 %s 额度: %w", item.ID, err)
			}
		}
		seenModels := make(map[string]struct{}, len(item.Models))
		for _, model := range item.Models {
			if strings.TrimSpace(model.ModelID) == "" || !validModelAvailability(model.Availability) {
				return fmt.Errorf("免费计划 %s 的模型适用项无效", item.ID)
			}
			if _, exists := seenModels[model.ModelID]; exists {
				return fmt.Errorf("免费计划 %s 的模型适用项重复: %s", item.ID, model.ModelID)
			}
			if _, exists := models[item.ProviderID+"\x00"+model.ModelID]; !exists {
				return fmt.Errorf("免费计划 %s 引用了未登记的模型: %s", item.ID, model.ModelID)
			}
			if err := validateEvidenceRefs(model.EvidenceIDs, evidence, true); err != nil {
				return fmt.Errorf("免费计划 %s 模型 %s: %w", item.ID, model.ModelID, err)
			}
			for _, quota := range model.Quotas {
				if err := validateQuota(quota, evidence); err != nil {
					return fmt.Errorf("免费计划 %s 模型 %s 额度: %w", item.ID, model.ModelID, err)
				}
			}
			seenModels[model.ModelID] = struct{}{}
		}
		plans[item.ID] = struct{}{}
	}
	return nil
}

// EffectiveStatus 根据有效期和复核时间计算当前展示状态，不修改原始目录。
func (p Plan) EffectiveStatus(now time.Time) PlanStatus {
	if p.Status == PlanStatusWithdrawn || p.Status == PlanStatusExpired {
		return p.Status
	}
	if p.EffectiveUntil != nil && !p.EffectiveUntil.After(now) {
		return PlanStatusExpired
	}
	if !p.NextReviewAt.IsZero() && !p.NextReviewAt.After(now) {
		return PlanStatusReviewRequired
	}
	return p.Status
}

func validateEligibility(item Eligibility) error {
	switch item.Regions.Mode {
	case RegionModeAllowlist, RegionModeDenylist:
		if len(item.Regions.Codes) == 0 {
			return fmt.Errorf("地区白名单/黑名单不能为空")
		}
	case RegionModeUnspecified:
		if len(item.Regions.Codes) != 0 {
			return fmt.Errorf("未明确地区范围时不得填写地区代码")
		}
	default:
		return fmt.Errorf("地区范围状态无效")
	}
	if !validRequirement(item.PaymentMethod) {
		return fmt.Errorf("付款方式要求状态无效")
	}
	if err := validateStringList("验证要求", item.VerificationRequirements, false); err != nil {
		return err
	}
	return validateStringList("账户类型", item.AccountTypes, false)
}

func validateQuota(item Quota, evidence map[string]struct{}) error {
	if strings.TrimSpace(item.Metric) == "" || strings.TrimSpace(item.Unit) == "" ||
		strings.TrimSpace(item.Period) == "" || strings.TrimSpace(item.Scope) == "" {
		return fmt.Errorf("额度必须明确指标、单位、周期和作用域")
	}
	switch item.State {
	case QuotaStateKnown:
		if item.Value == nil || math.IsNaN(*item.Value) || math.IsInf(*item.Value, 0) || *item.Value < 0 {
			return fmt.Errorf("已知额度必须是非负有限数值")
		}
	case QuotaStateUnknown, QuotaStateUnlimited, QuotaStateNotPublished, QuotaStateNotApplicable:
		if item.Value != nil {
			return fmt.Errorf("未知/无限制/未公布/不适用额度不得携带数值")
		}
	default:
		return fmt.Errorf("额度状态无效: %s", item.State)
	}
	return validateEvidenceRefs(item.EvidenceIDs, evidence, true)
}

func validateEvidenceRefs(ids []string, evidence map[string]struct{}, required bool) error {
	if required && len(ids) == 0 {
		return fmt.Errorf("缺少官方证据引用")
	}
	if err := validateStringList("证据引用", ids, false); err != nil {
		return err
	}
	for _, id := range ids {
		if _, exists := evidence[id]; !exists {
			return fmt.Errorf("引用了不存在的证据: %s", id)
		}
	}
	return nil
}

func validateStringList(label string, values []string, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%s不能为空", label)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s包含空值", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s包含重复值: %s", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validSlug(value string) bool { return slugPattern.MatchString(value) }

func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validPlanType(value PlanType) bool {
	switch value {
	case PlanTypeOngoingTier, PlanTypeDailyAllocation, PlanTypeFreeModelVariant,
		PlanTypeTrial, PlanTypeSubscriptionCredit, PlanTypeOneTimeCredit:
		return true
	default:
		return false
	}
}

func validPlanStatus(value PlanStatus) bool {
	switch value {
	case PlanStatusActive, PlanStatusReviewRequired, PlanStatusChanged, PlanStatusExpired, PlanStatusWithdrawn:
		return true
	default:
		return false
	}
}

func validEvidenceKind(value EvidenceKind) bool {
	switch value {
	case EvidenceOfficialDocument, EvidencePricingPage, EvidenceOfficialConsole, EvidenceAnnouncement, EvidenceTerms:
		return true
	default:
		return false
	}
}

func validRequirement(value Requirement) bool {
	switch value {
	case RequirementRequired, RequirementNotRequired, RequirementConditional, RequirementUnknown:
		return true
	default:
		return false
	}
}

func validDataUseState(value DataUseState) bool {
	switch value {
	case DataUseYes, DataUseNo, DataUseConditional, DataUseUnknown:
		return true
	default:
		return false
	}
}

func validModelAvailability(value ModelAvailability) bool {
	switch value {
	case ModelAvailabilityIncluded, ModelAvailabilityExcluded, ModelAvailabilityRequiresPaid, ModelAvailabilityUnknown:
		return true
	default:
		return false
	}
}
