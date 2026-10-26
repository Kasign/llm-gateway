package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/provider"
)

const managedProviderSettingsKey = "remote_providers:web:v1"
var managedProviderIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type managedProvider struct {
	ID string `json:"id"`
	Enabled bool `json:"enabled"`
	BaseURL string `json:"base_url"`
	APIKeyCiphertext string `json:"api_key_ciphertext,omitempty"`
	APIKeyHeader string `json:"api_key_header"`
	DefaultModel string `json:"default_model"`
	ModelMappings map[string]string `json:"model_mappings,omitempty"`
	AllowInsecureHTTP bool `json:"allow_insecure_http"`
	AllowPrivateNetwork bool `json:"allow_private_network"`
	MaxResponseBytes int64 `json:"max_response_bytes,omitempty"`
	MaxConcurrentRequests int `json:"max_concurrent_requests,omitempty"`
	RequestTimeoutSeconds int `json:"request_timeout_seconds"`
	ConcurrencyLimiter *provider.ConcurrencyLimiter `json:"-"`
	APIKey string `json:"-"`
	SourceConflict bool `json:"-"`
	SecretUnavailable bool `json:"-"`
}

type providerMutationRequest struct {
	ID string `json:"id"`
	Enabled bool `json:"enabled"`
	BaseURL string `json:"base_url"`
	APIKey string `json:"api_key"`
	APIKeyHeader string `json:"api_key_header"`
	DefaultModel string `json:"default_model"`
	ModelMappings map[string]string `json:"model_mappings"`
	AllowInsecureHTTP bool `json:"allow_insecure_http"`
	AllowPrivateNetwork bool `json:"allow_private_network"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
	MaxConcurrentRequests int `json:"max_concurrent_requests"`
	RequestTimeoutSeconds int `json:"request_timeout_seconds"`
}

type providerManagementError struct { status int; code string; message string }
func (e *providerManagementError) Error() string { return e.message }
func providerErr(status int, code, message string) error { return &providerManagementError{status:status,code:code,message:message} }
func writeProviderManagementError(w http.ResponseWriter, err error) {
	var e *providerManagementError
	if errors.As(err,&e) { jsonError(w,e.status,e.code,e.message); return }
	jsonError(w,http.StatusInternalServerError,"provider_management_error","Provider 操作失败")
}

func loadProviderEncryptionKey() ([]byte,error) {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_PROVIDER_ENCRYPTION_KEY"))
	if raw == "" { return nil,nil }
	key,err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 { return nil,errors.New("invalid Provider encryption key configuration") }
	return key,nil
}

func encryptProviderAPIKey(key []byte, plaintext string) (string,error) {
	if plaintext == "" { return "",nil }
	if len(key) != 32 { return "",errors.New("Provider encryption key is unavailable") }
	block,err := aes.NewCipher(key); if err != nil { return "",errors.New("Provider encryption is unavailable") }
	aead,err := cipher.NewGCM(block); if err != nil { return "",errors.New("Provider encryption is unavailable") }
	nonce := make([]byte,aead.NonceSize())
	if _,err := rand.Read(nonce); err != nil { return "",errors.New("Provider encryption could not generate a nonce") }
	sealed := aead.Seal(nonce,nonce,[]byte(plaintext),[]byte("llm-gateway/provider-key/v1"))
	return "v1:"+base64.StdEncoding.EncodeToString(sealed),nil
}

func decryptProviderAPIKey(key []byte,ciphertext string) (string,error) {
	if strings.TrimSpace(ciphertext)=="" { return "",nil }
	if len(key)!=32 { return "",errors.New("Provider encryption key is unavailable") }
	if !strings.HasPrefix(ciphertext,"v1:") { return "",errors.New("unsupported encrypted Provider key version") }
	sealed,err:=base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext,"v1:"))
	if err != nil { return "",errors.New("encrypted Provider key is invalid") }
	block,err:=aes.NewCipher(key); if err != nil { return "",errors.New("Provider decryption is unavailable") }
	aead,err:=cipher.NewGCM(block); if err != nil { return "",errors.New("Provider decryption is unavailable") }
	if len(sealed)<aead.NonceSize() { return "",errors.New("encrypted Provider key is truncated") }
	plain,err:=aead.Open(nil,sealed[:aead.NonceSize()],sealed[aead.NonceSize():],[]byte("llm-gateway/provider-key/v1"))
	if err != nil { return "",errors.New("Provider key could not be decrypted") }
	return string(plain),nil
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src)==0 { return nil }
	out:=make(map[string]string,len(src)); for k,v:=range src {out[k]=v}; return out
}

func (s *Server) initializeProviderEncryption() {
	key,err:=loadProviderEncryptionKey()
	s.providerEncryptionKey,s.providerEncryptionKeyErr=key,err
	if err!=nil { applog.Printf("Provider key encryption unavailable: invalid key configuration") }
}
func (s *Server) encryptionAvailable() bool { return s!=nil && s.providerEncryptionKeyErr==nil && len(s.providerEncryptionKey)==32 }
func (s *Server) deploymentProviderID(id string) bool {
	if s==nil||s.cfg==nil{return false}
	for _,item:=range s.cfg.Providers.OpenAICompatible {if strings.EqualFold(item.ID,id){return true}}
	return false
}

func (s *Server) loadWebProviders(ctx context.Context) {
	s.initializeProviderEncryption()
	if s.webProviders==nil {s.webProviders=make(map[string]managedProvider)}
	var rows []managedProvider
	found,err:=s.settings.GetJSON(ctx,managedProviderSettingsKey,&rows)
	if err!=nil {applog.Printf("load Web Provider settings failed: %v",err);return}
	if !found{return}
	seen:=make(map[string]bool)
	for _,row:=range rows {
		row.ID=strings.ToLower(strings.TrimSpace(row.ID))
		if !managedProviderIDPattern.MatchString(row.ID)||seen[row.ID] {applog.Printf("ignore invalid or duplicate Web Provider record");continue}
		seen[row.ID]=true; row.ModelMappings=cloneStringMap(row.ModelMappings)
		if row.APIKeyCiphertext!="" {
			plain,e:=decryptProviderAPIKey(s.providerEncryptionKey,row.APIKeyCiphertext)
			if e!=nil {row.SecretUnavailable=true} else {row.APIKey=plain}
		}
		row.SourceConflict=s.deploymentProviderID(row.ID)
		if row.SourceConflict {applog.Printf("Web Provider ID conflicts with deployment config; refusing override: id=%s",row.ID);s.webProviders[row.ID]=row;continue}
		if row.Enabled && !row.SecretUnavailable {
			remote, buildErr := s.buildRemoteProvider(row)
			if buildErr != nil {
				applog.Printf("stored Web Provider failed validation; not registered: id=%s", row.ID)
				row.SecretUnavailable = true
			} else {
				row.ConcurrencyLimiter = remote.ConcurrencyLimiter()
				if s.providers == nil {
					row.SourceConflict = true
				} else if registerErr := s.providers.Register(row.ID, func() provider.Provider { return remote }); registerErr != nil {
					applog.Printf("stored Web Provider registration failed: id=%s", row.ID)
					row.SourceConflict = true
				}
			}
		}
		s.webProviders[row.ID]=row
	}
}

func (s *Server) buildRemoteProvider(row managedProvider) (*provider.OpenAICompatibleProvider,error) {
	if row.SecretUnavailable {return nil,errors.New("stored Provider credential is unavailable")}
	if strings.TrimSpace(row.DefaultModel)=="" {return nil,errors.New("Provider default model is required")}
	return provider.NewOpenAICompatibleProvider(provider.OpenAICompatibleConfig{
		ID:row.ID,BaseURL:row.BaseURL,APIKey:row.APIKey,APIKeyHeader:row.APIKeyHeader,
		AllowInsecureHTTP:row.AllowInsecureHTTP,AllowPrivateNetwork:row.AllowPrivateNetwork,
		MaxResponseBytes:row.MaxResponseBytes,MaxConcurrentRequests:row.MaxConcurrentRequests,
		RequestTimeoutSeconds:row.RequestTimeoutSeconds,
		RequestLimiter:row.ConcurrencyLimiter,
	})
}

func providerRecords(items map[string]managedProvider) []managedProvider {
	ids:=make([]string,0,len(items));for id:=range items{ids=append(ids,id)};sort.Strings(ids)
	out:=make([]managedProvider,0,len(ids))
	for _,id:=range ids {row:=items[id];row.APIKey="";row.SourceConflict=false;row.SecretUnavailable=false;out=append(out,row)}
	return out
}
func (s *Server) persistWebProviderMap(ctx context.Context,items map[string]managedProvider) error {
	return s.settings.SetJSON(ctx,managedProviderSettingsKey,providerRecords(items))
}
func cloneManagedProviderMap(src map[string]managedProvider) map[string]managedProvider {
	out:=make(map[string]managedProvider,len(src));for id,row:=range src{out[id]=row};return out
}

func (s *Server) validateProviderInput(in providerMutationRequest,old *managedProvider,routeID string)(managedProvider,error) {
	id:=strings.ToLower(strings.TrimSpace(in.ID));if id==""{id=strings.ToLower(strings.TrimSpace(routeID))}
	if !managedProviderIDPattern.MatchString(id)||id=="llamacpp"||id=="ollama" {return managedProvider{},providerErr(400,"invalid_provider_id","Provider ID 格式无效或与内置 Provider 冲突")}
	if routeID!=""&&id!=strings.ToLower(strings.TrimSpace(routeID)){return managedProvider{},providerErr(400,"provider_id_immutable","Provider ID 不允许通过编辑修改")}
	row:=managedProvider{ID:id,Enabled:in.Enabled,BaseURL:strings.TrimRight(strings.TrimSpace(in.BaseURL),"/"),APIKeyHeader:strings.ToLower(strings.TrimSpace(in.APIKeyHeader)),DefaultModel:strings.TrimSpace(in.DefaultModel),ModelMappings:cloneStringMap(in.ModelMappings),AllowInsecureHTTP:in.AllowInsecureHTTP,AllowPrivateNetwork:in.AllowPrivateNetwork,MaxResponseBytes:in.MaxResponseBytes,MaxConcurrentRequests:in.MaxConcurrentRequests,RequestTimeoutSeconds:in.RequestTimeoutSeconds}
	if row.APIKeyHeader==""{row.APIKeyHeader="authorization"}
	if row.APIKeyHeader!="authorization"&&row.APIKeyHeader!="api-key"&&row.APIKeyHeader!="x-api-key" {return managedProvider{},providerErr(400,"invalid_api_key_header","API Key Header 仅支持 Authorization、api-key 或 x-api-key")}
	if row.DefaultModel==""||len(row.DefaultModel)>255||strings.ContainsAny(row.DefaultModel,"\r\n"){return managedProvider{},providerErr(400,"invalid_default_model","默认模型 ID 必须是非空单行字符串")}
	if len(row.ModelMappings)>128{return managedProvider{},providerErr(400,"invalid_model_mappings","模型映射最多支持 128 项")}
	mappings:=make(map[string]string,len(row.ModelMappings))
	for alias,target:=range row.ModelMappings {
		alias=strings.TrimSpace(alias);target=strings.TrimSpace(target)
		if alias==""||target==""||len(alias)>255||len(target)>255||strings.ContainsAny(alias+target,"\r\n"){return managedProvider{},providerErr(400,"invalid_model_mappings","模型映射包含无效模型 ID")}
		mappings[alias]=target
	}
	row.ModelMappings=mappings
	if row.MaxConcurrentRequests<0||row.MaxConcurrentRequests>provider.MaxOpenAICompatibleConcurrentRequests{return managedProvider{},providerErr(400,"invalid_concurrency_limit","并发上限必须在 0 到 64 之间")};if row.MaxResponseBytes<0{return managedProvider{},providerErr(400,"invalid_response_limit","最大响应大小不能为负数")};if row.RequestTimeoutSeconds<0||row.RequestTimeoutSeconds>provider.MaxOpenAICompatibleRequestTimeoutSeconds{return managedProvider{},providerErr(400,"invalid_request_timeout","整体请求超时必须在 0 到 86400 秒之间")}
	if old!=nil {row.APIKeyCiphertext=old.APIKeyCiphertext;row.APIKey=old.APIKey;row.SecretUnavailable=old.SecretUnavailable;row.ConcurrencyLimiter=old.ConcurrencyLimiter}
	if strings.TrimSpace(in.APIKey)!="" {
		ciphertext,err:=encryptProviderAPIKey(s.providerEncryptionKey,strings.TrimSpace(in.APIKey))
		if err!=nil{return managedProvider{},providerErr(503,"provider_encryption_unavailable","尚未配置有效的 Provider 密钥加密密钥，无法保存 API Key")}
		row.APIKeyCiphertext,row.APIKey,row.SecretUnavailable=ciphertext,strings.TrimSpace(in.APIKey),false
	} else if old!=nil&&old.APIKeyCiphertext!=""&&old.SecretUnavailable {
		return managedProvider{},providerErr(409,"provider_key_unavailable","已保存的 API Key 无法解密；请重新填写 Key，或先执行显式清除")
	}
	if _,err:=s.buildRemoteProvider(row);err!=nil{return managedProvider{},providerErr(400,"invalid_provider_config","Provider 配置校验失败，请检查 HTTPS、URL、私网访问策略和请求参数")}
	return row,nil
}

func (s *Server) safeProviderView(row managedProvider,source string,editable bool) map[string]interface{} {
	configured:=row.APIKey!=""||row.APIKeyCiphertext!=""
	v:=map[string]interface{}{"id":row.ID,"enabled":row.Enabled,"base_url":row.BaseURL,"api_key_header":row.APIKeyHeader,"api_key_configured":configured,"api_key_masked":"","default_model":row.DefaultModel,"model_mappings":cloneStringMap(row.ModelMappings),"allow_insecure_http":row.AllowInsecureHTTP,"allow_private_network":row.AllowPrivateNetwork,"max_response_bytes":row.MaxResponseBytes,"max_concurrent_requests":row.MaxConcurrentRequests,"request_timeout_seconds":row.RequestTimeoutSeconds,"source":source,"editable":editable}
	if configured{v["api_key_masked"]="••••••••"}
	if row.SourceConflict{v["source_conflict"]=true;v["enabled"]=false;v["warning"]="ID 与部署配置冲突，已拒绝覆盖"}
	if row.SecretUnavailable{v["secret_unavailable"]=true;v["warning"]="密文无法解密；请重新填写 API Key 或显式清除"}
	return v
}
func (s *Server) listManagedProviderViews() []map[string]interface{} {
	out:=make([]map[string]interface{},0)
	if s!=nil&&s.cfg!=nil {
		for _,r:=range s.cfg.Providers.OpenAICompatible {
			row:=managedProvider{ID:r.ID,Enabled:r.Enabled,BaseURL:r.BaseURL,APIKey:r.APIKey,APIKeyHeader:r.APIKeyHeader,DefaultModel:r.DefaultModel,ModelMappings:cloneStringMap(r.ModelMappings),AllowInsecureHTTP:r.AllowInsecureHTTP,AllowPrivateNetwork:r.AllowPrivateNetwork,MaxResponseBytes:r.MaxResponseBytes,MaxConcurrentRequests:r.MaxConcurrentRequests,RequestTimeoutSeconds:r.RequestTimeoutSeconds}
			out=append(out,s.safeProviderView(row,"deployment",false))
		}
	}
	s.providerConfigMu.RLock();rows:=make([]managedProvider,0,len(s.webProviders));for _,r:=range s.webProviders{rows=append(rows,r)};s.providerConfigMu.RUnlock()
	sort.Slice(rows,func(i,j int)bool{return rows[i].ID<rows[j].ID})
	for _,r:=range rows{out=append(out,s.safeProviderView(r,"web",!r.SourceConflict))}
	return out
}

func (s *Server) adminProvidersAPI(w http.ResponseWriter,r *http.Request) {
	if !s.adminAuthorized(r.Context(),r){jsonError(w,401,"unauthorized","需要管理员登录");return}
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data":s.listManagedProviderViews(),"encryption_available":s.encryptionAvailable()})
	case http.MethodPost:
		var in providerMutationRequest
		if err:=decodeProviderMutation(r,&in);err!=nil{writeProviderManagementError(w,err);return}
		if strings.TrimSpace(in.ID)==""{jsonError(w,400,"invalid_provider_id","Provider ID 为必填项");return}
		if err:=s.changeWebProvider(r.Context(),in.ID,&in,"save");err!=nil{writeProviderManagementError(w,err);return}
		w.WriteHeader(http.StatusCreated);_ = json.NewEncoder(w).Encode(map[string]interface{}{"data":s.listManagedProviderViews()})
	default:jsonError(w,405,"method_not_allowed","仅支持 GET 或 POST")
	}
}
func decodeProviderMutation(r *http.Request,in *providerMutationRequest)error {
	raw,err:=io.ReadAll(io.LimitReader(r.Body,128*1024+1))
	if err!=nil||len(raw)>128*1024{return providerErr(400,"invalid_request","请求正文无效或过大")}
	if len(bytes.TrimSpace(raw))==0{return nil}
	if json.Unmarshal(raw,in)!=nil{return providerErr(400,"invalid_request","请求 JSON 格式无效")}
	return nil
}

func (s *Server) adminProviderActionAPI(w http.ResponseWriter,r *http.Request) {
	if !s.adminAuthorized(r.Context(),r){jsonError(w,401,"unauthorized","需要管理员登录");return}
	parts:=strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path,"/api/admin/providers/"),"/"),"/")
	if len(parts)<1||parts[0]==""||!managedProviderIDPattern.MatchString(strings.ToLower(parts[0])){jsonError(w,404,"provider_not_found","Provider 不存在");return}
	id:=strings.ToLower(parts[0]);action:=""
	if len(parts)>1{if len(parts)>2{jsonError(w,404,"not_found","接口不存在");return};action=parts[1]}
	if action=="test"{if r.Method!=http.MethodPost{jsonError(w,405,"method_not_allowed","POST required");return};s.testProviderAPI(w,r,id);return}
	if r.Method==http.MethodPut&&action=="" {
		var in providerMutationRequest;if err:=decodeProviderMutation(r,&in);err!=nil{writeProviderManagementError(w,err);return}
		if err:=s.changeWebProvider(r.Context(),id,&in,"save");err!=nil{writeProviderManagementError(w,err);return}
		_=json.NewEncoder(w).Encode(map[string]interface{}{"data":s.listManagedProviderViews()});return
	}
	if r.Method==http.MethodDelete&&action=="" {
		if err:=s.changeWebProvider(r.Context(),id,nil,"delete");err!=nil{writeProviderManagementError(w,err);return}
		w.WriteHeader(http.StatusNoContent);return
	}
	if r.Method==http.MethodPost {
		switch action {
		case "enable","disable","clear-key":
			if err:=s.changeWebProvider(r.Context(),id,nil,action);err!=nil{writeProviderManagementError(w,err);return}
			_=json.NewEncoder(w).Encode(map[string]interface{}{"data":s.listManagedProviderViews()});return
		default:jsonError(w,404,"not_found","接口不存在");return
		}
	}
	jsonError(w,405,"method_not_allowed","请求方法不受支持")
}

func blocksActiveProviderMutation(action, activeID, providerID string, exists, requestedEnabled bool) bool {
	if activeID == "" || !strings.EqualFold(strings.TrimSpace(activeID), strings.TrimSpace(providerID)) {
		return false
	}
	switch action {
	case "delete", "disable", "clear-key":
		return true
	case "save":
		// 编辑表单同样不能通过启用状态复选框绕过活动 Provider 的保护。
		return exists && !requestedEnabled
	default:
		return false
	}
}


// prepareProviderConcurrencyLimit 约束并发上限的生效时点。
// 降低上限立即应用；提高上限必须等 Registry 替换/注册成功，且不能在编辑禁用 Provider 时提前放宽。
func prepareProviderConcurrencyLimit(action string, exists, enabled bool, limiter *provider.ConcurrencyLimiter, configuredLimit int) (previousLimit int, changedBeforeRegistry, raiseAfterRegistry bool, err error) {
	if limiter == nil {
		return 0, false, false, nil
	}
	if action != "enable" && !(action == "save" && exists) {
		return limiter.Limit(), false, false, nil
	}
	current := limiter.Limit()
	target := configuredLimit
	if target == 0 {
		target = provider.MaxOpenAICompatibleConcurrentRequests
	}
	if target < current {
		if err := limiter.SetLimit(configuredLimit); err != nil {
			return current, false, false, err
		}
		return current, true, false, nil
	}
	if target > current && (action == "enable" || enabled) {
		return current, false, true, nil
	}
	return current, false, false, nil
}

func (s *Server) changeWebProvider(ctx context.Context,id string,input *providerMutationRequest,action string)error {
	id=strings.ToLower(strings.TrimSpace(id))
	// 与上游切换共用锁，避免检查非活动后又被并发切换为活动 Provider。
	s.upstreamChangeMu.Lock();defer s.upstreamChangeMu.Unlock()
	s.providerConfigMu.Lock();defer s.providerConfigMu.Unlock()
	if s.deploymentProviderID(id){return providerErr(409,"provider_source_conflict","该 ID 由部署配置托管，Web 端只读且禁止覆盖")}
	old,exists:=s.webProviders[id]
	if !exists&&action!="save"{return providerErr(404,"provider_not_found","Web Provider 不存在")}
	active:=strings.ToLower(strings.TrimSpace(s.effectiveUpstream().Backend))
	if action=="save" && !exists && active==id { return providerErr(409,"provider_would_activate","当前上游配置仍指向该 ID；请先切换到其他后端，再创建同 ID Provider，避免新建即激活") }
	requestedEnabled := input != nil && input.Enabled
if blocksActiveProviderMutation(action, active, id, exists, requestedEnabled) {
		return providerErr(409, "active_provider", "请先切换到其他活动 Provider，再停用、清除 Key 或删除当前 Provider；编辑保存也不能禁用当前活动 Provider")
	}
	previous:=cloneManagedProviderMap(s.webProviders);next:=cloneManagedProviderMap(s.webProviders);var row managedProvider
	switch action {
	case "delete":delete(next,id)
	case "disable":row=old;row.Enabled=false;next[id]=row
	case "enable":row=old;row.Enabled=true;next[id]=row
	case "clear-key":row=old;row.APIKey="";row.APIKeyCiphertext="";row.SecretUnavailable=false;next[id]=row
	case "save":
		if input==nil{return providerErr(400,"invalid_request","Provider 配置不能为空")}
		if strings.TrimSpace(input.ID)!=""&&!strings.EqualFold(strings.TrimSpace(input.ID),id){return providerErr(400,"provider_id_immutable","Provider ID 与请求路径不一致")}
		input.ID=id;var oldPtr *managedProvider;if exists{oldPtr=&old}
		var err error;row,err=s.validateProviderInput(*input,oldPtr,id);if err!=nil{return err};next[id]=row
	default:return providerErr(400,"invalid_action","不支持的 Provider 操作")
	}
	if (action=="enable"||action=="clear-key")&&row.SecretUnavailable{return providerErr(409,"provider_key_unavailable","Provider Key 无法解密；请重新填写 Key 或执行显式清除")}
	if action=="enable"||action=="clear-key"{if _,err:=s.buildRemoteProvider(row);err!=nil{return providerErr(400,"invalid_provider_config","Provider 配置校验失败")}}
	var runtimeProvider *provider.OpenAICompatibleProvider
	if action != "delete" && row.Enabled {
		var err error
		runtimeProvider, err = s.buildRemoteProvider(row)
		if err != nil {
			return providerErr(400, "invalid_provider_config", "Provider 配置校验失败；请确认 Key 可解密并检查安全配置")
		}
		if row.ConcurrencyLimiter == nil {
			row.ConcurrencyLimiter = runtimeProvider.ConcurrencyLimiter()
			next[id] = row
		}
	}
	if err := s.persistWebProviderMap(ctx, next); err != nil {
		applog.Printf("persist Web Provider settings failed: id=%s", id)
		return providerErr(503, "provider_persistence_failed", "保存 Provider 配置失败，运行时配置未更新")
	}

	// 在途请求与替换后的实例共享同一个许可器。
	// 降低上限时先收紧预算，避免旧实例在 Registry 替换前继续接收超额请求；
	// 提高上限则推迟到 Registry 成功替换之后，避免替换失败时短暂放宽旧预算。
	previousLimit, limiterChanged, limiterRaisePending, limitErr := prepareProviderConcurrencyLimit(
		action, exists, row.Enabled, row.ConcurrencyLimiter, row.MaxConcurrentRequests,
	)
	if limitErr != nil {
		if rollbackErr := s.persistWebProviderMap(ctx, previous); rollbackErr != nil {
			applog.Printf("Web Provider persistence rollback failed: id=%s", id)
		}
		return providerErr(400, "invalid_concurrency_limit", "并发上限必须在 0 到 64 之间")
	}
	var registryErr error
	if s.providers==nil{registryErr=errors.New("Provider Registry unavailable")}else{
		switch {
		case action=="delete":if old.Enabled&&!old.SecretUnavailable{registryErr=s.providers.Unregister(id)}
		case !row.Enabled:if exists&&old.Enabled&&!old.SecretUnavailable{registryErr=s.providers.Unregister(id)}
		case exists&&old.Enabled&&!old.SecretUnavailable:registryErr=s.providers.Replace(id,func()provider.Provider{return runtimeProvider})
		case row.Enabled:registryErr=s.providers.Register(id,func()provider.Provider{return runtimeProvider})
		}
	}
	if registryErr != nil {
		if limiterChanged {
			if err := row.ConcurrencyLimiter.SetLimit(previousLimit); err != nil {
				applog.Printf("Web Provider concurrency limit rollback failed: id=%s", id)
			}
		}
		if e := s.persistWebProviderMap(ctx, previous); e != nil {
			applog.Printf("Web Provider persistence rollback failed: id=%s", id)
		}
		applog.Printf("Web Provider Registry update failed: id=%s", id)
		return providerErr(503, "provider_registry_update_failed", "更新 Provider 运行时注册表失败，已尝试回滚持久化配置")
	}
	if limiterRaisePending {
		// 目标上限已由配置与 Provider 构造器验证过；只有 Registry 更新成功后才放宽预算。
		if err := row.ConcurrencyLimiter.SetLimit(row.MaxConcurrentRequests); err != nil {
			applog.Printf("apply Provider concurrency limit failed: id=%s", id)
			return providerErr(503, "provider_concurrency_update_failed", "更新 Provider 并发预算失败；当前运行时保留旧上限")
		}
	}
	s.webProviders=next
	if exists && action!="delete" && row.Enabled && active==id {
		s.upstreamMu.Lock()
		if strings.EqualFold(s.upstream.Backend,id){s.upstream.BaseURL=row.BaseURL;if strings.TrimSpace(s.upstream.ModelName)==""{s.upstream.ModelName=row.DefaultModel}}
		s.upstreamMu.Unlock()
	}
	return nil
}

func (s *Server) testProviderAPI(w http.ResponseWriter,r *http.Request,id string) {
	var in providerMutationRequest
	if err:=decodeProviderMutation(r,&in);err!=nil{writeProviderManagementError(w,err);return}
	var cfg config.OpenAICompatibleProviderConfig
	if in.BaseURL!="" {
		in.ID=id;key:=strings.TrimSpace(in.APIKey)
		if key=="" {s.providerConfigMu.RLock();if row,ok:=s.webProviders[id];ok{key=row.APIKey};s.providerConfigMu.RUnlock()}
		in.APIKey=""
		row,err:=s.validateProviderInput(in,nil,id);if err!=nil{writeProviderManagementError(w,err);return};row.APIKey=key
		cfg=config.OpenAICompatibleProviderConfig{ID:row.ID,Enabled:true,BaseURL:row.BaseURL,APIKey:row.APIKey,APIKeyHeader:row.APIKeyHeader,DefaultModel:row.DefaultModel,ModelMappings:cloneStringMap(row.ModelMappings),AllowInsecureHTTP:row.AllowInsecureHTTP,AllowPrivateNetwork:row.AllowPrivateNetwork,MaxResponseBytes:row.MaxResponseBytes,MaxConcurrentRequests:row.MaxConcurrentRequests,RequestTimeoutSeconds:row.RequestTimeoutSeconds}
	} else {
		if r.URL.Query().Get("source") == "deployment" {
			for _, item := range s.cfg.Providers.OpenAICompatible {
				if strings.EqualFold(item.ID, id) { cfg = item; break }
			}
		} else {
			// 连接测试也允许测试已停用的 Web Provider；只读取运行时内存，不回显 Key。
			s.providerConfigMu.RLock()
			row, ok := s.webProviders[id]
			s.providerConfigMu.RUnlock()
			if ok && !row.SourceConflict && !row.SecretUnavailable {
				cfg = config.OpenAICompatibleProviderConfig{
					ID: row.ID, Enabled: row.Enabled, BaseURL: row.BaseURL, APIKey: row.APIKey,
					APIKeyHeader: row.APIKeyHeader, DefaultModel: row.DefaultModel,
					ModelMappings: cloneStringMap(row.ModelMappings),
					AllowInsecureHTTP: row.AllowInsecureHTTP, AllowPrivateNetwork: row.AllowPrivateNetwork,
					MaxResponseBytes: row.MaxResponseBytes, MaxConcurrentRequests: row.MaxConcurrentRequests,
					RequestTimeoutSeconds: row.RequestTimeoutSeconds,
				}
			}
		}
		if cfg.ID == "" {
			jsonError(w,404,"provider_not_found","Provider 不存在或不可用")
			return
		}
	}
	remote,err:=provider.NewOpenAICompatibleProvider(provider.OpenAICompatibleConfig{ID:cfg.ID,BaseURL:cfg.BaseURL,APIKey:cfg.APIKey,APIKeyHeader:cfg.APIKeyHeader,AllowInsecureHTTP:cfg.AllowInsecureHTTP,AllowPrivateNetwork:cfg.AllowPrivateNetwork,MaxResponseBytes:cfg.MaxResponseBytes,MaxConcurrentRequests:cfg.MaxConcurrentRequests,RequestTimeoutSeconds:cfg.RequestTimeoutSeconds})
	if err!=nil{jsonError(w,400,"invalid_provider_config","Provider 配置校验失败；请检查 HTTPS、URL 与网络访问策略");return}
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second);defer cancel()
	models,err:=remote.TestConnection(ctx)
	if err!=nil{_=json.NewEncoder(w).Encode(map[string]interface{}{"success":false,"error":err.Error(),"models":[]string{}});return}
	_=json.NewEncoder(w).Encode(map[string]interface{}{"success":true,"http_status":200,"models":models})
}
