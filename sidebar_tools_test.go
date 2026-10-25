package main

import (
	"strings"
	"testing"
)

// A snapshot of an empty sidebar is an empty list, and applying it must be
// possible: with remove_missing it means "no widgets". Without it, an empty
// list asks for nothing and is refused.
func TestSidebarApplyTakesAnEmptyListOnlyWithRemoveMissing(t *testing.T) {
	s := &mcpServer{}
	_, err := s.toolSidebarApply(map[string]any{"widgets": []any{}})
	if err == nil || !strings.Contains(err.Error(), "remove_missing") {
		t.Errorf("empty list without remove_missing: err = %v", err)
	}
	// Past the list check it fails on the missing space, not on the list.
	_, err = s.toolSidebarApply(map[string]any{"widgets": []any{}, "remove_missing": true})
	if err == nil || strings.Contains(err.Error(), "widgets is required") {
		t.Errorf("empty list with remove_missing: err = %v", err)
	}
}
