package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func budgetList(n, textLen int) []map[string]any {
	items := make([]map[string]any, n)
	for i := range items {
		items[i] = map[string]any{"id": i, "snippet": strings.Repeat("x", textLen)}
	}
	return items
}

func encodedLen(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func TestCapListLeavesASmallAnswerAlone(t *testing.T) {
	s := &mcpServer{cfg: serverConfig{responseBudget: 64 << 10}}
	out := map[string]any{"objects": budgetList(10, 100), "count": 10}
	s.capList(out, "objects", 0)
	if _, cut := out["truncated"]; cut || out["count"] != 10 {
		t.Fatalf("a small answer was changed: %v", out)
	}
}

func TestCapListCutsToTheBudgetAndSaysWhereToGoOn(t *testing.T) {
	const budget = 8 << 10
	s := &mcpServer{cfg: serverConfig{responseBudget: budget}}
	upstream := map[string]any{"has_more": false, "total": 500}
	out := map[string]any{
		"objects": budgetList(100, 200), "count": 100, "shown": 100,
		"pagination": upstream, "has_more": false, "next_offset": 140,
	}
	s.capList(out, "objects", 40)

	kept := len(out["objects"].([]map[string]any))
	if kept == 0 || kept >= 100 {
		t.Fatalf("kept %d of 100", kept)
	}
	if size := encodedLen(t, out); size > budget {
		t.Fatalf("answer is %d bytes, budget %d", size, budget)
	}
	// One more entry would not have fitted: the cut is as late as possible.
	out["objects"] = budgetList(kept+1, 200)
	if encodedLen(t, out) <= budget {
		t.Fatalf("cut too early at %d", kept)
	}
	out["objects"] = budgetList(kept, 200)

	truncated := out["truncated"].(map[string]any)
	if truncated["next_offset"] != 40+kept || truncated["omitted"] != 100-kept {
		t.Fatalf("truncated = %v", truncated)
	}
	if out["count"] != kept || out["shown"] != kept || out["next_offset"] != 40+kept || out["has_more"] != true {
		t.Fatalf("counts not corrected: %v", out)
	}
	if out["pagination"].(map[string]any)["has_more"] != true || upstream["has_more"] != false {
		t.Fatalf("pagination must say has_more without changing the upstream map: %v / %v", out["pagination"], upstream)
	}
}

// An entry larger than the whole budget is still answered, alone, so paging
// moves on instead of returning nothing forever.
func TestCapListAlwaysKeepsOneEntry(t *testing.T) {
	s := &mcpServer{cfg: serverConfig{responseBudget: 1 << 10}}
	out := map[string]any{"objects": budgetList(3, 5000), "count": 3}
	s.capList(out, "objects", 0)
	if out["count"] != 1 || out["truncated"].(map[string]any)["next_offset"] != 1 {
		t.Fatalf("got %v", out)
	}
}

func TestCapListOffWhenBudgetIsZero(t *testing.T) {
	s := &mcpServer{}
	out := map[string]any{"objects": budgetList(50, 5000), "count": 50}
	s.capList(out, "objects", 0)
	if out["count"] != 50 {
		t.Fatalf("budget 0 must not cut: %v", out["count"])
	}
}

func TestResponseBudgetFromEnv(t *testing.T) {
	for raw, want := range map[string]int{
		"": defaultResponseBudgetKB << 10, "abc": defaultResponseBudgetKB << 10,
		"-5": defaultResponseBudgetKB << 10, "0": 0, "256": 256 << 10,
	} {
		t.Setenv("ANYTYPE_RESPONSE_BUDGET_KB", raw)
		if got := responseBudgetFromEnv(); got != want {
			t.Errorf("%q: got %d, want %d", raw, got, want)
		}
	}
}
