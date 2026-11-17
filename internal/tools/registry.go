package tools

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

var (
	ErrInvalidDefinition = errors.New("invalid tool definition")
	ErrDuplicateTool     = errors.New("tool already registered")
	ErrToolNotFound      = errors.New("tool not found")
	ErrRegistryFull      = errors.New("tool registry is full")
	ErrNilExecutor       = errors.New("tool executor is nil")
)

type registration struct {
	definition Definition
	executor   Executor
}

// Registry 保存服务端启动时装配的工具定义和执行器。
// 注册允许并发调用，但产品运行时应在服务启动阶段完成注册。
type Registry struct {
	mu    sync.RWMutex
	items map[string]registration
}

func NewRegistry() *Registry {
	return &Registry{items: make(map[string]registration)}
}

func (r *Registry) Register(def Definition, executor Executor) error {
	if r == nil { return fmt.Errorf("%w: registry is nil", ErrInvalidDefinition) }
	if isNilExecutor(executor) { return ErrNilExecutor }
	if err := validateDefinition(def); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDefinition, err)
	}
	def = def.Clone()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[def.Name]; exists { return fmt.Errorf("%w: %s", ErrDuplicateTool, def.Name) }
	if len(r.items) >= 64 { return ErrRegistryFull }
	if r.items == nil { r.items = make(map[string]registration) }
	r.items[def.Name] = registration{definition: def, executor: executor}
	return nil
}

func (r *Registry) Lookup(name string) (Definition, Executor, error) {
	if r == nil { return Definition{}, nil, ErrToolNotFound }
	r.mu.RLock()
	item, ok := r.items[name]
	r.mu.RUnlock()
	if !ok { return Definition{}, nil, fmt.Errorf("%w: %s", ErrToolNotFound, name) }
	return item.definition.Clone(), item.executor, nil
}

// List 返回按名称排序的 Definition 深拷贝，不暴露内部 map 或 Schema 字节。
func (r *Registry) List() []Definition {
	if r == nil { return nil }
	r.mu.RLock()
	out := make([]Definition, 0, len(r.items))
	for _, item := range r.items { out = append(out, item.definition.Clone()) }
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func validateDefinition(def Definition) error {
	if len(def.Name) == 0 || len(def.Name) > MaxToolNameBytes || !toolNamePattern.MatchString(def.Name) {
		return errors.New("name must be a stable lowercase identifier")
	}
	if len(def.Description) > MaxToolDescriptionBytes {
		return fmt.Errorf("description exceeds %d bytes", MaxToolDescriptionBytes)
	}
	if err := ValidateSchema(def.Parameters); err != nil { return err }
	return nil
}

func isNilExecutor(executor Executor) bool {
	if executor == nil { return true }
	v := reflect.ValueOf(executor)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
