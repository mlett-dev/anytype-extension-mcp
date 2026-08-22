package anytypefiles

import "testing"

// Round-trip: every condition and empty-placement a read reports must be a
// spelling the write side accepts, and must survive unchanged.
func TestFilterConditionRoundTrip(t *testing.T) {
	for _, value := range filterConditions {
		reported := canonicalName(filterConditionNames, value, value.String())
		back, err := lookupEnum(filterConditions, reported, "filter condition", false)
		if err != nil {
			t.Fatalf("condition %v reported as %q, which does not write back: %v", value, reported, err)
		}
		if back != value {
			t.Fatalf("condition %v reported as %q, which reads back as %v", value, reported, back)
		}
	}
	for _, value := range sortEmptyTypes {
		reported := canonicalName(sortEmptyTypeNames, value, value.String())
		back, err := lookupEnum(sortEmptyTypes, reported, "empty placement", false)
		if err != nil {
			t.Fatalf("empty placement %v reported as %q, which does not write back: %v", value, reported, err)
		}
		if back != value {
			t.Fatalf("empty placement %v reported as %q, which reads back as %v", value, reported, back)
		}
	}
}

// The protobuf spellings an older query-inspect handed out must still write.
func TestLegacyProtobufSpellingsStillAccepted(t *testing.T) {
	for _, raw := range []string{"notequal", "notin", "notempty", "notlike",
		"greaterorequal", "lessorequal", "allin", "notallin", "exactin", "notexactin"} {
		if _, err := lookupEnum(filterConditions, raw, "filter condition", false); err != nil {
			t.Errorf("legacy condition %q rejected: %v", raw, err)
		}
	}
	if _, err := lookupEnum(sortEmptyTypes, "notspecified", "empty placement", false); err != nil {
		t.Errorf("legacy empty placement rejected: %v", err)
	}
}

// Every advertised condition must be its own canonical spelling.
//
// This is the guard against an alias being added straight to the
// filterConditions literal the way viewTypes carries "grid". canonicalNames
// inverts the map, so a second name for one value would make the reported
// spelling depend on map iteration order — the read side would then flip
// between not_equal and its alias between runs. Pinning every name in
// FilterConditionNames catches that, where pinning a handful by hand would not.
func TestEveryAdvertisedNameIsItsOwnCanonicalSpelling(t *testing.T) {
	for _, name := range FilterConditionNames() {
		value, err := lookupEnum(filterConditions, name, "filter condition", false)
		if err != nil {
			t.Fatalf("FilterConditionNames advertises %q, which the table does not know: %v", name, err)
		}
		if got := canonicalName(filterConditionNames, value, ""); got != name {
			t.Errorf("condition %q is reported as %q — filterConditions has two names for one value", name, got)
		}
	}
	for _, name := range []string{"not_specified", "start", "end"} {
		value, err := lookupEnum(sortEmptyTypes, name, "empty placement", false)
		if err != nil {
			t.Fatalf("empty placement %q unknown: %v", name, err)
		}
		if got := canonicalName(sortEmptyTypeNames, value, ""); got != name {
			t.Errorf("empty placement %q is reported as %q", name, got)
		}
	}
}

// The aliases must never displace a canonical name. canonicalNames runs as a
// package-level var and protobufAliases in init(), which Go guarantees to run
// afterwards — this pins that ordering, because reversing it would silently
// make "notequal" the reported spelling again.
func TestAliasesDoNotBecomeCanonical(t *testing.T) {
	for _, alias := range []string{"notequal", "notempty", "notin"} {
		value, err := lookupEnum(filterConditions, alias, "filter condition", false)
		if err != nil {
			t.Fatalf("alias %q is not accepted: %v", alias, err)
		}
		if got := canonicalName(filterConditionNames, value, ""); got == alias {
			t.Errorf("alias %q became the reported spelling", alias)
		}
	}
	value, err := lookupEnum(sortEmptyTypes, "notspecified", "empty placement", false)
	if err != nil {
		t.Fatalf("alias notspecified is not accepted: %v", err)
	}
	if got := canonicalName(sortEmptyTypeNames, value, ""); got == "notspecified" {
		t.Error("alias notspecified became the reported spelling")
	}
}

// Relation formats must NOT be folded: text and longtext are different formats.
func TestRelationFormatsUntouched(t *testing.T) {
	short, _ := lookupEnum(relationFormats, "text", "format", false)
	long, _ := lookupEnum(relationFormats, "longtext", "format", false)
	if short == long {
		t.Fatal("text and longtext must stay distinct formats")
	}
	// Every format a read reports (the protobuf name) must write back.
	for _, value := range relationFormats {
		if _, err := lookupEnum(relationFormats, value.String(), "format", false); err != nil {
			t.Errorf("format %q does not write back: %v", value.String(), err)
		}
	}
}

// Every format a read can report must be offered by the filter/sort schemas,
// or a schema-validating client cannot write back what it just read.
func TestFilterFormatNamesCoverEverythingAReadEmits(t *testing.T) {
	offered := make(map[string]bool)
	for _, name := range FilterFormatNames() {
		offered[name] = true
	}
	for _, value := range relationFormats {
		emitted := value.String()
		if !offered[emitted] {
			t.Errorf("a read reports format %q, which the filter schema does not offer", emitted)
		}
		if _, err := lookupEnum(relationFormats, emitted, "format", false); err != nil {
			t.Errorf("format %q does not write back: %v", emitted, err)
		}
	}
}

// The REST vocabulary must stay usable too: both spellings work.
func TestFilterFormatNamesKeepRESTSpellings(t *testing.T) {
	offered := make(map[string]bool)
	for _, name := range FilterFormatNames() {
		offered[name] = true
	}
	for _, name := range []string{"objects", "select", "multi_select", "text", "files"} {
		if !offered[name] {
			t.Errorf("REST spelling %q is no longer offered", name)
		}
	}
}
