package anytypefiles

// Page columns.
//
// Anytype has no column block a caller creates directly. Columns appear when a
// block is inserted or moved with position left/right: heart wraps the target
// and the newcomer into a layout row whose children are layout columns
// (InsertTo -> moveFromSide -> wrapToRow). What is left over afterwards is how
// wide each column should be, and that is a plain field on the column block —
// "width", a fraction of the row.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/gogo/protobuf/types"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// ColumnWidth is one column of a row and the share of the row it takes.
type ColumnWidth struct {
	BlockID  string  `json:"block_id"`
	Width    float64 `json:"width"`
	Previous float64 `json:"previous_width"`
}

// SetColumnWidths distributes the width of one row across its columns.
//
// blockID may be the row, one of its columns, or a block sitting in a column —
// callers know the block they put somewhere, not the layout scaffolding heart
// built around it, so the row is found by walking up from whatever was passed.
//
// The widths are shares and are normalised to sum to 1: [2, 1] and
// [0.667, 0.333] mean the same thing. All zeroes is the way back to equal
// columns, which is also what heart itself writes whenever the number of
// columns in a row changes (normalizeLayoutRow).
func (c *Client) SetColumnWidths(ctx context.Context, spaceID, objectID, blockID string, widths []float64) (string, []ColumnWidth, error) {
	if strings.TrimSpace(blockID) == "" {
		return "", nil, errors.New("block_id is required")
	}
	if len(widths) == 0 {
		return "", nil, errors.New("widths is required: pass one share per column, or zeroes to make them equal")
	}

	blocks, err := c.showBlocks(ctx, spaceID, objectID)
	if err != nil {
		return "", nil, err
	}
	rowID, err := findLayoutRow(blocks, blockID)
	if err != nil {
		return "", nil, err
	}
	columns := blocks[rowID].ChildrenIds
	if len(widths) != len(columns) {
		return "", nil, fmt.Errorf("row %s has %d columns but %d widths were given; pass exactly one share per column",
			rowID, len(columns), len(widths))
	}

	normalised, err := normaliseWidths(widths)
	if err != nil {
		return "", nil, err
	}

	fields := make([]*pb.RpcBlockListSetFieldsRequestBlockField, 0, len(columns))
	out := make([]ColumnWidth, 0, len(columns))
	for i, columnID := range columns {
		// SetFields assigns the whole struct (b.Model().Fields = fr.Fields), so
		// the existing fields have to be carried over rather than replaced.
		merged := &types.Struct{Fields: map[string]*types.Value{}}
		if existing := blocks[columnID].GetFields(); existing != nil {
			for key, value := range existing.GetFields() {
				merged.Fields[key] = value
			}
		}
		previous := merged.Fields["width"].GetNumberValue()
		merged.Fields["width"] = &types.Value{Kind: &types.Value_NumberValue{NumberValue: normalised[i]}}
		fields = append(fields, &pb.RpcBlockListSetFieldsRequestBlockField{
			BlockId: columnID, Fields: merged,
		})
		out = append(out, ColumnWidth{BlockID: columnID, Width: normalised[i], Previous: previous})
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockListSetFields(callCtx, &pb.RpcBlockListSetFieldsRequest{
		ContextId: objectID, BlockFields: fields,
	})
	if err != nil {
		return "", nil, fmt.Errorf("gRPC BlockListSetFields failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockListSetFieldsResponseError_NULL {
		return "", nil, fmt.Errorf("BlockListSetFields error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return rowID, out, nil
}

// normaliseWidths turns shares of any scale into fractions of one row.
func normaliseWidths(widths []float64) ([]float64, error) {
	sum := 0.0
	for i, w := range widths {
		if w < 0 || math.IsNaN(w) || math.IsInf(w, 0) {
			return nil, fmt.Errorf("widths[%d] is %v; a column width is a share of the row and cannot be negative", i, w)
		}
		sum += w
	}
	// All zeroes is not an error but the reset: heart reads a zero width as
	// "no preference" and lays the columns out evenly.
	if sum == 0 {
		return widths, nil
	}
	out := make([]float64, len(widths))
	for i, w := range widths {
		out[i] = math.Round(w/sum*10000) / 10000
	}
	return out, nil
}

// findLayoutRow walks up from a block to the row its column belongs to.
func findLayoutRow(blocks map[string]*model.Block, blockID string) (string, error) {
	block, ok := blocks[blockID]
	if !ok {
		return "", fmt.Errorf("block %s not found in this object", blockID)
	}
	if isLayout(block, model.BlockContentLayout_Row) {
		return blockID, nil
	}

	parent := make(map[string]string, len(blocks))
	for id, b := range blocks {
		for _, child := range b.ChildrenIds {
			parent[child] = id
		}
	}
	for at, hops := blockID, 0; hops < 8; hops++ {
		up, ok := parent[at]
		if !ok {
			break
		}
		if isLayout(blocks[up], model.BlockContentLayout_Row) {
			return up, nil
		}
		at = up
	}
	return "", fmt.Errorf(
		"block %s is not inside a row of columns. Columns come into being by moving a block next to another one: "+
			"block-move with position=left or right and drop_target_id set to the block it should sit beside", blockID)
}

func isLayout(block *model.Block, style model.BlockContentLayoutStyle) bool {
	if block == nil {
		return false
	}
	layout := block.GetLayout()
	return layout != nil && layout.Style == style
}

// showBlocks reads an object's blocks as heart stores them, fields included.
func (c *Client) showBlocks(ctx context.Context, spaceID, objectID string) (map[string]*model.Block, error) {
	if strings.TrimSpace(objectID) == "" {
		return nil, errors.New("object_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC ObjectShow failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return nil, fmt.Errorf("ObjectShow error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	out := make(map[string]*model.Block, len(resp.GetObjectView().GetBlocks()))
	for _, b := range resp.GetObjectView().GetBlocks() {
		out[b.Id] = b
	}
	return out, nil
}
