package main

import "testing"

func TestEveryToolIsAnnotated(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range (&mcpServer{}).allToolDefs() {
		m := def.(map[string]any)
		name := m["name"].(string)
		seen[name] = true
		ann, ok := m["annotations"].(map[string]any)
		if !ok {
			t.Errorf("%s: no annotations", name)
			continue
		}
		for _, key := range []string{"readOnlyHint", "destructiveHint", "openWorldHint"} {
			if _, ok := ann[key].(bool); !ok {
				t.Errorf("%s: %s missing", name, key)
			}
		}
		if ann["readOnlyHint"] == true && ann["destructiveHint"] == true {
			t.Errorf("%s: read-only and destructive", name)
		}
	}
	for name := range toolAnnotations {
		if !seen[name] {
			t.Errorf("toolAnnotations names unknown tool %s", name)
		}
	}
}

func TestToolsListCarriesAnnotations(t *testing.T) {
	result := testRPC(t, &mcpServer{}, "tools/list", map[string]any{}).Result.(map[string]any)
	tools := map[string]map[string]any{}
	for _, def := range result["tools"].([]any) {
		m := def.(map[string]any)
		tools[m["name"].(string)] = m
	}
	hint := func(name, key string) any { return tools[name]["annotations"].(map[string]any)[key] }
	if hint("file-export", "readOnlyHint") != true {
		t.Error("file-export is not read-only")
	}
	if hint("object-delete-permanently", "destructiveHint") != true {
		t.Error("object-delete-permanently is not destructive")
	}
	if _, ok := tools["file-upload-attachment"]["_meta"]; !ok {
		t.Error("file-upload-attachment lost its _meta")
	}
}
