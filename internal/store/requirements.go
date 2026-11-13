package store

import "strings"

// Preconditions carry meaning independently of presentation tag budgets.
func cleanRequirements(values []string) []string {
	if values == nil {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if key != "" && !seen[key] {
			out = append(out, value)
			seen[key] = true
		}
	}
	return out
}
