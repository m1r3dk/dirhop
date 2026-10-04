package shell

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{`cd documents`, []string{"cd", "documents"}},
		{`download "release files/app.zip"`, []string{"download", "release files/app.zip"}},
		{`find '*.zip'`, []string{"find", "*.zip"}},
		{`cd release\ files`, []string{"cd", "release files"}},
		{`search "日本 語"`, []string{"search", "日本 語"}},
		{`stat ""`, []string{"stat", ""}},
	}
	for _, tt := range tests {
		got, err := Split(tt.line)
		if err != nil {
			t.Fatalf("Split(%q): %v", tt.line, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("Split(%q) = %#v, want %#v", tt.line, got, tt.want)
		}
	}
}

func TestSplitErrors(t *testing.T) {
	for _, line := range []string{`cd "broken`, `cd broken\`} {
		if _, err := Split(line); err == nil {
			t.Fatalf("Split(%q) unexpectedly succeeded", line)
		}
	}
}
