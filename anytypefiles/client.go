package anytypefiles

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pb/service"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

const (
	DefaultGRPCAddress = "dns:///127.0.0.1:31010"
	DefaultTimeout     = 30 * time.Second

	// keyringService and keyringTokenUser mirror the storage used by
	// anytype-cli so a token saved via "anytype-cli login" is picked up
	// automatically. ANYTYPE_SESSION_TOKEN always takes precedence.
	keyringService   = "anytype-cli"
	keyringTokenUser = "session-token"
)

type Config struct {
	GRPCAddress  string
	SessionToken string
	Timeout      time.Duration
}

type Client struct {
	conn    *grpc.ClientConn
	rpc     service.ClientCommandsClient
	token   string
	timeout time.Duration
}

type UploadRequest struct {
	SpaceID   string
	LocalPath string
	URL       string
	Type      model.BlockContentFileType
	Style     model.BlockContentFileStyle
}

type UploadResponse struct {
	ObjectID      string
	PreloadFileID string
}

type DownloadRequest struct {
	ObjectID string
	Path     string
}

type DownloadResponse struct {
	LocalPath string
}

func NewClient(_ context.Context, cfg Config) (*Client, error) {
	grpcAddress := cfg.GRPCAddress
	if strings.TrimSpace(grpcAddress) == "" {
		grpcAddress = DefaultGRPCAddress
	}

	token := strings.TrimSpace(cfg.SessionToken)
	if token == "" {
		storedToken, err := keyring.Get(keyringService, keyringTokenUser)
		if err != nil {
			if errors.Is(err, keyring.ErrNotFound) {
				return nil, errors.New("no session token found: set ANYTYPE_SESSION_TOKEN or log in via anytype-cli")
			}
			return nil, fmt.Errorf("could not read session token from keyring: %w", err)
		}
		token = strings.TrimSpace(storedToken)
	}
	if token == "" {
		return nil, errors.New("empty session token: set ANYTYPE_SESSION_TOKEN")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	conn, err := grpc.NewClient(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to gRPC at %s: %w", grpcAddress, err)
	}

	return &Client{
		conn:    conn,
		rpc:     service.NewClientCommandsClient(conn),
		token:   token,
		timeout: timeout,
	}, nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) UploadFile(ctx context.Context, req UploadRequest) (UploadResponse, error) {
	if strings.TrimSpace(req.SpaceID) == "" {
		return UploadResponse{}, errors.New("space_id is required")
	}

	localPath := strings.TrimSpace(req.LocalPath)
	url := strings.TrimSpace(req.URL)
	if (localPath == "" && url == "") || (localPath != "" && url != "") {
		return UploadResponse{}, errors.New("exactly one of local_path or url is required")
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.FileUpload(callCtx, &pb.RpcFileUploadRequest{
		SpaceId:   req.SpaceID,
		LocalPath: localPath,
		Url:       url,
		Type:      req.Type,
		Style:     req.Style,
	})
	if err != nil {
		return UploadResponse{}, fmt.Errorf("gRPC FileUpload failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcFileUploadResponseError_NULL {
		return UploadResponse{}, fmt.Errorf("FileUpload error (%s): %s", resp.Error.Code.String(), resp.Error.Description)
	}

	return UploadResponse{
		ObjectID:      resp.ObjectId,
		PreloadFileID: resp.PreloadFileId,
	}, nil
}

func (c *Client) DownloadFile(ctx context.Context, req DownloadRequest) (DownloadResponse, error) {
	if strings.TrimSpace(req.ObjectID) == "" {
		return DownloadResponse{}, errors.New("object_id is required")
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.FileDownload(callCtx, &pb.RpcFileDownloadRequest{
		ObjectId: req.ObjectID,
		Path:     strings.TrimSpace(req.Path),
	})
	if err != nil {
		return DownloadResponse{}, fmt.Errorf("gRPC FileDownload failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcFileDownloadResponseError_NULL {
		return DownloadResponse{}, fmt.Errorf("FileDownload error (%s): %s", resp.Error.Code.String(), resp.Error.Description)
	}

	return DownloadResponse{
		LocalPath: resp.LocalPath,
	}, nil
}

func (c *Client) contextWithAuth(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("token", c.token))
	if _, hasDeadline := ctx.Deadline(); hasDeadline || c.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

func ParseFileType(value string) (model.BlockContentFileType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "file":
		return model.BlockContentFile_File, nil
	case "none":
		return model.BlockContentFile_None, nil
	case "image":
		return model.BlockContentFile_Image, nil
	case "video":
		return model.BlockContentFile_Video, nil
	case "audio":
		return model.BlockContentFile_Audio, nil
	default:
		return 0, fmt.Errorf("invalid type %q (expected: file|image|video|audio|none)", value)
	}
}

func ParseFileStyle(value string) (model.BlockContentFileStyle, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return model.BlockContentFile_Auto, nil
	case "link":
		return model.BlockContentFile_Link, nil
	case "embed":
		return model.BlockContentFile_Embed, nil
	default:
		return 0, fmt.Errorf("invalid style %q (expected: auto|link|embed)", value)
	}
}

// AppVersion reports the version of the anytype-heart server this client is
// talking to, which is what decides which quirks apply.
func (c *Client) AppVersion(ctx context.Context) (string, string, error) {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.AppGetVersion(callCtx, &pb.RpcAppGetVersionRequest{})
	if err != nil {
		return "", "", fmt.Errorf("gRPC AppGetVersion failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcAppGetVersionResponseError_NULL {
		return "", "", fmt.Errorf("AppGetVersion error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.Version, resp.Details, nil
}
