package anytypefiles

import (
	"reflect"
	"testing"
)

func ids(blocks []BlockInfo) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.ID)
	}
	return out
}

// heart lists the root's children before their content; the page shows each
// column's content before the next column.
func TestDocumentOrderIsDepthFirst(t *testing.T) {
	blocks := []BlockInfo{
		{ID: "root", ChildrenIDs: []string{"header", "col1", "col2"}},
		{ID: "header", ChildrenIDs: []string{"title"}},
		{ID: "col1", ChildrenIDs: []string{"h1", "a"}},
		{ID: "col2", ChildrenIDs: []string{"h2", "b"}},
		{ID: "title"},
		{ID: "h1"}, {ID: "a", ChildrenIDs: []string{"a1"}}, {ID: "a1"},
		{ID: "h2"}, {ID: "b"},
	}
	got := ids(documentOrder(blocks, "root"))
	want := []string{"root", "header", "title", "col1", "h1", "a", "a1", "col2", "h2", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Nothing is lost: blocks the walk cannot reach, children that do not exist
// and a cycle all leave the result complete and finite.
func TestDocumentOrderKeepsEveryBlockOnce(t *testing.T) {
	blocks := []BlockInfo{
		{ID: "orphan"},
		{ID: "root", ChildrenIDs: []string{"x", "missing", "root"}},
		{ID: "x", ChildrenIDs: []string{"root"}},
	}
	got := ids(documentOrder(blocks, "root"))
	want := []string{"root", "x", "orphan"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := ids(documentOrder(blocks, "")); !reflect.DeepEqual(got, []string{"orphan", "root", "x"}) {
		t.Fatalf("without a root the original order must stay, got %v", got)
	}
}
