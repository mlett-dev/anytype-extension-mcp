package anytypefiles

import (
	"strings"
	"testing"

	"github.com/gogo/protobuf/types"

	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// The GUI clamps, snaps onto multiples of 400 within 10 px and floors; a
// width the tool stores has to be the width the GUI shows.
func TestTableColumnDisplayWidthMatchesTheGUI(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{30, 50}, {50, 50}, {140, 140}, {260.7, 260},
		{389, 389}, {390, 400}, {395, 400}, {410, 400}, {411, 411},
		{1995, 2000}, {2500, 2000},
	}
	for _, tc := range cases {
		if got := tableColumnDisplayWidth(tc.in); got != tc.want {
			t.Errorf("tableColumnDisplayWidth(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func numberField(v float64) *types.Struct {
	return &types.Struct{Fields: map[string]*types.Value{
		"width": {Kind: &types.Value_NumberValue{NumberValue: v}},
	}}
}

// A missing width is the default, but a stored 0 is not: the GUI only falls
// back on null and shows 0 as the minimum.
func TestTableColumnWidthPx(t *testing.T) {
	if _, ok := tableColumnWidthPx(nil); ok {
		t.Error("no fields reported as a width of its own")
	}
	text := &types.Struct{Fields: map[string]*types.Value{
		"width": {Kind: &types.Value_StringValue{StringValue: "wide"}},
	}}
	if _, ok := tableColumnWidthPx(text); ok {
		t.Error("a non-numeric width reported as a width")
	}
	if got, ok := tableColumnWidthPx(numberField(0)); !ok || got != TableColumnMinWidth {
		t.Errorf("stored 0 = (%d, %v), want (%d, true)", got, ok, TableColumnMinWidth)
	}
	if got, ok := tableColumnWidthPx(numberField(260)); !ok || got != 260 {
		t.Errorf("stored 260 = (%d, %v), want (260, true)", got, ok)
	}
}

func layoutBlock(id string, style model.BlockContentLayoutStyle, children ...string) *model.Block {
	return &model.Block{Id: id, ChildrenIds: children,
		Content: &model.BlockContentOfLayout{Layout: &model.BlockContentLayout{Style: style}}}
}

// tableInPageColumn is a page row whose first column holds a 2x1 table. The
// first table column has a width of its own, the second has none.
func tableInPageColumn() []*model.Block {
	return []*model.Block{
		layoutBlock("row", model.BlockContentLayout_Row, "col-a", "col-b"),
		layoutBlock("col-a", model.BlockContentLayout_Column, "table"),
		layoutBlock("col-b", model.BlockContentLayout_Column, "text"),
		{Id: "text", Content: &model.BlockContentOfText{Text: &model.BlockContentText{}}},
		{Id: "table", ChildrenIds: []string{"cols", "rows"},
			Content: &model.BlockContentOfTable{Table: &model.BlockContentTable{}}},
		layoutBlock("cols", model.BlockContentLayout_TableColumns, "c1", "c2"),
		{Id: "c1", Fields: numberField(260),
			Content: &model.BlockContentOfTableColumn{TableColumn: &model.BlockContentTableColumn{}}},
		{Id: "c2", Content: &model.BlockContentOfTableColumn{TableColumn: &model.BlockContentTableColumn{}}},
		layoutBlock("rows", model.BlockContentLayout_TableRows, "r1"),
		{Id: "r1", ChildrenIds: []string{"r1-c1"},
			Content: &model.BlockContentOfTableRow{TableRow: &model.BlockContentTableRow{}}},
		{Id: "r1-c1", Content: &model.BlockContentOfText{Text: &model.BlockContentText{}}},
	}
}

func TestBuildTableInfoReportsColumnWidths(t *testing.T) {
	var blocks []BlockInfo
	for _, b := range tableInPageColumn() {
		blocks = append(blocks, blockFromModel(b))
	}
	info, err := buildTableInfo(blocks, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Columns) != 2 || info.Columns[0].WidthPx != 260 || info.Columns[1].WidthPx != 0 {
		t.Errorf("columns = %+v, want widths [260, none]", info.Columns)
	}
}

// block-column-width must not reach the page row through a table's own blocks:
// pixel widths would be stored there as shares.
func TestFindLayoutRowRefusesTableParts(t *testing.T) {
	byID := map[string]*model.Block{}
	for _, b := range tableInPageColumn() {
		byID[b.Id] = b
	}
	for _, id := range []string{"table", "cols", "c1", "rows", "r1"} {
		_, err := findLayoutRow(byID, id)
		if err == nil || !strings.Contains(err.Error(), "table-column-width") {
			t.Errorf("%s: err = %v, want a pointer to table-column-width", id, err)
		}
	}
	// Ordinary blocks in the row, a cell's text included, still find it.
	for _, id := range []string{"text", "col-a", "r1-c1"} {
		if row, err := findLayoutRow(byID, id); err != nil || row != "row" {
			t.Errorf("%s: (%q, %v), want the page row", id, row, err)
		}
	}

	// A table outside any page row: the cell finds no row, and the error
	// names the tool that sizes table columns.
	delete(byID, "row")
	delete(byID, "col-a")
	if _, err := findLayoutRow(byID, "r1-c1"); err == nil || !strings.Contains(err.Error(), "table-column-width") {
		t.Errorf("cell outside a page row: err = %v, want a pointer to table-column-width", err)
	}
}
