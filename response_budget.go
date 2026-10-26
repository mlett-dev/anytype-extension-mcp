package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// defaultResponseBudgetKB caps what a list tool answers with. A client keeps
// every result in the model's context, and the call log showed single
// searches of 1.8 MB and listings of 600 KB; the normal 95th percentile was
// below 100 KB. Past the budget the list is cut and next_offset says where
// to continue.
const defaultResponseBudgetKB = 128

// responseBudgetFromEnv reads ANYTYPE_RESPONSE_BUDGET_KB; 0 turns the cap
// off. A value that is not a number falls back to the default rather than
// to no limit.
func responseBudgetFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("ANYTYPE_RESPONSE_BUDGET_KB"))
	kb, err := strconv.Atoi(raw)
	if raw == "" || err != nil || kb < 0 {
		kb = defaultResponseBudgetKB
	}
	return kb << 10
}

// capList cuts out[key] from the end until out encodes to at most budget
// bytes. At least one entry stays, so paging always moves forward even past
// an entry larger than the budget. A cut adds truncated with next_offset,
// corrects count, shown and has_more, and is noted in the call log.
func (s *mcpServer) capList(out map[string]any, key string, offset int) {
	budget := s.cfg.responseBudget
	items, ok := out[key].([]map[string]any)
	if budget <= 0 || !ok || len(items) < 2 {
		return
	}
	out[key] = []map[string]any{}
	base, err := json.Marshal(out)
	out[key] = items
	if err != nil {
		return
	}
	// Room for the truncated entry a cut adds, sized with the largest numbers
	// it can hold, so the cut answer fits as well.
	reserve, _ := json.Marshal(map[string]any{"truncated": truncation(budget, offset+len(items), len(items))})
	size, kept := len(base)+len(reserve), 0
	for i, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return
		}
		size += len(encoded)
		if i > 0 {
			size++ // the comma
		}
		if size > budget && i > 0 {
			break
		}
		kept = i + 1
	}
	if kept == len(items) {
		return
	}

	out[key] = items[:kept]
	for _, field := range []string{"count", "shown"} {
		if _, ok := out[field]; ok {
			out[field] = kept
		}
	}
	if _, ok := out["has_more"]; ok {
		out["has_more"] = true
	}
	if _, ok := out["next_offset"]; ok {
		out["next_offset"] = offset + kept
	}
	if pagination, ok := out["pagination"].(map[string]any); ok {
		cut := make(map[string]any, len(pagination))
		for k, v := range pagination {
			cut[k] = v
		}
		cut["has_more"] = true
		out["pagination"] = cut
	}
	out["truncated"] = truncation(budget, offset+kept, len(items)-kept)
	s.note("cut", len(items)-kept)
}

func truncation(budget, nextOffset, omitted int) map[string]any {
	return map[string]any{
		"next_offset": nextOffset,
		"omitted":     omitted,
		"note":        fmt.Sprintf("cut to stay under %d KB; continue with offset=%d, or ask for fewer fields or properties", budget>>10, nextOffset),
	}
}
