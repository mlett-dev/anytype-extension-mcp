package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDebugToolsReportMetaKeysNotValues(t *testing.T) {
	meta := map[string]any{
		"openai/locale":    "xx-XX",
		"openai/subject":   "v1/subject-secret-value",
		"openai/session":   "v1/session-secret-value",
		"openai/userAgent": "agent-secret-value",
		"openai/userLocation": map[string]any{
			"city": "city-secret-value", "latitude": "12.34567", "longitude": "76.54321",
		},
	}
	for _, name := range []string{"debug-receive-file", "debug-receive-openai-file"} {
		response := testRPC(t, &mcpServer{}, "tools/call", map[string]any{
			"name": name, "arguments": map[string]any{}, "_meta": meta,
		})
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{"secret-value", "12.34567", "76.54321", "xx-XX"} {
			if bytes.Contains(encoded, []byte(leaked)) {
				t.Errorf("%s echoed a _meta value: %s", name, leaked)
			}
		}
		out := response.Result.(map[string]any)["structuredContent"].(map[string]any)
		keys, _ := out["call_meta_keys"].([]string)
		if len(keys) != len(meta) || keys[0] != "openai/locale" {
			t.Errorf("%s: call_meta_keys = %#v", name, out["call_meta_keys"])
		}
	}
}
