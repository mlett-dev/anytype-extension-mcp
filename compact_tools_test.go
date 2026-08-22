package main

import "testing"

func TestCompactPropertiesMatchesKeyIDAndName(t *testing.T) {
	raw := []any{
		map[string]any{
			"id":     "prop-id",
			"key":    "date",
			"name":   "Datum",
			"format": "date",
			"date":   map[string]any{"start": "2026-04-25"},
		},
		map[string]any{
			"id":     "amount-id",
			"key":    "amount",
			"name":   "Betrag",
			"format": "number",
			"number": 12.5,
		},
	}

	props := compactProperties(raw, asStringSet([]string{"Datum", "amount-id"}), 20, 500)
	if len(props) != 2 {
		t.Fatalf("expected 2 matched properties, got %d: %#v", len(props), props)
	}
	if _, ok := props["date"]; !ok {
		t.Fatalf("expected property matched by visible name to be returned under technical key: %#v", props)
	}
	if _, ok := props["amount"]; !ok {
		t.Fatalf("expected property matched by id to be returned under technical key: %#v", props)
	}
}

func TestCompactPropertiesMatchesCaseInsensitiveName(t *testing.T) {
	raw := []any{
		map[string]any{
			"id":   "invoice-id",
			"key":  "invoice",
			"name": "Rechnung",
			"url":  "https://example.test/invoice.pdf",
		},
	}

	props := compactProperties(raw, asStringSet([]string{"rechnung"}), 20, 500)
	if _, ok := props["invoice"]; !ok {
		t.Fatalf("expected lowercase selector to match visible name: %#v", props)
	}
}

func TestCompactPropertiesHandlesMapPayload(t *testing.T) {
	raw := map[string]any{
		"auto": map[string]any{
			"name": "Auto",
			"text": "VW",
		},
	}

	props := compactProperties(raw, asStringSet([]string{"Auto"}), 20, 500)
	entry, ok := props["auto"].(map[string]any)
	if !ok {
		t.Fatalf("expected map payload property to be returned: %#v", props)
	}
	if entry["value"] != "VW" {
		t.Fatalf("expected text value to be preserved, got %#v", entry["value"])
	}
}

func TestCompactPropertiesMapPayloadUsesDeterministicOrder(t *testing.T) {
	raw := map[string]any{
		"zeta": map[string]any{
			"name": "Zeta",
			"text": "last",
		},
		"alpha": map[string]any{
			"name": "Alpha",
			"text": "first",
		},
	}

	props := compactProperties(raw, nil, 1, 500)
	if len(props) != 1 {
		t.Fatalf("expected exactly one property, got %d: %#v", len(props), props)
	}
	if _, ok := props["alpha"]; !ok {
		t.Fatalf("expected deterministic map ordering to pick alpha first: %#v", props)
	}
}

func TestCompactPropertyDefinitionsDoNotAddValue(t *testing.T) {
	raw := []any{
		map[string]any{
			"object": "property",
			"id":     "amount-id",
			"key":    "amount",
			"name":   "Betrag",
			"format": "number",
		},
	}

	props := compactPropertyDefinitions(raw, asStringSet([]string{"Betrag"}), 20, 500)
	entry, ok := props["amount"].(map[string]any)
	if !ok {
		t.Fatalf("expected property definition to be returned: %#v", props)
	}
	if _, ok := entry["value"]; ok {
		t.Fatalf("expected property definition not to include value: %#v", entry)
	}
	if entry["format"] != "number" {
		t.Fatalf("expected format to be preserved, got %#v", entry["format"])
	}
}

func TestCreateObjectRequestBodyRequiresTypeKey(t *testing.T) {
	_, err := createObjectRequestBody(map[string]any{
		"name": "Missing type",
	})
	if err == nil {
		t.Fatal("expected missing type_key to fail")
	}
}

func TestCreateObjectRequestBodyCopiesAllowedFields(t *testing.T) {
	body, err := createObjectRequestBody(map[string]any{
		"type_key":    "page",
		"name":        "Page",
		"body":        "Markdown",
		"template_id": "template-id",
		"ignored":     "value",
	})
	if err != nil {
		t.Fatalf("expected create body to be valid: %v", err)
	}
	if body["type_key"] != "page" || body["name"] != "Page" || body["body"] != "Markdown" || body["template_id"] != "template-id" {
		t.Fatalf("expected allowed fields to be copied, got %#v", body)
	}
	if _, ok := body["ignored"]; ok {
		t.Fatalf("expected unknown field to be omitted: %#v", body)
	}
}

func TestUpdateObjectRequestBodyCopiesAllowedFields(t *testing.T) {
	body, err := updateObjectRequestBody(map[string]any{
		"object_id": "object-id",
		"name":      "Updated",
		"markdown":  "Body",
		"ignored":   "value",
	})
	if err != nil {
		t.Fatalf("expected update body to be valid: %v", err)
	}
	if body["name"] != "Updated" || body["markdown"] != "Body" {
		t.Fatalf("expected allowed update fields to be copied, got %#v", body)
	}
	if _, ok := body["object_id"]; ok {
		t.Fatalf("expected object_id to stay out of update body: %#v", body)
	}
	if _, ok := body["ignored"]; ok {
		t.Fatalf("expected unknown field to be omitted: %#v", body)
	}
}

func TestValidatePropertyLinks(t *testing.T) {
	cases := []struct {
		name    string
		raw     any
		wantErr bool
	}{
		{"absent", nil, false},
		{"one field", []any{map[string]any{"key": "done", "checkbox": false}}, false},
		{"empty array clears", []any{map[string]any{"key": "tag", "multi_select": []any{}}}, false},
		{"no value field", []any{map[string]any{"key": "tag"}}, true},
		{"value instead of a typed field", []any{map[string]any{"key": "tag", "value": nil}}, true},
		// The one heart accepts and half-executes: it would keep text and drop
		// number, and answer 200.
		{"two value fields", []any{map[string]any{"key": "foo", "text": "abc", "number": 123}}, true},
		{"not a list", map[string]any{"key": "tag"}, true},
		{"entry not an object", []any{"tag"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePropertyLinks(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("expected an error for %#v", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected %#v to pass, got %v", tc.raw, err)
			}
		})
	}
}

func TestUpdateObjectRequestBodyRejectsAmbiguousProperty(t *testing.T) {
	_, err := updateObjectRequestBody(map[string]any{
		"properties": []any{map[string]any{"key": "foo", "text": "abc", "number": 123}},
	})
	if err == nil {
		t.Fatal("expected two typed value fields to be refused before the request is sent")
	}
}

func TestCreateObjectRequestBodyRejectsValuelessProperty(t *testing.T) {
	_, err := createObjectRequestBody(map[string]any{
		"type_key":   "page",
		"properties": []any{map[string]any{"key": "tag"}},
	})
	if err == nil {
		t.Fatal("expected a property without a typed value field to be refused")
	}
}
