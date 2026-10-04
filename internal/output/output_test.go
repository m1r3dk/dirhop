package output

import "testing"

func TestSize(t *testing.T) {
	for _, tt := range []struct {
		n    int64
		want string
	}{{-1, "-"}, {0, "0 B"}, {1024, "1.0 KiB"}, {5 * 1024 * 1024, "5.0 MiB"}} {
		if got := Size(tt.n); got != tt.want {
			t.Fatalf("Size(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
