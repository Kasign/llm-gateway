package settings

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const backendRuntimeProfilesKey = "backend_runtime_profiles"

var ErrBackendRuntimeProfileNotFound = errors.New("backend runtime profile not found")
var runtimeProfileCacheTypePattern = regexp.MustCompile("^[a-zA-Z0-9_]{1,16}$")

// BackendRuntimeParameters 记录用户声明的 llama.cpp Server 启动参数；nil 表示未知/未记录。
type BackendRuntimeParameters struct {
	Threads        *int   `json:"threads,omitempty"`
	ThreadsBatch   *int   `json:"threads_batch,omitempty"`
	BatchSize      *int   `json:"batch_size,omitempty"`
	UBatchSize     *int   `json:"ubatch_size,omitempty"`
	ContextSize    *int   `json:"ctx_size,omitempty"`
	GPULayers      *int   `json:"n_gpu_layers,omitempty"`
	FlashAttention *bool  `json:"flash_attn,omitempty"`
	CacheTypeK     string `json:"cache_type_k,omitempty"`
	CacheTypeV     string `json:"cache_type_v,omitempty"`
}

// BackendRuntimeProfile 是用户声明的实际 Backend 运行配置档案，不会被 Gateway 自动应用到 Backend 进程。
type BackendRuntimeProfile struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Backend    string                   `json:"backend"`
	ModelID    string                   `json:"model_id"`
	Version    int                      `json:"version"`
	Source     string                   `json:"source"`
	Parameters BackendRuntimeParameters `json:"parameters"`
	Notes      string                   `json:"notes,omitempty"`
	UpdatedAt  time.Time                `json:"updated_at"`
}

func (p BackendRuntimeProfile) NormalizeAndValidate() (BackendRuntimeProfile, error) {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Backend = strings.ToLower(strings.TrimSpace(p.Backend))
	p.ModelID = strings.TrimSpace(p.ModelID)
	p.Notes = strings.TrimSpace(p.Notes)
	if p.ID == "" || len(p.ID) > 64 {
		return p, fmt.Errorf("profile id must contain 1 to 64 characters")
	}
	for _, r := range p.ID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return p, fmt.Errorf("profile id contains unsupported characters")
		}
	}
	if p.Name == "" || len(p.Name) > 80 {
		return p, fmt.Errorf("profile name must contain 1 to 80 characters")
	}
	if p.Backend != "llamacpp" {
		return p, fmt.Errorf("only llamacpp runtime profiles are supported")
	}
	if p.ModelID == "" || len(p.ModelID) > 256 {
		return p, fmt.Errorf("model_id must contain 1 to 256 characters")
	}
	if len(p.Notes) > 500 {
		return p, fmt.Errorf("profile notes must not exceed 500 characters")
	}
	if p.Parameters.Threads != nil && (*p.Parameters.Threads < 0 || *p.Parameters.Threads > 256) {
		return p, fmt.Errorf("threads must be between 0 and 256; 0 means backend default")
	}
	if p.Parameters.ThreadsBatch != nil && (*p.Parameters.ThreadsBatch < 0 || *p.Parameters.ThreadsBatch > 256) {
		return p, fmt.Errorf("threads_batch must be between 0 and 256; 0 means backend default")
	}
	if p.Parameters.BatchSize != nil && (*p.Parameters.BatchSize < 1 || *p.Parameters.BatchSize > 65536) {
		return p, fmt.Errorf("batch_size must be between 1 and 65536")
	}
	if p.Parameters.UBatchSize != nil && (*p.Parameters.UBatchSize < 1 || *p.Parameters.UBatchSize > 65536) {
		return p, fmt.Errorf("ubatch_size must be between 1 and 65536")
	}
	if p.Parameters.BatchSize != nil && p.Parameters.UBatchSize != nil && *p.Parameters.UBatchSize > *p.Parameters.BatchSize {
		return p, fmt.Errorf("ubatch_size must not exceed batch_size")
	}
	if p.Parameters.ContextSize != nil && (*p.Parameters.ContextSize < 1 || *p.Parameters.ContextSize > 1048576) {
		return p, fmt.Errorf("ctx_size must be between 1 and 1048576")
	}
	if p.Parameters.GPULayers != nil && (*p.Parameters.GPULayers < -1 || *p.Parameters.GPULayers > 1024) {
		return p, fmt.Errorf("n_gpu_layers must be between -1 and 1024")
	}
	for name, value := range map[string]string{"cache_type_k": p.Parameters.CacheTypeK, "cache_type_v": p.Parameters.CacheTypeV} {
		if value != "" && !runtimeProfileCacheTypePattern.MatchString(value) {
			return p, fmt.Errorf("%s contains unsupported characters or is too long", name)
		}
	}
	if p.Parameters.Threads == nil && p.Parameters.ThreadsBatch == nil &&
		p.Parameters.BatchSize == nil && p.Parameters.UBatchSize == nil &&
		p.Parameters.ContextSize == nil && p.Parameters.GPULayers == nil &&
		p.Parameters.FlashAttention == nil && p.Parameters.CacheTypeK == "" && p.Parameters.CacheTypeV == "" {
		return p, fmt.Errorf("at least one runtime parameter must be recorded")
	}
	p.Source = "user_declared"
	if p.Version < 1 {
		p.Version = 1
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	return p, nil
}

type backendRuntimeProfilesDocument struct {
	Profiles []BackendRuntimeProfile `json:"profiles"`
}

func (r *Repo) loadBackendRuntimeProfiles(ctx context.Context) ([]BackendRuntimeProfile, error) {
	var doc backendRuntimeProfilesDocument
	_, err := r.GetJSON(ctx, backendRuntimeProfilesKey, &doc)
	if err != nil {
		return nil, err
	}
	if doc.Profiles == nil {
		doc.Profiles = []BackendRuntimeProfile{}
	}
	return doc.Profiles, nil
}

func (r *Repo) ListBackendRuntimeProfiles(ctx context.Context, backend, modelID string) ([]BackendRuntimeProfile, error) {
	r.profileMu.Lock()
	defer r.profileMu.Unlock()
	profiles, err := r.loadBackendRuntimeProfiles(ctx)
	if err != nil {
		return nil, err
	}
	backend, modelID = strings.ToLower(strings.TrimSpace(backend)), strings.TrimSpace(modelID)
	out := make([]BackendRuntimeProfile, 0, len(profiles))
	for _, profile := range profiles {
		if backend != "" && profile.Backend != backend {
			continue
		}
		if modelID != "" && profile.ModelID != modelID {
			continue
		}
		out = append(out, profile)
	}
	return out, nil
}

func (r *Repo) GetBackendRuntimeProfile(ctx context.Context, id string) (BackendRuntimeProfile, error) {
	r.profileMu.Lock()
	defer r.profileMu.Unlock()
	profiles, err := r.loadBackendRuntimeProfiles(ctx)
	if err != nil {
		return BackendRuntimeProfile{}, err
	}
	for _, profile := range profiles {
		if profile.ID == strings.TrimSpace(id) {
			return profile, nil
		}
	}
	return BackendRuntimeProfile{}, ErrBackendRuntimeProfileNotFound
}

func (r *Repo) SaveBackendRuntimeProfile(ctx context.Context, profile BackendRuntimeProfile) (BackendRuntimeProfile, error) {
	r.profileMu.Lock()
	defer r.profileMu.Unlock()
	profiles, err := r.loadBackendRuntimeProfiles(ctx)
	if err != nil {
		return BackendRuntimeProfile{}, err
	}
	profile, err = profile.NormalizeAndValidate()
	if err != nil {
		return BackendRuntimeProfile{}, err
	}
	found := false
	for i := range profiles {
		if profiles[i].ID != profile.ID {
			continue
		}
		profile.Version = profiles[i].Version + 1
		found = true
		profiles[i] = profile
		break
	}
	if !found {
		if len(profiles) >= 50 {
			return BackendRuntimeProfile{}, fmt.Errorf("at most 50 runtime profiles can be stored")
		}
		profile.Version = 1
		profiles = append(profiles, profile)
	}
	if err := r.SetJSON(ctx, backendRuntimeProfilesKey, backendRuntimeProfilesDocument{Profiles: profiles}); err != nil {
		return BackendRuntimeProfile{}, err
	}
	return profile, nil
}

func (r *Repo) DeleteBackendRuntimeProfile(ctx context.Context, id string) error {
	r.profileMu.Lock()
	defer r.profileMu.Unlock()
	profiles, err := r.loadBackendRuntimeProfiles(ctx)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	out := make([]BackendRuntimeProfile, 0, len(profiles))
	found := false
	for _, profile := range profiles {
		if profile.ID == id {
			found = true
			continue
		}
		out = append(out, profile)
	}
	if !found {
		return ErrBackendRuntimeProfileNotFound
	}
	return r.SetJSON(ctx, backendRuntimeProfilesKey, backendRuntimeProfilesDocument{Profiles: out})
}
