package output

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
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

// Raw formats an exact byte count, or "-" when unknown.
func Raw(n int64) string {
	if n < 0 {
		return "-"
	}
	return fmt.Sprint(n)
}
