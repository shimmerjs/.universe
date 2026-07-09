package module

import (
	"fmt"
	"strconv"
	"strings"
)

// VMStatUsedGiB parses vm_stat output into used GiB. active + wired +
// compressed is the btop-style "used"; output carrying none of those page
// counts is an error.
func VMStatUsedGiB(vmStat string) (float64, error) {
	pages := map[string]float64{}
	pageSize := 16384.0
	for line := range strings.SplitSeq(vmStat, "\n") {
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			if i := strings.Index(line, "page size of "); i >= 0 {
				if fields := strings.Fields(line[i+len("page size of "):]); len(fields) > 0 {
					if ps, err := strconv.ParseFloat(fields[0], 64); err == nil {
						pageSize = ps
					}
				}
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "."), 64)
		if err != nil {
			continue
		}
		pages[strings.TrimSpace(k)] = n
	}
	used, found := 0.0, false
	for _, k := range []string{"Pages active", "Pages wired down", "Pages occupied by compressor"} {
		if n, ok := pages[k]; ok {
			used += n
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("vm_stat: no page counts in output")
	}
	const gib = 1024 * 1024 * 1024
	return used * pageSize / gib, nil
}
