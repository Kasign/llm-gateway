package config

import (
	"testing"
	"time"
)

func TestWarmStartInterval(t *testing.T) {
	if (&Config{}).WarmStartInterval() != 0 {
		t.Fatal("disabled warm start must not repeat")
	}
	off := &Config{}
	off.Llama.WarmStart = false
	off.Llama.WarmStartInterval = 5
	if off.WarmStartInterval() != 0 {
		t.Fatal("interval ignored when warm start is off")
	}
	on := &Config{}
	on.Llama.WarmStart = true
	if on.WarmStartInterval() != 15*time.Second {
		t.Fatalf("default %s", on.WarmStartInterval())
	}
	on.Llama.WarmStartInterval = 20
	if on.WarmStartInterval() != 20*time.Second {
		t.Fatalf("configured %s", on.WarmStartInterval())
	}
}
