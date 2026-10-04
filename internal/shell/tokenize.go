package shell

import (
	"fmt"
	"strings"
	"unicode"
)

// Split parses the quoting and escaping needed for filesystem paths. It is not
// a command shell and deliberately performs no expansion or substitution.
func Split(line string) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	escaped := false
	started := false

	flush := func() {
		if started {
			args = append(args, b.String())
			b.Reset()
			started = false
		}
	}

	for _, r := range line {
		if escaped {
			b.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			started = true
		case unicode.IsSpace(r):
			flush()
		default:
			b.WriteRune(r)
			started = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("unfinished escape")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	return args, nil
}
