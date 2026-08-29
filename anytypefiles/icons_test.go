package anytypefiles

import "testing"

// Every relation that does not belong to the written format has to go, not
// just the ones that outrank it: a relation that merely loses is still stored
// and reappears once the icon above it is removed.
func TestIconRelationsToClearCoversEveryCompetingRelation(t *testing.T) {
	cases := []struct {
		format string
		want   []string
	}{
		{IconFormatNamed, []string{"iconEmoji", "iconImage"}},
		{IconFormatEmoji, []string{"iconName", "iconOption", "iconImage"}},
		{IconFormatFile, []string{"iconName", "iconOption", "iconEmoji"}},
	}
	for _, tc := range cases {
		got := iconRelationsToClear[tc.format]
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.format, got, tc.want)
		}
		for i, key := range tc.want {
			if got[i] != key {
				t.Fatalf("%s: got %v, want %v", tc.format, got, tc.want)
			}
		}
	}
}

func TestIconFormatKnown(t *testing.T) {
	for _, format := range []string{IconFormatEmoji, IconFormatFile, IconFormatNamed} {
		if !IconFormatKnown(format) {
			t.Errorf("%q should be a known icon format", format)
		}
	}
	if IconFormatKnown("sticker") {
		t.Error("an unknown format must not claim to be known")
	}
}
