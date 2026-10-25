package main

import "testing"

func TestRequestedIconFormatReadsFormatOrNothing(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no icon at all", map[string]any{"name": "Shenzhen"}, ""},
		{"explicit null icon", map[string]any{"icon": nil}, ""},
		{"emoji", map[string]any{"icon": map[string]any{"format": "emoji", "emoji": "🚇"}}, "emoji"},
		{"file", map[string]any{"icon": map[string]any{"format": "file", "file": "obj-id"}}, "file"},
		{"named", map[string]any{"icon": map[string]any{"format": "icon", "name": "book"}}, "icon"},
		// Removing an icon is not a format to verify: the target legitimately
		// ends up showing something else, or nothing.
		{"emoji cleared", map[string]any{"icon": map[string]any{"format": "emoji", "emoji": ""}}, ""},
		{"named cleared", map[string]any{"icon": map[string]any{"format": "icon", "name": ""}}, ""},
	}
	for _, tc := range cases {
		if got := requestedIconFormat(tc.args); got != tc.want {
			t.Errorf("%s: requestedIconFormat = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The API answers a file icon with a gateway URL rather than the file object id
// that was sent, so only the format can be compared. Comparing the whole icon
// would report every file icon as failed and trigger a pointless repair.
func TestReportedIconFormatIgnoresTheFileURL(t *testing.T) {
	obj := map[string]any{"icon": map[string]any{
		"format": "file",
		"file":   "http://127.0.0.1:31012/v1/spaces/space-id/files/file-object-id",
	}}
	if got := reportedIconFormat(obj); got != "file" {
		t.Fatalf("reportedIconFormat = %q, want \"file\"", got)
	}
	if got := reportedIconFormat(map[string]any{"name": "Shenzhen"}); got != "" {
		t.Fatalf("object without an icon: reportedIconFormat = %q, want \"\"", got)
	}
}

// A call that carried no icon must not reach for the network at all.
func TestApplyIconDoesNothingWhenNoIconWasRequested(t *testing.T) {
	s := &mcpServer{}
	target := iconTarget{
		objectID: "obj-id",
		reread: func() (map[string]any, error) {
			t.Fatal("applyIcon must not re-read when no icon was requested")
			return nil, nil
		},
	}
	obj := map[string]any{"icon": map[string]any{"format": "emoji", "emoji": "🚇"}}

	if _, applied, note := s.applyIcon(target, "", obj); !applied || note != "" {
		t.Fatalf("no icon requested: applied=%v note=%q, want true and no note", applied, note)
	}
}

// The icon the caller asked for is the one showing, but the losing relations
// could not be cleared. That is a stale leftover, not a failed write: the
// result says so without claiming the icon is missing.
func TestApplyIconStaysAppliedWhenOnlyTheCleanupFails(t *testing.T) {
	// A zero-value server has no session token, so the gRPC client fails to
	// build — the same path an unavailable connection takes.
	s := &mcpServer{}
	target := iconTarget{
		objectID: "obj-id",
		reread: func() (map[string]any, error) {
			t.Fatal("nothing visible changed, so there is nothing to re-read")
			return nil, nil
		},
	}
	obj := map[string]any{"icon": map[string]any{"format": "emoji", "emoji": "🚇"}}

	got, applied, note := s.applyIcon(target, "emoji", obj)
	if !applied {
		t.Fatal("the requested icon is in effect, so it must be reported as applied")
	}
	if note == "" {
		t.Fatal("expected a note about the icon that could not be cleared")
	}
	if reportedIconFormat(got) != "emoji" {
		t.Fatalf("expected the object to come back unchanged, got %#v", got)
	}
	if fields := iconFields(note, applied); fields["icon_applied"] != nil {
		t.Fatalf("a stale leftover must not report icon_applied=false, got %#v", fields)
	}
}

func TestIconRemoval(t *testing.T) {
	cases := []struct {
		args map[string]any
		want bool
	}{
		{map[string]any{"icon": nil}, true},
		{map[string]any{"icon": map[string]any{"format": "emoji", "emoji": ""}}, true},
		{map[string]any{"icon": map[string]any{"format": "icon", "name": ""}}, true},
		{map[string]any{"icon": map[string]any{"format": "file", "file": ""}}, true},
		{map[string]any{"icon": map[string]any{"format": "emoji", "emoji": "x"}}, false},
		{map[string]any{"icon": map[string]any{"format": "other"}}, false},
		{map[string]any{}, false},
	}
	for _, c := range cases {
		if got := iconRemoval(c.args); got != c.want {
			t.Errorf("iconRemoval(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestIconFailureIsEmptyOnSuccess(t *testing.T) {
	if fields := iconFields("", true); len(fields) != 0 {
		t.Fatalf("a successful icon apply must add no fields, got %#v", fields)
	}
	fields := iconFields("hidden by the previous emoji", false)
	if fields["icon_applied"] != false {
		t.Fatalf("expected icon_applied=false, got %#v", fields["icon_applied"])
	}
	if fields["icon_warning"] != "hidden by the previous emoji" {
		t.Fatalf("expected the note to be carried through, got %#v", fields["icon_warning"])
	}
}
