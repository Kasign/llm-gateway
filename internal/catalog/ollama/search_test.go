package ollama

import (
	"strings"
	"testing"
)

func TestParseHumanCount(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"2.7M", 2700000, true},
		{"157.2K", 157200, true},
		{"21.1M", 21100000, true},
		{"", 0, false},
		{"3,630,070", 3630070, true},
	}
	for _, c := range cases {
		got, ok := parseHumanCount(c.in)
		if ok != c.ok || got != c.want {
			t.Fatalf("parseHumanCount(%q) = %d,%v want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseCurrentSearchCard(t *testing.T) {
	page := `
<ul role="list">
<li class="border-b border-black/[0.08]">
  <a href="/library/qwen3.8" class="group flex items-start justify-between gap-6 py-6">
    <div class="min-w-0 flex-1">
      <h2 class="truncate text-xl font-medium" title="qwen3.8">
        <span >qwen3.8</span>
      </h2>
      <p class="mt-1 max-w-2xl truncate text-sm leading-6 text-black/60" title="Qwen3.8 delivers substantial gains.">Qwen3.8 delivers substantial gains.</p>
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <span class="inline-flex items-center rounded-md bg-indigo-50 px-2 py-0.5 text-xs font-medium text-indigo-600 sm:text-[13px]">vision</span>
        <span class="inline-flex items-center rounded-md bg-[#ddf4ff] px-2 py-0.5 text-xs font-medium text-blue-600 sm:text-[13px]">27b</span>
      </div>
    </div>
    <span class="inline-flex shrink-0 items-center gap-1.5" title="3,630,070 downloads"><span >3.6M</span></span>
  </a>
</li>
<li class="border-b border-black/[0.08]">
  <a href="/aiconjured/qwen3.8-custom" class="group flex items-start justify-between gap-6 py-6">
    <div class="min-w-0 flex-1">
      <h2 title="aiconjured/qwen3.8-custom"><span >aiconjured/qwen3.8-custom</span></h2>
      <p class="mt-1 max-w-2xl truncate text-sm leading-6 text-black/60">Text &#43; MTP</p>
    </div>
    <span title="184,200 downloads"><span >184.2K</span></span>
  </a>
</li>
</ul>`
	matches := reLibraryLink.FindAllStringSubmatch(page, -1)
	if len(matches) != 2 {
		t.Fatalf("cards = %d", len(matches))
	}
	if got := canonicalModelID(matches[0][1]); got != "qwen3.8" {
		t.Fatalf("library id = %q", got)
	}
	if got := canonicalModelID(matches[1][1]); got != "aiconjured/qwen3.8-custom" {
		t.Fatalf("namespace id = %q", got)
	}
	block := matches[0][2]
	if !strings.Contains(reDesc.FindStringSubmatch(block)[1], "substantial gains") {
		t.Fatal("description missing")
	}
	pulls := rePullsExact.FindStringSubmatch(block)
	n, ok := parseHumanCount(pulls[1])
	if !ok || n != 3630070 {
		t.Fatalf("pulls = %d %v", n, ok)
	}
	if tags := reTagPill.FindAllStringSubmatch(block, -1); len(tags) != 1 || tags[0][1] != "vision" {
		t.Fatalf("tags = %#v", tags)
	}
	if sizes := reSizePill.FindAllStringSubmatch(block, -1); len(sizes) != 1 || sizes[0][1] != "27b" {
		t.Fatalf("sizes = %#v", sizes)
	}
	desc := reDesc.FindStringSubmatch(matches[1][2])
	if desc[1] != "Text &#43; MTP" {
		t.Fatalf("raw desc = %q", desc[1])
	}
}
