package parser

import "strings"

// textScore returns score when any lowercase marker occurs in the page text.
func textScore(score int, markers ...string) func(*Document) int {
	return func(document *Document) int {
		for _, marker := range markers {
			if strings.Contains(document.text, marker) {
				return score
			}
		}
		return 0
	}
}
