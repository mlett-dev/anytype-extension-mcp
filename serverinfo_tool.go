package main

import (
	"context"
	"fmt"
)

// What this server is and what it is connected to.
//
// The MCP handshake carries a version in serverInfo, but the connector keeps
// that to itself — a model on the other end has no way to ask. Since the
// answer decides which behaviours to expect, it needs to be a tool.

func (s *mcpServer) serverInfoToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "server-info",
			"description": "Report which version of this Anytype MCP server is running, how many tools it offers, and which Anytype server it is connected to. Ask for this when a tool behaves unexpectedly or when instructions mention a capability that may not exist here yet — the version tells you whether it should.",
			"inputSchema": restSchema(nil, map[string]any{}),
		},
	}
}

func (s *mcpServer) dispatchServerInfoTool(name string, args map[string]any) (map[string]any, error, bool) {
	if name != "server-info" {
		return nil, nil, false
	}
	res, err := s.toolServerInfo()
	return res, err, true
}

func (s *mcpServer) toolServerInfo() (map[string]any, error) {
	out := map[string]any{
		"server":              serverName,
		"version":             serverVersion,
		"tool_count":          len(s.allToolDefs()),
		"protocol":            protoVersion,
		"toolset":             "full",
		"api_version":         s.cfg.apiVersion,
		"input_root":          s.cfg.inRoot,
		"output_root":         s.cfg.outRoot,
		"unsplash_configured": s.cfg.unsplashKey != "",
	}
	if s.cfg.leanToolset {
		out["toolset"] = "lean"
	}

	// The Anytype version matters as much as this server's: nearly every quirk
	// documented in the tool descriptions was established against a particular
	// build of anytype-heart.
	client, err := s.grpcClient()
	if err != nil {
		out["anytype"] = fmt.Sprintf("not reachable: %v", err)
		return out, nil
	}
	defer client.Close()
	version, details, err := client.AppVersion(context.Background())
	if err != nil {
		out["anytype"] = fmt.Sprintf("not reachable: %v", err)
		return out, nil
	}
	out["anytype_version"] = version
	if details != "" {
		out["anytype_build"] = details
	}
	return out, nil
}
