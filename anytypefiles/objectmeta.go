package anytypefiles

// Object-level actions the REST API does not expose: favourites, the archive
// (including the way back out of it) and the per-object undo stack.
//
// The REST delete-object endpoint archives an object but offers no way to
// restore it. ObjectListSetIsArchived does both directions, which is why
// unarchiving is only reachable over gRPC.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

var fileBlockTypes = map[string]model.BlockContentFileType{
	"file":  model.BlockContentFile_File,
	"image": model.BlockContentFile_Image,
	"video": model.BlockContentFile_Video,
	"audio": model.BlockContentFile_Audio,
	"pdf":   model.BlockContentFile_PDF,
	"none":  model.BlockContentFile_None,
}

// FileBlockTypeNames lists the accepted file block types, for tool schemas.
func FileBlockTypeNames() []string {
	return []string{"file", "image", "video", "audio", "pdf"}
}

// UndoResult reports what an undo or redo step did and how much is left.
type UndoResult struct {
	Undo int32 `json:"undo_steps_left"`
	Redo int32 `json:"redo_steps_left"`
}

// CreateFileBlock inserts a file, image, video, audio or PDF block and uploads
// its content.
//
// Exactly one of localPath and url is used. localPath must be a path in the
// anytype-heart server's own filesystem, not in this process's — the caller is
// responsible for mapping it (see mapHostPathToServerPath in the tool layer).
func (c *Client) CreateFileBlock(ctx context.Context, objectID, targetID, position, fileType, localPath, url string) (string, error) {
	localPath = strings.TrimSpace(localPath)
	url = strings.TrimSpace(url)
	if localPath == "" && url == "" {
		return "", errors.New("either staged_path or url is required")
	}
	if localPath != "" && url != "" {
		return "", errors.New("pass either staged_path or url, not both")
	}
	blockType, err := lookupEnum(fileBlockTypes, fileType, "file type", true)
	if err != nil {
		return "", err
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return "", err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockFileCreateAndUpload(callCtx, &pb.RpcBlockFileCreateAndUploadRequest{
		ContextId: objectID, TargetId: targetID, Position: pos,
		LocalPath: localPath, Url: url, FileType: blockType,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockFileCreateAndUpload failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockFileCreateAndUploadResponseError_NULL {
		return "", fmt.Errorf("BlockFileCreateAndUpload error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

// There is no wrapper for BlockFileSetName on purpose: in anytype-heart
// v0.50.8 that RPC is an unimplemented stub which returns success and changes
// nothing (verified live — the block kept its old name). Renaming goes through
// the file object the block points at instead.

// SetFavorite adds objects to the Favorites section of the sidebar, or removes
// them from it.
func (c *Client) SetFavorite(ctx context.Context, objectIDs []string, favorite bool) error {
	if len(objectIDs) == 0 {
		return errors.New("object_ids is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectListSetIsFavorite(callCtx, &pb.RpcObjectListSetIsFavoriteRequest{
		ObjectIds: objectIDs, IsFavorite: favorite,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectListSetIsFavorite failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectListSetIsFavoriteResponseError_NULL {
		return fmt.Errorf("ObjectListSetIsFavorite error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// SetArchived moves objects to the bin or restores them from it.
//
// archived=false is the restore path, and the only one available at all: the
// REST delete-object endpoint archives but never unarchives.
func (c *Client) SetArchived(ctx context.Context, objectIDs []string, archived bool) error {
	if len(objectIDs) == 0 {
		return errors.New("object_ids is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectListSetIsArchived(callCtx, &pb.RpcObjectListSetIsArchivedRequest{
		ObjectIds: objectIDs, IsArchived: archived,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectListSetIsArchived failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectListSetIsArchivedResponseError_NULL {
		return fmt.Errorf("ObjectListSetIsArchived error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// UndoRedo steps one object's edit history backwards or forwards.
//
// The undo stack is per object and lives in the opened object's state, so the
// object is shown first — a bare Undo on an object heart has not loaded finds
// an empty stack. Running out of steps is reported as a plain message rather
// than a raw gRPC error code, because it is an expected outcome, not a fault.
func (c *Client) UndoRedo(ctx context.Context, spaceID, objectID string, redo bool) (UndoResult, bool, error) {
	if strings.TrimSpace(objectID) == "" {
		return UndoResult{}, false, errors.New("object_id is required")
	}
	if _, _, err := c.ReadBlocks(ctx, spaceID, objectID); err != nil {
		return UndoResult{}, false, err
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	if redo {
		resp, err := c.rpc.ObjectRedo(callCtx, &pb.RpcObjectRedoRequest{ContextId: objectID})
		if err != nil {
			return UndoResult{}, false, fmt.Errorf("gRPC ObjectRedo failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcObjectRedoResponseError_NULL {
			if resp.Error.Code == pb.RpcObjectRedoResponseError_CAN_NOT_MOVE {
				return UndoResult{}, false, nil
			}
			return UndoResult{}, false, fmt.Errorf("ObjectRedo error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		return UndoResult{
			Undo: resp.GetCounters().GetUndo(), Redo: resp.GetCounters().GetRedo(),
		}, true, nil
	}

	resp, err := c.rpc.ObjectUndo(callCtx, &pb.RpcObjectUndoRequest{ContextId: objectID})
	if err != nil {
		return UndoResult{}, false, fmt.Errorf("gRPC ObjectUndo failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectUndoResponseError_NULL {
		if resp.Error.Code == pb.RpcObjectUndoResponseError_CAN_NOT_MOVE {
			return UndoResult{}, false, nil
		}
		return UndoResult{}, false, fmt.Errorf("ObjectUndo error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return UndoResult{
		Undo: resp.GetCounters().GetUndo(), Redo: resp.GetCounters().GetRedo(),
	}, true, nil
}
