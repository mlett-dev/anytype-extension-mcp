package main

import (
	"context"
	"fmt"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// Table editing tools.
//
// block-create with kind=table only produces an empty grid; everything the GUI
// can do to a table afterwards lives here. Insert, delete, duplicate and move
// take an axis parameter instead of existing twice, once per axis.
//
// Rows and columns can be addressed either by block id or by zero-based index.
// Indices are what a model can actually produce from reading table-inspect
// output, so they are accepted everywhere an id is.

const tableAxisDescription = "Whether the operation applies to rows or to columns."

func tableIndexProp(what string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": "Zero-based " + what + " index from table-inspect. Alternative to the id form.",
	}
}

func tableIndexListProp(what string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Zero-based " + what + " indices from table-inspect. Resolved against the table as it looks now, so several may be passed at once.",
		"items":       map[string]any{"type": "integer"},
	}
}

func tableIDListProp(what string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": what + " block ids from table-inspect.",
		"items":       map[string]any{"type": "string"},
	}
}

func (s *mcpServer) tableToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "table-inspect",
			"description": "Read a table's structure: its rows and columns with ids and indices, which row is a header, and the text of every cell. Call this before any other table tool — they all address rows and columns by the ids or indices reported here. The full row x column grid is always returned; cells carrying \"exists\": false have no block yet, which is normal for never-filled or emptied cells and is fixed by writing to them. table_block_id may be omitted when the object holds exactly one table.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id from block-list. Omit if the object has exactly one table."),
			}),
		},
		map[string]any{
			"name":        "table-set-cells",
			"description": "Write text into table cells. This is how a table gets its content: block-create with kind=table only lays out an empty grid, and the cell blocks themselves are created here on first write. Pass many cells in one call. Each cell is addressed by row and column, either as zero-based indices or as block ids.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "cells"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"cells": map[string]any{
					"type":        "array",
					"description": "Cells to write.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"row":       tableIndexProp("row"),
							"column":    tableIndexProp("column"),
							"row_id":    strProp("Row block id, instead of row."),
							"column_id": strProp("Column block id, instead of column."),
							"text":      strProp("Cell text. Pass an empty string to clear the cell."),
						},
						"required": []any{"text"},
					},
				},
			}),
		},
		map[string]any{
			"name":        "table-insert",
			"description": "Insert rows or columns into a table. Without a target they are appended at the end, which is the common case; with a target they go next to that row or column. New rows and columns start empty — fill them with table-set-cells.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "axis"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"axis":           enumProp(tableAxisDescription, []string{"row", "column"}),
				"count":          map[string]any{"type": "integer", "description": "How many to insert. Defaults to 1.", "default": 1},
				"target_id":      strProp("Row or column block id to insert next to. Omit to append at the end."),
				"target":         tableIndexProp("row or column"),
				"position":       enumProp("Where to insert relative to the target: before means above a row / left of a column, after means below a row / right of a column. Defaults to after.", []string{"before", "after"}),
			}),
		},
		map[string]any{
			"name":        "table-delete",
			"description": "Delete rows or columns from a table, including their cells. Pass ids or indices; indices are resolved against the table as it looks before the first deletion, so a batch behaves as intended.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "axis"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"axis":           enumProp(tableAxisDescription, []string{"row", "column"}),
				"ids":            tableIDListProp("Row or column"),
				"indices":        tableIndexListProp("row or column"),
			}),
		},
		map[string]any{
			"name":        "table-duplicate",
			"description": "Duplicate one row or column together with its cell contents.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "axis"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"axis":           enumProp(tableAxisDescription, []string{"row", "column"}),
				"target_id":      strProp("Row or column block id to duplicate."),
				"target":         tableIndexProp("row or column"),
				"position":       enumProp("Where to place the copy relative to the original. Defaults to after.", []string{"before", "after"}),
			}),
		},
		map[string]any{
			"name":        "table-move",
			"description": "Reorder a row or a column by moving it next to another one.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "axis"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"axis":           enumProp(tableAxisDescription, []string{"row", "column"}),
				"target_id":      strProp("Row or column block id to move."),
				"target":         tableIndexProp("row or column to move"),
				"drop_target_id": strProp("Row or column block id to move it next to."),
				"drop_target":    tableIndexProp("row or column to move next to"),
				"position":       enumProp("Place it before or after the drop target. Defaults to after.", []string{"before", "after"}),
			}),
		},
		map[string]any{
			"name":        "table-row-header",
			"description": "Mark rows as header rows, or turn that off again. Header rows are styled differently and stay in place when the table is sorted.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "is_header"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"ids":            tableIDListProp("Row"),
				"indices":        tableIndexListProp("row"),
				"is_header":      map[string]any{"type": "boolean", "description": "true makes them header rows, false makes them normal rows."},
			}),
		},
		map[string]any{
			"name":        "table-row-clear",
			"description": "Empty the cells of rows while keeping the rows themselves. Use table-delete to remove the rows instead. Anytype discards emptied cell blocks afterwards, so the cleared cells come back from table-inspect with \"exists\": false until something is written to them again.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"ids":            tableIDListProp("Row"),
				"indices":        tableIndexListProp("row"),
			}),
		},
		map[string]any{
			"name":        "table-sort",
			"description": "Sort the table's rows by the text in one column. Header rows are not sorted along. This reorders the stored rows; it is not a live view sort like query-sort-add.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":       spaceIDProp(),
				"object_id":      strProp("Object containing the table."),
				"table_block_id": strProp("Table block id. Omit if the object has exactly one table."),
				"column_id":      strProp("Column block id to sort by."),
				"column":         tableIndexProp("column to sort by"),
				"direction":      enumProp("Sort direction. Defaults to asc.", []string{"asc", "desc"}),
			}),
		},
	}
}

func (s *mcpServer) dispatchTableTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "table-inspect":
		res, err := s.toolTableInspect(args)
		return res, err, true
	case "table-set-cells":
		res, err := s.toolTableSetCells(args)
		return res, err, true
	case "table-insert":
		res, err := s.toolTableInsert(args)
		return res, err, true
	case "table-delete":
		res, err := s.toolTableDelete(args)
		return res, err, true
	case "table-duplicate":
		res, err := s.toolTableDuplicate(args)
		return res, err, true
	case "table-move":
		res, err := s.toolTableMove(args)
		return res, err, true
	case "table-row-header":
		res, err := s.toolTableRowHeader(args)
		return res, err, true
	case "table-row-clear":
		res, err := s.toolTableRowClear(args)
		return res, err, true
	case "table-sort":
		res, err := s.toolTableSort(args)
		return res, err, true
	}
	return nil, nil, false
}

// hasArg reports whether the caller supplied a key at all. Index arguments need
// this: 0 is a valid row, so a plain zero default cannot mean "not given".
func hasArg(args map[string]any, key string) bool {
	raw, ok := args[key]
	return ok && raw != nil
}

func optionalIntSlice(args map[string]any, key string) ([]int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of integers", key)
	}
	out := make([]int, 0, len(items))
	for i, item := range items {
		switch v := item.(type) {
		case float64:
			out = append(out, int(v))
		case int:
			out = append(out, v)
		default:
			return nil, fmt.Errorf("%s[%d] must be an integer", key, i)
		}
	}
	return out, nil
}

// tableTarget resolves the common arguments of every table tool.
func (s *mcpServer) tableTarget(args map[string]any) (*anytypefiles.Client, string, string, string, error) {
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, "", "", "", err
	}
	return client, spaceID, objectID, optionalString(args, "table_block_id"), nil
}

// tableResult renders a table for the model, cells included.
func tableResult(spaceID, objectID string, info anytypefiles.TableInfo, extra map[string]any) map[string]any {
	rows := make([]map[string]any, 0, len(info.Rows))
	for _, r := range info.Rows {
		entry := map[string]any{"id": r.ID, "index": r.Index}
		if r.IsHeader {
			entry["is_header"] = true
		}
		rows = append(rows, entry)
	}
	columns := make([]map[string]any, 0, len(info.Columns))
	for _, c := range info.Columns {
		columns = append(columns, map[string]any{"id": c.ID, "index": c.Index})
	}
	cells := make([]map[string]any, 0, len(info.Cells))
	for _, c := range info.Cells {
		entry := map[string]any{
			"id": c.ID, "row": c.Row, "column": c.Column, "text": c.Text,
		}
		// Only the exceptions are called out, so the common case stays compact.
		if !c.Exists {
			entry["exists"] = false
		}
		cells = append(cells, entry)
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"table_block_id": info.BlockID,
		"row_count":      info.RowCount, "column_count": info.ColCount,
		"rows": rows, "columns": columns, "cells": cells,
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (s *mcpServer) toolTableInspect(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, err := client.InspectTable(context.Background(), spaceID, objectID, tableID)
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, nil), nil
}

func (s *mcpServer) toolTableSetCells(args map[string]any) (map[string]any, error) {
	raw, ok := args["cells"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("cells is required")
	}
	items, err := asObjectSlice(raw)
	if err != nil {
		return nil, fmt.Errorf("cells: %w", err)
	}
	writes := make([]anytypefiles.CellWrite, 0, len(items))
	for i, item := range items {
		text, err := requiredString(item, "text")
		if err != nil {
			// An empty string is a legitimate value here: it clears the cell.
			if _, present := item["text"]; !present {
				return nil, fmt.Errorf("cells[%d]: %w", i, err)
			}
			text = ""
		}
		writes = append(writes, anytypefiles.CellWrite{
			Row:         optionalInt(item, "row", 0),
			Column:      optionalInt(item, "column", 0),
			RowID:       optionalString(item, "row_id"),
			ColumnID:    optionalString(item, "column_id"),
			Text:        text,
			RowGiven:    hasArg(item, "row"),
			ColumnGiven: hasArg(item, "column"),
		})
	}

	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, written, err := client.SetCells(context.Background(), spaceID, objectID, tableID, writes)
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"written_cell_ids": written, "written_count": len(written),
	}), nil
}

func (s *mcpServer) toolTableInsert(args map[string]any) (map[string]any, error) {
	axis, err := requiredString(args, "axis")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, err := client.InsertLines(context.Background(), spaceID, objectID, tableID, axis,
		optionalString(args, "target_id"), optionalInt(args, "target", 0), hasArg(args, "target"),
		optionalString(args, "position"), optionalInt(args, "count", 1))
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"axis": axis, "inserted": true,
	}), nil
}

func (s *mcpServer) toolTableDelete(args map[string]any) (map[string]any, error) {
	axis, err := requiredString(args, "axis")
	if err != nil {
		return nil, err
	}
	indices, err := optionalIntSlice(args, "indices")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, deleted, err := client.DeleteLines(context.Background(), spaceID, objectID, tableID, axis,
		optionalStringSlice(args, "ids"), indices)
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"axis": axis, "deleted_ids": deleted, "deleted": true,
	}), nil
}

func (s *mcpServer) toolTableDuplicate(args map[string]any) (map[string]any, error) {
	axis, err := requiredString(args, "axis")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, err := client.DuplicateLine(context.Background(), spaceID, objectID, tableID, axis,
		optionalString(args, "target_id"), optionalInt(args, "target", 0), hasArg(args, "target"),
		optionalString(args, "position"))
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"axis": axis, "duplicated": true,
	}), nil
}

func (s *mcpServer) toolTableMove(args map[string]any) (map[string]any, error) {
	axis, err := requiredString(args, "axis")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, err := client.MoveLine(context.Background(), spaceID, objectID, tableID, axis,
		optionalString(args, "target_id"), optionalInt(args, "target", 0), hasArg(args, "target"),
		optionalString(args, "drop_target_id"), optionalInt(args, "drop_target", 0), hasArg(args, "drop_target"),
		optionalString(args, "position"))
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"axis": axis, "moved": true,
	}), nil
}

func (s *mcpServer) toolTableRowHeader(args map[string]any) (map[string]any, error) {
	raw, ok := args["is_header"]
	if !ok {
		return nil, fmt.Errorf("is_header is required")
	}
	isHeader, ok := raw.(bool)
	if !ok {
		return nil, fmt.Errorf("is_header must be a boolean")
	}
	indices, err := optionalIntSlice(args, "indices")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, targets, err := client.SetRowHeader(context.Background(), spaceID, objectID, tableID,
		optionalStringSlice(args, "ids"), indices, isHeader)
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"row_ids": targets, "is_header": isHeader, "updated": true,
	}), nil
}

func (s *mcpServer) toolTableRowClear(args map[string]any) (map[string]any, error) {
	indices, err := optionalIntSlice(args, "indices")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	info, targets, err := client.ClearRows(context.Background(), spaceID, objectID, tableID,
		optionalStringSlice(args, "ids"), indices)
	if err != nil {
		return nil, err
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"row_ids": targets, "cleared": true,
	}), nil
}

func (s *mcpServer) toolTableSort(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, tableID, err := s.tableTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	direction := optionalString(args, "direction")
	info, err := client.SortTable(context.Background(), spaceID, objectID, tableID,
		optionalString(args, "column_id"), optionalInt(args, "column", 0), hasArg(args, "column"),
		direction)
	if err != nil {
		return nil, err
	}
	if direction == "" {
		direction = "asc"
	}
	return tableResult(spaceID, objectID, info, map[string]any{
		"direction": direction, "sorted": true,
	}), nil
}
