package main

import (
	"os"
	"strings"
)

// loadDotEnv reads KEY=VALUE lines from path (default ".env") and applies
// them only for vars not already present in the environment.
// A missing file is a no-op, so plain env vars / defaults keep working.
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for k, v := range parseDotEnv(string(b)) {
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}

// parseDotEnv parses KEY=VALUE lines. Blank lines and lines starting with
// '#' are ignored. A leading 'export ' is accepted. Values may be wrapped
// in single or double quotes (stripped). No $-expansion, no inline comments.
func parseDotEnv(data string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, " \t") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		out[k] = v
	}
	return out
}
