package output

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"
)

func JSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func Size(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	exp := int(math.Log(float64(n)) / math.Log(float64(unit)))
	if exp > 6 {
		exp = 6
	}
	prefix := "KMGTPE"[exp-1]
	return fmt.Sprintf("%.1f %ciB", float64(n)/math.Pow(float64(unit), float64(exp)), prefix)
}

func RelativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
