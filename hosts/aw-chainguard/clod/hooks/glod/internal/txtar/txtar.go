// Package txtar parses a minimal subset of the golang.org/x/tools/txtar archive
// format into named sections, shared by the hook test suites. Inlined to keep
// the module dependency-free.
package txtar

import "strings"

// Parse splits a txtar archive into named sections. A line of the form
// "-- name --" starts a section whose body runs to the next marker or EOF; any
// text before the first marker is ignored.
func Parse(data []byte) map[string]string {
	files := map[string]string{}
	var name string
	var body []string
	flush := func() {
		if name != "" {
			files[name] = strings.Join(body, "\n")
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "-- ") && strings.HasSuffix(s, " --") && len(s) > 5 {
			flush()
			name = strings.TrimSpace(s[3 : len(s)-3])
			body = body[:0]
			continue
		}
		if name != "" {
			body = append(body, line)
		}
	}
	flush()
	return files
}
