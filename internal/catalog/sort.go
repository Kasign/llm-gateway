package catalog

import "sort"

// SortListings orders listings in place according to sort key.
func SortListings(items []Listing, key SortKey) {
	if len(items) < 2 {
		return
	}
	switch key {
	case SortNewest:
		sort.SliceStable(items, func(i, j int) bool {
			ti, tj := items[i].SortTime, items[j].SortTime
			if ti.IsZero() && tj.IsZero() {
				return false
			}
			if ti.IsZero() {
				return false
			}
			if tj.IsZero() {
				return true
			}
			return ti.After(tj)
		})
	default:
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].SortDownloads > items[j].SortDownloads
		})
	}
}

// MergeSorted combines HF and Ollama results after each source is sorted; global re-sort applied.
func MergeSorted(hf, ollama []Listing, sort SortKey, limit int) []Listing {
	out := append(append([]Listing{}, hf...), ollama...)
	SortListings(out, sort)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
