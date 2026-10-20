package node

import "testing"

func TestIsMetricsInterface(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "eth0", want: true},
		{name: "ens3", want: true},
		{name: "lo", want: false},
		{name: "docker0", want: false},
		{name: "veth123", want: false},
		{name: "wg0", want: false},
		{name: "pp-egress", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isMetricsInterface(test.name); got != test.want {
				t.Fatalf("isMetricsInterface(%q) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}

func TestBootIdentityFallbackIsStableForSample(t *testing.T) {
	bootTime, bootID := bootIdentity(60)
	if bootTime <= 0 {
		t.Fatalf("bootIdentity() boot time = %d", bootTime)
	}
	if bootID == "" {
		t.Fatal("bootIdentity() returned empty boot ID")
	}
}
