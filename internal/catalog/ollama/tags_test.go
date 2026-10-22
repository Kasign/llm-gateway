package ollama

import "testing"

const fixtureLlama32Tags = `
        <a href="/library/llama3.2:latest" class="sm:hidden flex flex-col space-y-[6px] group text-[13px] px-4 py-3">
          <p class="flex text-neutral-500">2.0GB · 128K context window · Text · 2 years ago</p>
        </a>
        <div class="hidden group px-4 py-3 sm:grid sm:grid-cols-12 text-[13px]">
            <a href="/library/llama3.2:latest" class="block group-hover:underline text-sm font-medium text-neutral-800">llama3.2:latest</a>
          <p  class="col-span-2 text-neutral-500">2.0GB</p>
          <p class="col-span-2 text-neutral-500">128K</p>
          <p class="col-span-2 text-neutral-500">Text</p>
        </div>
        <a href="/library/llama3.2:1b" class="sm:hidden flex flex-col space-y-[6px] group text-[13px] px-4 py-3">
          <p class="flex text-neutral-500">1.3GB · 128K context window · Text · 2 years ago</p>
        </a>
        <div class="hidden group px-4 py-3 sm:grid sm:grid-cols-12 text-[13px]">
            <a href="/library/llama3.2:1b" class="block group-hover:underline text-sm font-medium text-neutral-800">llama3.2:1b</a>
          <p  class="col-span-2 text-neutral-500">1.3GB</p>
          <p class="col-span-2 text-neutral-500">128K</p>
          <p class="col-span-2 text-neutral-500">Text</p>
        </div>
`

func TestParseLibraryTagsFromHTML(t *testing.T) {
	tags := parseLibraryTagsFromHTML(fixtureLlama32Tags, "llama3.2")
	byTag := map[string]Tag{}
	for _, tg := range tags {
		byTag[tg.Tag] = tg
	}
	if len(tags) < 2 {
		t.Fatalf("expected >=2 tags, got %d", len(tags))
	}
	latest, ok := byTag["latest"]
	if !ok || latest.Size != "2.0GB" || latest.Context != "128K" {
		t.Fatalf("latest: %+v", latest)
	}
	oneB, ok := byTag["1b"]
	if !ok || oneB.Size != "1.3GB" {
		t.Fatalf("1b: %+v", oneB)
	}
}

func TestParseCurrentLibraryTags(t *testing.T) {
	html := `
<div class="group px-4 py-3">
  <a href="/library/qwen3:latest" class="md:hidden flex flex-col space-y-[6px] group">
    <span class="group-hover:underline">qwen3:latest</span>
  </a>
  <div class="hidden md:flex flex-col space-y-[6px]">
    <div class="grid grid-cols-12 items-center">
      <a href="/library/qwen3:latest" class="group-hover:underline">qwen3:latest</a>
      <p class="col-span-2 text-neutral-500 text-[13px]">5.2GB</p>
      <p class="col-span-2 text-neutral-500 text-[13px]">40K</p>
      <div class="col-span-2 text-neutral-500 text-[13px] ">Text</div>
    </div>
  </div>
</div>
<div class="group px-4 py-3">
  <a href="/aiconjured/custom:q4_K_M" class="group-hover:underline">custom:q4_K_M</a>
  <p class="col-span-2 text-neutral-500 text-[13px]">3.1GB</p>
  <p class="col-span-2 text-neutral-500 text-[13px]">32K</p>
  <div class="col-span-2 text-neutral-500 text-[13px] ">Text</div>
</div>`
	tags := parseLibraryTagsFromHTML(html, "qwen3")
	if len(tags) != 1 || tags[0].Tag != "latest" || tags[0].Size != "5.2GB" || tags[0].Context != "40K" || tags[0].Input != "Text" {
		t.Fatalf("qwen3 tags: %+v", tags)
	}
	namespaced := parseLibraryTagsFromHTML(html, "aiconjured/custom")
	if len(namespaced) != 1 || namespaced[0].Tag != "q4_K_M" || namespaced[0].Size != "3.1GB" {
		t.Fatalf("namespaced tags: %+v", namespaced)
	}
	if got := tagsPageURL("https://ollama.com", "qwen3"); got != "https://ollama.com/library/qwen3/tags" {
		t.Fatal(got)
	}
	if got := tagsPageURL("https://ollama.com", "aiconjured/custom"); got != "https://ollama.com/aiconjured/custom/tags" {
		t.Fatal(got)
	}
}

func TestParseOllamaMobileMeta(t *testing.T) {
	size, ctx, input := parseOllamaMobileMeta("2.0GB · 128K context window · Text · 2 years ago")
	if size != "2.0GB" || ctx != "128K" || input != "Text" {
		t.Fatalf("got %q %q %q", size, ctx, input)
	}
}
