package auth

import (
	"reflect"
	"testing"
)

func TestDecodeAllowedToolsDefaultsToNoGrant(t *testing.T) {
	cases := []struct {
		name string
		raw string
		want []string
	}{
		{name:"legacy NULL", raw:"", want:[]string{}},
		{name:"JSON null", raw:"null", want:[]string{}},
		{name:"malformed JSON", raw:"{", want:[]string{}},
		{name:"empty grant", raw:"[]", want:[]string{}},
		{name:"stored grant", raw:`["web.search"]`, want:[]string{"web.search"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeAllowedTools(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decodeAllowedTools(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
