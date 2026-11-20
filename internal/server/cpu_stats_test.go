package server

import "testing"

func TestParseCPUModel(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"x86 model name", "processor : 0\nmodel name : Intel(R) Core(TM) i5-1340P\n", "Intel(R) Core(TM) i5-1340P"},
		{"arm hardware", "processor : 0\nHardware : Raspberry Pi 5 Model B Rev 1.0\n", "Raspberry Pi 5 Model B Rev 1.0"},
		{"unknown", "processor : 0\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCPUModel(tc.input); got != tc.want { t.Fatalf("parseCPUModel() = %q, want %q", got, tc.want) }
		})
	}
}
