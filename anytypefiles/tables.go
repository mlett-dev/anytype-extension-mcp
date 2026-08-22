package anytypefiles

// Table editing for Anytype objects.
//
// BlockTableCreate (see blocks.go) only produces an empty grid. Everything the
// GUI offers afterwards — filling cells, inserting and removing rows and
// columns, marking a header row, sorting — lives behind the dedicated
// BlockTable* RPCs and is implemented here.
//
// Structure of a table, as heart stores it:
//
//	table (BlockContentOfTable)
//	├── layout style=table_columns  → children are the column blocks
//	└── layout style=table_rows     → children are the row blocks
//	                                   └── children are the cell blocks
//
// The two child layouts are told apart by their style, not by their order.
// A cell block's id is always rowID + "-" + colID, and a cell only exists once
// the row has been filled; empty rows legitimately have no children at all.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// tableCellSeparator joins a row id and a column id into a cell id. It mirrors
// table.TableCellSeparator in anytype-heart, which is not importable here.
//
// Cell ids are only ever CONSTRUCTED from a row and column id, never parsed
// back: row and column ids contain "-" themselves, so splitting is ambiguous.
const tableCellSeparator = "-"

// TableCell is one cell of a table.
//
// Exists reports whether the cell block is actually present. Anytype only
// materialises a cell once something is written to it, and it removes emptied
// cells again on cleanup, so a perfectly healthy table can have holes. The full
// grid is reported either way, with Exists=false for the holes: callers get a
// stable row x column shape, and the id is the one the cell will have once it
// is written.
type TableCell struct {
	ID     string `json:"id"`
	Row    int    `json:"row"`
	Column int    `json:"column"`
	Text   string `json:"text,omitempty"`
	Exists bool   `json:"exists"`
}

// TableLine is a row or a column of a table.
type TableLine struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	IsHeader bool   `json:"is_header,omitempty"`
}

// TableInfo is the resolved structure of one table block.
type TableInfo struct {
	BlockID  string      `json:"block_id"`
	Rows     []TableLine `json:"rows"`
	Columns  []TableLine `json:"columns"`
	Cells    []TableCell `json:"cells"`
	RowCount int         `json:"row_count"`
	ColCount int         `json:"column_count"`
}

// rowID returns the id of the row at the given zero-based index.
func (t TableInfo) rowID(index int) (string, error) {
	if index < 0 || index >= len(t.Rows) {
		return "", fmt.Errorf("row %d is out of range; table has %d rows (0-%d)", index, len(t.Rows), len(t.Rows)-1)
	}
	return t.Rows[index].ID, nil
}

// colID returns the id of the column at the given zero-based index.
func (t TableInfo) colID(index int) (string, error) {
	if index < 0 || index >= len(t.Columns) {
		return "", fmt.Errorf("column %d is out of range; table has %d columns (0-%d)", index, len(t.Columns), len(t.Columns)-1)
	}
	return t.Columns[index].ID, nil
}

// lineID resolves a row or column that the caller gave either as a block id or
// as a zero-based index. Models reliably produce indices; ids they have to have
// seen first, so both are accepted everywhere.
//
// param is the caller-facing parameter name ("target", "row", "column", ...).
// It cannot be derived from axis: the same axis is reached through "target" in
// table-insert and through "row" in table-set-cells, and an error message that
// names a key the schema does not accept sends the model into a retry loop.
func (t TableInfo) lineID(axis, param, raw string, index int, indexGiven bool) (string, error) {
	if strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw), nil
	}
	if !indexGiven {
		return "", fmt.Errorf("either %s_id or %s (index) is required", param, param)
	}
	if axis == "row" {
		return t.rowID(index)
	}
	return t.colID(index)
}

// InspectTable resolves a table block into rows, columns and cells.
func (c *Client) InspectTable(ctx context.Context, spaceID, objectID, tableBlockID string) (TableInfo, error) {
	blocks, _, err := c.ReadBlocks(ctx, spaceID, objectID)
	if err != nil {
		return TableInfo{}, err
	}
	return buildTableInfo(blocks, tableBlockID)
}

func buildTableInfo(blocks []BlockInfo, tableBlockID string) (TableInfo, error) {
	byID := make(map[string]BlockInfo, len(blocks))
	for _, b := range blocks {
		byID[b.ID] = b
	}

	// An omitted block id is only unambiguous when the object holds exactly one
	// table; guessing between several would silently edit the wrong one.
	tableBlockID = strings.TrimSpace(tableBlockID)
	if tableBlockID == "" {
		var found []string
		for _, b := range blocks {
			if b.Kind == "table" {
				found = append(found, b.ID)
			}
		}
		switch len(found) {
		case 0:
			return TableInfo{}, errors.New("this object contains no table block")
		case 1:
			tableBlockID = found[0]
		default:
			return TableInfo{}, fmt.Errorf("this object contains %d tables; pass table_block_id (candidates: %s)",
				len(found), strings.Join(found, ", "))
		}
	}

	table, ok := byID[tableBlockID]
	if !ok {
		return TableInfo{}, fmt.Errorf("block %q not found in this object", tableBlockID)
	}
	if table.Kind != "table" {
		return TableInfo{}, fmt.Errorf("block %q is a %s block, not a table", tableBlockID, table.Kind)
	}

	info := TableInfo{BlockID: tableBlockID}
	var rowIDs []string
	for _, childID := range table.ChildrenIDs {
		child, ok := byID[childID]
		if !ok {
			continue
		}
		switch child.Style {
		case "tablecolumns":
			for i, colID := range child.ChildrenIDs {
				info.Columns = append(info.Columns, TableLine{ID: colID, Index: i})
			}
		case "tablerows":
			rowIDs = child.ChildrenIDs
		}
	}

	for i, rowID := range rowIDs {
		row := byID[rowID]
		info.Rows = append(info.Rows, TableLine{ID: rowID, Index: i, IsHeader: row.IsHeader})
		// Cells are matched by column id rather than by position: a row that has
		// never been filled has no cells at all, and a partially filled row has
		// gaps, so index arithmetic over ChildrenIDs would misalign columns.
		// Missing cells are still reported, flagged with Exists=false.
		for colIdx, col := range info.Columns {
			cellID := rowID + tableCellSeparator + col.ID
			cell, ok := byID[cellID]
			info.Cells = append(info.Cells, TableCell{
				ID: cellID, Row: i, Column: colIdx, Text: cell.Text, Exists: ok,
			})
		}
	}
	info.RowCount = len(info.Rows)
	info.ColCount = len(info.Columns)
	return info, nil
}

// fillRows makes sure the given rows have a cell block for every column.
// BlockTableRowListFill is idempotent, so it is safe to call before any write.
func (c *Client) fillRows(ctx context.Context, objectID string, rowIDs []string) error {
	if len(rowIDs) == 0 {
		return nil
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockTableRowListFill(callCtx, &pb.RpcBlockTableRowListFillRequest{
		ContextId: objectID, BlockIds: rowIDs,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockTableRowListFill failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableRowListFillResponseError_NULL {
		return fmt.Errorf("BlockTableRowListFill error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// CellWrite is one requested cell update, addressed by index or by id.
type CellWrite struct {
	Row         int    `json:"row"`
	Column      int    `json:"column"`
	RowID       string `json:"row_id,omitempty"`
	ColumnID    string `json:"column_id,omitempty"`
	Text        string `json:"text"`
	RowGiven    bool   `json:"-"`
	ColumnGiven bool   `json:"-"`
}

// SetCells writes text into table cells, creating missing cell blocks first.
// This is the operation that actually fills a table; BlockTableCreate alone
// leaves a grid whose cells do not exist yet.
func (c *Client) SetCells(ctx context.Context, spaceID, objectID, tableBlockID string, writes []CellWrite) (TableInfo, []string, error) {
	if len(writes) == 0 {
		return TableInfo{}, nil, errors.New("cells is required")
	}
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, nil, err
	}

	type resolved struct {
		cellID string
		text   string
	}
	items := make([]resolved, 0, len(writes))
	rowSet := make(map[string]bool)
	rowOrder := make([]string, 0, len(writes))
	for i, w := range writes {
		rowID, err := info.lineID("row", "row", w.RowID, w.Row, w.RowGiven)
		if err != nil {
			return TableInfo{}, nil, fmt.Errorf("cells[%d]: %w", i, err)
		}
		colID, err := info.lineID("column", "column", w.ColumnID, w.Column, w.ColumnGiven)
		if err != nil {
			return TableInfo{}, nil, fmt.Errorf("cells[%d]: %w", i, err)
		}
		items = append(items, resolved{cellID: rowID + tableCellSeparator + colID, text: w.Text})
		if !rowSet[rowID] {
			rowSet[rowID] = true
			rowOrder = append(rowOrder, rowID)
		}
	}

	if err := c.fillRows(ctx, objectID, rowOrder); err != nil {
		return TableInfo{}, nil, err
	}

	// Each write is left pending and the whole batch is committed once, rather
	// than closing and reopening the object for every single cell.
	written := make([]string, 0, len(items))
	for _, item := range items {
		if err := c.setBlockTextPending(ctx, objectID, item.cellID, item.text, nil); err != nil {
			return TableInfo{}, nil, fmt.Errorf("cell %s: %w", item.cellID, err)
		}
		written = append(written, item.cellID)
	}
	if err := c.FlushTextChanges(ctx, spaceID, objectID); err != nil {
		return TableInfo{}, written, err
	}

	info, err = c.InspectTable(ctx, spaceID, objectID, info.BlockID)
	if err != nil {
		return TableInfo{}, written, err
	}
	return info, written, nil
}

// InsertLines adds rows or columns to a table.
//
// With no target the lines are appended at the end via BlockTableExpand; with a
// target they are inserted relative to it via the Row/Column create RPCs.
func (c *Client) InsertLines(ctx context.Context, spaceID, objectID, tableBlockID, axis, targetRaw string, targetIndex int, targetGiven bool, position string, count int) (TableInfo, error) {
	if count <= 0 {
		count = 1
	}
	if err := checkAxis(axis); err != nil {
		return TableInfo{}, err
	}
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, err
	}

	if strings.TrimSpace(targetRaw) == "" && !targetGiven {
		callCtx, cancel := c.contextWithAuth(ctx)
		req := &pb.RpcBlockTableExpandRequest{ContextId: objectID, TargetId: info.BlockID}
		if axis == "row" {
			req.Rows = uint32(count)
		} else {
			req.Columns = uint32(count)
		}
		resp, err := c.rpc.BlockTableExpand(callCtx, req)
		cancel()
		if err != nil {
			return TableInfo{}, fmt.Errorf("gRPC BlockTableExpand failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableExpandResponseError_NULL {
			return TableInfo{}, fmt.Errorf("BlockTableExpand error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		return c.InspectTable(ctx, spaceID, objectID, info.BlockID)
	}

	targetID, err := info.lineID(axis, "target", targetRaw, targetIndex, targetGiven)
	if err != nil {
		return TableInfo{}, err
	}
	pos, err := tablePosition(axis, position)
	if err != nil {
		return TableInfo{}, err
	}

	for i := 0; i < count; i++ {
		callCtx, cancel := c.contextWithAuth(ctx)
		if axis == "row" {
			resp, err := c.rpc.BlockTableRowCreate(callCtx, &pb.RpcBlockTableRowCreateRequest{
				ContextId: objectID, TargetId: targetID, Position: pos,
			})
			cancel()
			if err != nil {
				return TableInfo{}, fmt.Errorf("gRPC BlockTableRowCreate failed: %w", err)
			}
			if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableRowCreateResponseError_NULL {
				return TableInfo{}, fmt.Errorf("BlockTableRowCreate error (%s): %s", resp.Error.Code, resp.Error.Description)
			}
		} else {
			resp, err := c.rpc.BlockTableColumnCreate(callCtx, &pb.RpcBlockTableColumnCreateRequest{
				ContextId: objectID, TargetId: targetID, Position: pos,
			})
			cancel()
			if err != nil {
				return TableInfo{}, fmt.Errorf("gRPC BlockTableColumnCreate failed: %w", err)
			}
			if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableColumnCreateResponseError_NULL {
				return TableInfo{}, fmt.Errorf("BlockTableColumnCreate error (%s): %s", resp.Error.Code, resp.Error.Description)
			}
		}
	}
	return c.InspectTable(ctx, spaceID, objectID, info.BlockID)
}

// DeleteLines removes rows or columns, addressed by id or by index.
//
// All indices are resolved to block ids up front, against one snapshot of the
// table. Deleting several lines therefore behaves as the caller intended: the
// indices refer to the table as it looked before the first deletion shifted it.
func (c *Client) DeleteLines(ctx context.Context, spaceID, objectID, tableBlockID, axis string, ids []string, indices []int) (TableInfo, []string, error) {
	if err := checkAxis(axis); err != nil {
		return TableInfo{}, nil, err
	}
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, nil, err
	}

	targets := make([]string, 0, len(ids)+len(indices))
	seen := make(map[string]bool)
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			targets = append(targets, id)
		}
	}
	for _, id := range ids {
		add(strings.TrimSpace(id))
	}
	for _, idx := range indices {
		var id string
		if axis == "row" {
			id, err = info.rowID(idx)
		} else {
			id, err = info.colID(idx)
		}
		if err != nil {
			return TableInfo{}, nil, err
		}
		add(id)
	}
	if len(targets) == 0 {
		return TableInfo{}, nil, fmt.Errorf("pass ids (%s block ids) or indices (zero-based %s numbers)", axis, axis)
	}

	for _, id := range targets {
		callCtx, cancel := c.contextWithAuth(ctx)
		if axis == "row" {
			resp, err := c.rpc.BlockTableRowDelete(callCtx, &pb.RpcBlockTableRowDeleteRequest{
				ContextId: objectID, TargetId: id,
			})
			cancel()
			if err != nil {
				return TableInfo{}, nil, fmt.Errorf("gRPC BlockTableRowDelete failed: %w", err)
			}
			if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableRowDeleteResponseError_NULL {
				return TableInfo{}, nil, fmt.Errorf("BlockTableRowDelete error (%s): %s", resp.Error.Code, resp.Error.Description)
			}
		} else {
			resp, err := c.rpc.BlockTableColumnDelete(callCtx, &pb.RpcBlockTableColumnDeleteRequest{
				ContextId: objectID, TargetId: id,
			})
			cancel()
			if err != nil {
				return TableInfo{}, nil, fmt.Errorf("gRPC BlockTableColumnDelete failed: %w", err)
			}
			if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableColumnDeleteResponseError_NULL {
				return TableInfo{}, nil, fmt.Errorf("BlockTableColumnDelete error (%s): %s", resp.Error.Code, resp.Error.Description)
			}
		}
	}
	info, err = c.InspectTable(ctx, spaceID, objectID, info.BlockID)
	return info, targets, err
}

// DuplicateLine copies one row or column.
func (c *Client) DuplicateLine(ctx context.Context, spaceID, objectID, tableBlockID, axis, targetRaw string, targetIndex int, targetGiven bool, position string) (TableInfo, error) {
	if err := checkAxis(axis); err != nil {
		return TableInfo{}, err
	}
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, err
	}
	targetID, err := info.lineID(axis, "target", targetRaw, targetIndex, targetGiven)
	if err != nil {
		return TableInfo{}, err
	}
	pos, err := tablePosition(axis, position)
	if err != nil {
		return TableInfo{}, err
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	// TargetId is where the copy lands, BlockId is what gets copied; duplicating
	// a line in place means passing the same id for both.
	if axis == "row" {
		resp, err := c.rpc.BlockTableRowDuplicate(callCtx, &pb.RpcBlockTableRowDuplicateRequest{
			ContextId: objectID, TargetId: targetID, BlockId: targetID, Position: pos,
		})
		if err != nil {
			return TableInfo{}, fmt.Errorf("gRPC BlockTableRowDuplicate failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableRowDuplicateResponseError_NULL {
			return TableInfo{}, fmt.Errorf("BlockTableRowDuplicate error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	} else {
		resp, err := c.rpc.BlockTableColumnDuplicate(callCtx, &pb.RpcBlockTableColumnDuplicateRequest{
			ContextId: objectID, TargetId: targetID, BlockId: targetID, Position: pos,
		})
		if err != nil {
			return TableInfo{}, fmt.Errorf("gRPC BlockTableColumnDuplicate failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableColumnDuplicateResponseError_NULL {
			return TableInfo{}, fmt.Errorf("BlockTableColumnDuplicate error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	return c.InspectTable(ctx, spaceID, objectID, info.BlockID)
}

// MoveLine reorders a row or a column.
//
// Columns have their own RPC. Rows do not: they are reordered through the
// generic block move, which heart explicitly permits between rows of the same
// table (and only for position top or bottom).
func (c *Client) MoveLine(ctx context.Context, spaceID, objectID, tableBlockID, axis, targetRaw string, targetIndex int, targetGiven bool, dropRaw string, dropIndex int, dropGiven bool, position string) (TableInfo, error) {
	if err := checkAxis(axis); err != nil {
		return TableInfo{}, err
	}
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, err
	}
	targetID, err := info.lineID(axis, "target", targetRaw, targetIndex, targetGiven)
	if err != nil {
		return TableInfo{}, err
	}
	dropID, err := info.lineID(axis, "drop_target", dropRaw, dropIndex, dropGiven)
	if err != nil {
		return TableInfo{}, err
	}
	pos, err := tablePosition(axis, position)
	if err != nil {
		return TableInfo{}, err
	}

	if axis == "column" {
		callCtx, cancel := c.contextWithAuth(ctx)
		defer cancel()
		resp, err := c.rpc.BlockTableColumnMove(callCtx, &pb.RpcBlockTableColumnMoveRequest{
			ContextId: objectID, TargetId: targetID, DropTargetId: dropID, Position: pos,
		})
		if err != nil {
			return TableInfo{}, fmt.Errorf("gRPC BlockTableColumnMove failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableColumnMoveResponseError_NULL {
			return TableInfo{}, fmt.Errorf("BlockTableColumnMove error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		return c.InspectTable(ctx, spaceID, objectID, info.BlockID)
	}

	rowPosition := "bottom"
	if pos == model.Block_Top {
		rowPosition = "top"
	}
	if err := c.MoveBlocks(ctx, objectID, []string{targetID}, objectID, dropID, rowPosition); err != nil {
		return TableInfo{}, err
	}
	return c.InspectTable(ctx, spaceID, objectID, info.BlockID)
}

// SetRowHeader marks rows as header rows or turns that off again.
func (c *Client) SetRowHeader(ctx context.Context, spaceID, objectID, tableBlockID string, ids []string, indices []int, isHeader bool) (TableInfo, []string, error) {
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, nil, err
	}
	targets, err := resolveRowTargets(info, ids, indices)
	if err != nil {
		return TableInfo{}, nil, err
	}
	for _, id := range targets {
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockTableRowSetHeader(callCtx, &pb.RpcBlockTableRowSetHeaderRequest{
			ContextId: objectID, TargetId: id, IsHeader: isHeader,
		})
		cancel()
		if err != nil {
			return TableInfo{}, nil, fmt.Errorf("gRPC BlockTableRowSetHeader failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableRowSetHeaderResponseError_NULL {
			return TableInfo{}, nil, fmt.Errorf("BlockTableRowSetHeader error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	info, err = c.InspectTable(ctx, spaceID, objectID, info.BlockID)
	return info, targets, err
}

// ClearRows empties the cells of rows without removing the rows themselves.
//
// This does NOT use BlockTableRowListClean. Despite the name that RPC does not
// clear anything: it unlinks cell blocks that are *already* empty, which is a
// tidy-up pass, not the GUI's "clear contents". Verified against anytype-heart
// v0.50.8. Clearing is therefore an explicit empty-text write per cell.
func (c *Client) ClearRows(ctx context.Context, spaceID, objectID, tableBlockID string, ids []string, indices []int) (TableInfo, []string, error) {
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, nil, err
	}
	targets, err := resolveRowTargets(info, ids, indices)
	if err != nil {
		return TableInfo{}, nil, err
	}
	// Fill first so that rows with missing cell blocks end up in a defined
	// state rather than being silently skipped.
	if err := c.fillRows(ctx, objectID, targets); err != nil {
		return TableInfo{}, nil, err
	}
	wanted := make(map[string]bool, len(targets))
	for _, id := range targets {
		wanted[id] = true
	}
	for _, row := range info.Rows {
		if !wanted[row.ID] {
			continue
		}
		for _, col := range info.Columns {
			if err := c.setBlockTextPending(ctx, objectID, row.ID+tableCellSeparator+col.ID, "", nil); err != nil {
				return TableInfo{}, nil, err
			}
		}
	}
	if err := c.FlushTextChanges(ctx, spaceID, objectID); err != nil {
		return TableInfo{}, targets, err
	}
	info, err = c.InspectTable(ctx, spaceID, objectID, info.BlockID)
	return info, targets, err
}

// SortTable sorts the table rows by the values in one column.
func (c *Client) SortTable(ctx context.Context, spaceID, objectID, tableBlockID, columnRaw string, columnIndex int, columnGiven bool, direction string) (TableInfo, error) {
	info, err := c.InspectTable(ctx, spaceID, objectID, tableBlockID)
	if err != nil {
		return TableInfo{}, err
	}
	columnID, err := info.lineID("column", "column", columnRaw, columnIndex, columnGiven)
	if err != nil {
		return TableInfo{}, err
	}
	if strings.TrimSpace(direction) == "" {
		direction = "asc"
	}
	sortType, err := lookupEnum(sortTypes, direction, "sort direction", false)
	if err != nil {
		return TableInfo{}, err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockTableSort(callCtx, &pb.RpcBlockTableSortRequest{
		ContextId: objectID, ColumnId: columnID, Type: sortType,
	})
	if err != nil {
		return TableInfo{}, fmt.Errorf("gRPC BlockTableSort failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableSortResponseError_NULL {
		return TableInfo{}, fmt.Errorf("BlockTableSort error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return c.InspectTable(ctx, spaceID, objectID, info.BlockID)
}

// tablePosition maps a caller's position onto what the table RPCs accept.
//
// The two axes disagree: row operations take Top/Bottom, column operations take
// Left/Right, and passing the wrong pair fails with "position is not supported".
// Callers may use whichever vocabulary fits their mental model — before/after,
// top/bottom or left/right — and it is translated per axis here.
func tablePosition(axis, raw string) (model.BlockPosition, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		key = "after"
	}
	var before bool
	switch key {
	case "top", "left", "before":
		before = true
	case "bottom", "right", "after":
		before = false
	default:
		return model.Block_None, fmt.Errorf("unsupported position %q for table %ss; use before or after", raw, axis)
	}
	if axis == "column" {
		if before {
			return model.Block_Left, nil
		}
		return model.Block_Right, nil
	}
	if before {
		return model.Block_Top, nil
	}
	return model.Block_Bottom, nil
}

func checkAxis(axis string) error {
	switch strings.ToLower(strings.TrimSpace(axis)) {
	case "row", "column":
		return nil
	}
	return fmt.Errorf("axis must be row or column, got %q", axis)
}

func resolveRowTargets(info TableInfo, ids []string, indices []int) ([]string, error) {
	out := make([]string, 0, len(ids)+len(indices))
	seen := make(map[string]bool)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, idx := range indices {
		id, err := info.rowID(idx)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("pass ids (row block ids) or indices (zero-based row numbers)")
	}
	return out, nil
}
