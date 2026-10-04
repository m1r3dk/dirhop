package cli

import "testing"

func TestParseSize(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  int64
	}{{"42", 42}, {"1KB", 1000}, {"1KiB", 1024}, {"1.5GB", 1500000000}} {
		got, err := parseSize(tt.input)
		if err != nil || got != tt.want {
			t.Fatalf("parseSize(%q) = %d, %v; want %d", tt.input, got, err, tt.want)
		}
	}
}

func TestParseSizeFilter(t *testing.T) {
	min, max, err := parseSizeFilter("100MB..2GB")
	if err != nil || min == nil || max == nil || *min != 100000000 || *max != 2000000000 {
		t.Fatalf("unexpected filter: min=%v max=%v err=%v", min, max, err)
	}
	min, max, err = parseSizeFilter(">1GiB")
	if err != nil || min == nil || max != nil || *min != 1<<30 {
		t.Fatalf("unexpected lower filter: min=%v max=%v err=%v", min, max, err)
	}
}
