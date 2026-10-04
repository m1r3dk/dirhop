package filesystem

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func ParseSize(value string) (int64, error) {
	s := strings.TrimSpace(strings.ToUpper(value))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	units := []struct {
		suffix string
		factor float64
	}{
		{"PIB", 1 << 50}, {"PB", 1e15}, {"TIB", 1 << 40}, {"TB", 1e12},
		{"GIB", 1 << 30}, {"GB", 1e9}, {"MIB", 1 << 20}, {"MB", 1e6},
		{"KIB", 1 << 10}, {"KB", 1e3}, {"B", 1},
	}
	factor := float64(1)
	for _, unit := range units {
		if strings.HasSuffix(s, unit.suffix) {
			s = strings.TrimSpace(strings.TrimSuffix(s, unit.suffix))
			factor = unit.factor
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 || n*factor > math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	return int64(n * factor), nil
}

func ParseSizeFilter(value string) (min, max *int64, err error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return nil, nil, nil
	}
	if left, right, ok := strings.Cut(s, ".."); ok {
		a, e := ParseSize(left)
		if e != nil {
			return nil, nil, e
		}
		b, e := ParseSize(right)
		if e != nil {
			return nil, nil, e
		}
		if a > b {
			return nil, nil, fmt.Errorf("invalid size range %q", value)
		}
		return &a, &b, nil
	}
	if strings.HasPrefix(s, ">") {
		n, e := ParseSize(strings.TrimPrefix(s, ">"))
		if e != nil {
			return nil, nil, e
		}
		return &n, nil, nil
	}
	if strings.HasPrefix(s, "<") {
		n, e := ParseSize(strings.TrimPrefix(s, "<"))
		if e != nil {
			return nil, nil, e
		}
		return nil, &n, nil
	}
	n, e := ParseSize(s)
	if e != nil {
		return nil, nil, e
	}
	return &n, &n, nil
}
