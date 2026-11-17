package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"strconv"
	"testing"
)

type testExecutor struct{}
func (testExecutor) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func testDefinition(name string) Definition {
	return Definition{Name:name, Description:"test tool", Parameters:json.RawMessage(testSchema)}
}

func TestRegistryRegisterLookupAndClone(t *testing.T) {
	r := NewRegistry()
	def := testDefinition("web.search")
	if err := r.Register(def, testExecutor{}); err != nil { t.Fatal(err) }
	def.Parameters[0] = '!'
	got, executor, err := r.Lookup("web.search")
	if err != nil { t.Fatal(err) }
	if _, ok := executor.(testExecutor); !ok { t.Fatalf("unexpected executor %T", executor) }
	if err := ValidateSchema(got.Parameters); err != nil { t.Fatalf("registry retained caller mutation: %v", err) }
	got.Parameters[0] = '!'
	list := r.List()
	if len(list) != 1 || list[0].Name != "web.search" { t.Fatalf("unexpected list: %#v", list) }
	if err := r.Register(testDefinition("web.search"), testExecutor{}); !errors.Is(err, ErrDuplicateTool) { t.Fatalf("expected duplicate error, got %v", err) }
}

func TestRegistryRejectsInvalidDefinitionsAndTypedNil(t *testing.T) {
	r := NewRegistry()
	var typedNil *testExecutor
	if err := r.Register(testDefinition("web.search"), typedNil); !errors.Is(err, ErrNilExecutor) { t.Fatalf("expected nil executor error, got %v", err) }
	for _, name := range []string{"", "Web.Search", "web..search", "web.search.", "web search"} {
		if err := r.Register(testDefinition(name), testExecutor{}); !errors.Is(err, ErrInvalidDefinition) { t.Errorf("expected invalid name %q, got %v", name, err) }
	}
}

func TestRegistrySortAndConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zeta", "alpha", "web.search"} {
		if err := r.Register(testDefinition(name), testExecutor{}); err != nil { t.Fatal(err) }
	}
	list := r.List()
	if list[0].Name != "alpha" || list[1].Name != "web.search" || list[2].Name != "zeta" { t.Fatalf("not sorted: %#v", list) }
	var wg sync.WaitGroup
	for i:=0; i<16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j:=0; j<100; j++ {
				if _, _, err := r.Lookup("web.search"); err != nil { t.Error(err); return }
				_ = r.List()
			}
		}()
	}
	wg.Wait()
}

func TestRegistryConcurrentRegistration(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := r.Register(testDefinition("tool"+strconv.Itoa(i)), testExecutor{}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs { t.Error(err) }
	if got := len(r.List()); got != 32 { t.Fatalf("expected 32 registrations, got %d", got) }
}

func TestRegistryZeroValue(t *testing.T) {
	var r Registry
	if err := r.Register(testDefinition("web.search"), testExecutor{}); err != nil { t.Fatal(err) }
	if _, _, err := r.Lookup("web.search"); err != nil { t.Fatal(err) }
}

func TestRegistryLimit(t *testing.T) {
	r := NewRegistry()
	for i:=0; i<64; i++ {
		name := "tool" + strconv.Itoa(i)
		if err := r.Register(testDefinition(name), testExecutor{}); err != nil { t.Fatalf("register %s: %v", name, err) }
	}
	if err := r.Register(testDefinition("tool64"), testExecutor{}); !errors.Is(err, ErrRegistryFull) { t.Fatalf("expected registry full, got %v", err) }
}
