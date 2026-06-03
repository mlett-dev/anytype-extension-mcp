package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type listedEntry struct {
	Name         string `json:"name"`
	RelativePath string `json:"relative_path"`
	HostPath     string `json:"host_path"`
	ServerPath   string `json:"server_path,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	ModifiedAt   string `json:"modified_at"`
	IsDir        bool   `json:"is_dir"`
}

func (s *mcpServer) toolInfo(args map[string]any) (map[string]any, error) {
	return map[string]any{
		"input_root":         s.cfg.inRoot,
		"output_root":        s.cfg.outRoot,
		"server_input_root":  s.cfg.serverInRoot,
		"server_output_root": s.cfg.serverOutRoot,
		"api_base_url":       s.cfg.apiBaseURL,
		"api_version":        s.cfg.apiVersion,
		"api_key_set":        s.cfg.apiKey != "",
		"upload_usage": map[string]any{
			"preferred_flow": []string{
				"Call file-list-input",
				"Use returned relative_path values verbatim",
				"Pass relative_path or absolute path under input_root to file-upload or file-upload-many",
			},
			"examples": []string{
				"test-upload.txt",
				"Unterlagen/überblick.pdf",
				"/data/in/test-upload.txt",
			},
		},
		"download_usage": map[string]any{
			"examples": []string{
				"Call file-download with target_name",
				"Call file-download-many for bulk download",
				"Check output using file-list-output",
			},
		},
		"notes": []string{
			"Use relative_path values returned by file-list-input exactly as returned.",
			"Do not reconstruct filenames manually.",
			"Spaces, umlauts, and other special characters are allowed and should be preserved exactly.",
		},
	}, nil
}

func (s *mcpServer) toolListInput(args map[string]any) (map[string]any, error) {
	recursive := optionalBool(args, "recursive", false)
	includeDirs := optionalBool(args, "include_dirs", false)
	limit := optionalInt(args, "limit", 500)

	entries, err := listEntriesUnderRoot(s.cfg.inRoot, s.cfg.serverInRoot, recursive, includeDirs, limit)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"root":               s.cfg.inRoot,
		"server_root":        s.cfg.serverInRoot,
		"count":              len(entries),
		"use_relative_paths": true,
		"entries":            entries,
	}, nil
}

func (s *mcpServer) toolListOutput(args map[string]any) (map[string]any, error) {
	recursive := optionalBool(args, "recursive", false)
	includeDirs := optionalBool(args, "include_dirs", false)
	limit := optionalInt(args, "limit", 500)

	entries, err := listEntriesUnderRoot(s.cfg.outRoot, s.cfg.serverOutRoot, recursive, includeDirs, limit)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"root":        s.cfg.outRoot,
		"server_root": s.cfg.serverOutRoot,
		"count":       len(entries),
		"entries":     entries,
	}, nil
}

func (s *mcpServer) toolUpload(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	stagedPath, err := requiredRawString(args, "staged_path")
	if err != nil {
		return nil, err
	}

	resolvedPath, err := resolveUnderRoot(s.cfg.inRoot, stagedPath, false)
	if err != nil {
		suggestions := suggestSimilarInputPaths(s.cfg.inRoot, stagedPath, 8)
		if len(suggestions) > 0 {
			return nil, fmt.Errorf("invalid staged_path: %w. input_root=%q. Use the exact filename/path and do not rewrite spaces to underscores. Similar files: %s", err, s.cfg.inRoot, strings.Join(suggestions, ", "))
		}
		return nil, fmt.Errorf("invalid staged_path: %w. input_root=%q. Use the exact filename/path and do not rewrite spaces to underscores", err, s.cfg.inRoot)
	}

	typeValue := rawOptionalString(args, "type")
	if strings.TrimSpace(typeValue) == "" {
		typeValue = "file"
	}
	fileType, err := ParseFileType(strings.TrimSpace(typeValue))
	if err != nil {
		return nil, err
	}
	styleValue := rawOptionalString(args, "style")
	if strings.TrimSpace(styleValue) == "" {
		styleValue = "auto"
	}
	style, err := ParseFileStyle(strings.TrimSpace(styleValue))
	if err != nil {
		return nil, err
	}

	client, err := NewClient(context.Background(), Config{
		GRPCAddress:  s.cfg.grpcAddr,
		SessionToken: s.cfg.token,
		Timeout:      s.cfg.timeout,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	serverPath, err := mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to map staged_path for server: %w", err)
	}

	uploadResp, err := client.UploadFile(context.Background(), UploadRequest{
		SpaceID:   spaceID,
		LocalPath: serverPath,
		Type:      fileType,
		Style:     style,
	})
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"object_id":       uploadResp.ObjectID,
		"preload_file_id": uploadResp.PreloadFileID,
		"staged_path":     resolvedPath,
		"server_path":     serverPath,
	}, nil
}

func (s *mcpServer) toolDownload(args map[string]any) (map[string]any, error) {
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, err
	}
	targetName := rawOptionalString(args, "target_name")
	if targetName != "" {
		if err := validateTargetName(targetName); err != nil {
			return nil, fmt.Errorf("invalid target_name: %w", err)
		}
	}

	outRoot, err := ensureDir(s.cfg.outRoot)
	if err != nil {
		return nil, fmt.Errorf("output root not usable: %w", err)
	}

	client, err := NewClient(context.Background(), Config{
		GRPCAddress:  s.cfg.grpcAddr,
		SessionToken: s.cfg.token,
		Timeout:      s.cfg.timeout,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	serverOutRoot, err := ensureDir(s.cfg.serverOutRoot)
	if err != nil {
		return nil, fmt.Errorf("server output root not usable: %w", err)
	}

	downloadResp, err := client.DownloadFile(context.Background(), DownloadRequest{
		ObjectID: objectID,
		Path:     serverOutRoot,
	})
	if err != nil {
		return nil, err
	}

	rawLocalPath := strings.TrimSpace(downloadResp.LocalPath)
	if rawLocalPath == "" {
		return nil, fmt.Errorf("download returned empty local path")
	}

	serverLocalPath, err := normalizeUnderRoot(serverOutRoot, rawLocalPath)
	if err != nil {
		return nil, fmt.Errorf("download returned path outside server output root: %w", err)
	}

	localPath, err := mapServerPathToHostPath(serverOutRoot, outRoot, serverLocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to map downloaded path to host output root: %w", err)
	}

	if _, err := os.Stat(localPath); err != nil {
		entries, _ := os.ReadDir(outRoot)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return nil, fmt.Errorf(
			"download reported server path %q mapped to host path %q (serverOutRoot=%q, outRoot=%q, entries=%v): %w",
			rawLocalPath, localPath, serverOutRoot, outRoot, names, err,
		)
	}

	finalPath := localPath
	if targetName != "" {
		finalPath = filepath.Join(outRoot, targetName)
		if err := os.Rename(localPath, finalPath); err != nil {
			return nil, fmt.Errorf("failed to rename downloaded file: %w", err)
		}
	}

	return map[string]any{
		"object_id":   objectID,
		"local_path":  finalPath,
		"server_path": serverLocalPath,
	}, nil
}

func (s *mcpServer) toolUploadMany(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}

	rawItems, ok := args["items"]
	if !ok || rawItems == nil {
		return nil, fmt.Errorf("items is required")
	}

	itemMaps, err := asObjectSlice(rawItems)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}

	defaultType := rawOptionalString(args, "default_type")
	if strings.TrimSpace(defaultType) == "" {
		defaultType = "file"
	}
	defaultStyle := rawOptionalString(args, "default_style")
	if strings.TrimSpace(defaultStyle) == "" {
		defaultStyle = "auto"
	}
	stopOnError := optionalBool(args, "stop_on_error", false)

	client, err := NewClient(context.Background(), Config{
		GRPCAddress:  s.cfg.grpcAddr,
		SessionToken: s.cfg.token,
		Timeout:      s.cfg.timeout,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	results := make([]map[string]any, 0, len(itemMaps))
	okCount := 0
	errorCount := 0

	for i, item := range itemMaps {
		stagedPath, err := requiredRawString(item, "staged_path")
		if err != nil {
			results = append(results, map[string]any{
				"index": i,
				"error": err.Error(),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		resolvedPath, err := resolveUnderRoot(s.cfg.inRoot, stagedPath, false)
		if err != nil {
			msg := fmt.Sprintf("invalid staged_path: %v. input_root=%q", err, s.cfg.inRoot)
			if suggestions := suggestSimilarInputPaths(s.cfg.inRoot, stagedPath, 8); len(suggestions) > 0 {
				msg = fmt.Sprintf("%s. Use the exact filename/path and do not rewrite spaces to underscores. Similar files: %s", msg, strings.Join(suggestions, ", "))
			}
			results = append(results, map[string]any{
				"index":       i,
				"staged_path": stagedPath,
				"error":       msg,
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		typeValue := rawOptionalString(item, "type")
		if strings.TrimSpace(typeValue) == "" {
			typeValue = defaultType
		}
		styleValue := rawOptionalString(item, "style")
		if strings.TrimSpace(styleValue) == "" {
			styleValue = defaultStyle
		}

		fileType, err := ParseFileType(strings.TrimSpace(typeValue))
		if err != nil {
			results = append(results, map[string]any{
				"index":       i,
				"staged_path": stagedPath,
				"error":       err.Error(),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		style, err := ParseFileStyle(strings.TrimSpace(styleValue))
		if err != nil {
			results = append(results, map[string]any{
				"index":       i,
				"staged_path": stagedPath,
				"error":       err.Error(),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		serverPath, err := mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, resolvedPath)
		if err != nil {
			results = append(results, map[string]any{
				"index":       i,
				"staged_path": stagedPath,
				"error":       fmt.Sprintf("failed to map staged_path for server: %v", err),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		uploadResp, err := client.UploadFile(context.Background(), UploadRequest{
			SpaceID:   spaceID,
			LocalPath: serverPath,
			Type:      fileType,
			Style:     style,
		})
		if err != nil {
			results = append(results, map[string]any{
				"index":       i,
				"staged_path": stagedPath,
				"host_path":   resolvedPath,
				"server_path": serverPath,
				"error":       err.Error(),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		results = append(results, map[string]any{
			"index":           i,
			"staged_path":     stagedPath,
			"host_path":       resolvedPath,
			"server_path":     serverPath,
			"object_id":       uploadResp.ObjectID,
			"preload_file_id": uploadResp.PreloadFileID,
		})
		okCount++
	}

	return map[string]any{
		"space_id":    spaceID,
		"total":       len(itemMaps),
		"ok_count":    okCount,
		"error_count": errorCount,
		"results":     results,
	}, nil
}

func (s *mcpServer) toolDownloadMany(args map[string]any) (map[string]any, error) {
	rawItems, ok := args["items"]
	if !ok || rawItems == nil {
		return nil, fmt.Errorf("items is required")
	}

	itemMaps, err := asObjectSlice(rawItems)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}

	stopOnError := optionalBool(args, "stop_on_error", false)

	outRoot, err := ensureDir(s.cfg.outRoot)
	if err != nil {
		return nil, fmt.Errorf("output root not usable: %w", err)
	}

	serverOutRoot, err := ensureDir(s.cfg.serverOutRoot)
	if err != nil {
		return nil, fmt.Errorf("server output root not usable: %w", err)
	}

	client, err := NewClient(context.Background(), Config{
		GRPCAddress:  s.cfg.grpcAddr,
		SessionToken: s.cfg.token,
		Timeout:      s.cfg.timeout,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	results := make([]map[string]any, 0, len(itemMaps))
	okCount := 0
	errorCount := 0

	for i, item := range itemMaps {
		objectID, err := requiredString(item, "object_id")
		if err != nil {
			results = append(results, map[string]any{
				"index": i,
				"error": err.Error(),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		targetName := rawOptionalString(item, "target_name")
		if targetName != "" {
			if err := validateTargetName(targetName); err != nil {
				results = append(results, map[string]any{
					"index":     i,
					"object_id": objectID,
					"error":     fmt.Sprintf("invalid target_name: %v", err),
				})
				errorCount++
				if stopOnError {
					break
				}
				continue
			}
		}

		downloadResp, err := client.DownloadFile(context.Background(), DownloadRequest{
			ObjectID: objectID,
			Path:     serverOutRoot,
		})
		if err != nil {
			results = append(results, map[string]any{
				"index":     i,
				"object_id": objectID,
				"error":     err.Error(),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		rawLocalPath := strings.TrimSpace(downloadResp.LocalPath)
		if rawLocalPath == "" {
			results = append(results, map[string]any{
				"index":     i,
				"object_id": objectID,
				"error":     "download returned empty local path",
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		serverLocalPath, err := normalizeUnderRoot(serverOutRoot, rawLocalPath)
		if err != nil {
			results = append(results, map[string]any{
				"index":     i,
				"object_id": objectID,
				"error":     fmt.Sprintf("download returned path outside server output root: %v", err),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		localPath, err := mapServerPathToHostPath(serverOutRoot, outRoot, serverLocalPath)
		if err != nil {
			results = append(results, map[string]any{
				"index":     i,
				"object_id": objectID,
				"error":     fmt.Sprintf("failed to map downloaded path to host output root: %v", err),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		if _, err := os.Stat(localPath); err != nil {
			results = append(results, map[string]any{
				"index":       i,
				"object_id":   objectID,
				"server_path": serverLocalPath,
				"local_path":  localPath,
				"error":       err.Error(),
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		finalPath := localPath
		if targetName != "" {
			finalPath = filepath.Join(outRoot, targetName)
			if err := os.Rename(localPath, finalPath); err != nil {
				results = append(results, map[string]any{
					"index":     i,
					"object_id": objectID,
					"error":     fmt.Sprintf("failed to rename downloaded file: %v", err),
				})
				errorCount++
				if stopOnError {
					break
				}
				continue
			}
		}

		results = append(results, map[string]any{
			"index":       i,
			"object_id":   objectID,
			"local_path":  finalPath,
			"server_path": serverLocalPath,
		})
		okCount++
	}

	return map[string]any{
		"total":       len(itemMaps),
		"ok_count":    okCount,
		"error_count": errorCount,
		"results":     results,
	}, nil
}

func normalizeUnderRoot(root string, value string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(value) == "" {
		return "", errors.New("empty path")
	}

	candidate := value
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}

	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside root %q", candidateAbs, rootAbs)
	}

	return candidateAbs, nil
}

func mapHostPathToServerPath(hostRoot string, serverRoot string, hostPath string) (string, error) {
	hostRootAbs, err := filepath.Abs(hostRoot)
	if err != nil {
		return "", err
	}

	hostPathAbs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(hostRootAbs, hostPathAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside host root %q", hostPathAbs, hostRootAbs)
	}

	return filepath.Join(serverRoot, rel), nil
}

func mapServerPathToHostPath(serverRoot string, hostRoot string, serverPath string) (string, error) {
	serverRootAbs, err := filepath.Abs(serverRoot)
	if err != nil {
		return "", err
	}

	serverPathAbs, err := filepath.Abs(serverPath)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(serverRootAbs, serverPathAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside server root %q", serverPathAbs, serverRootAbs)
	}

	return filepath.Join(hostRoot, rel), nil
}

func listEntriesUnderRoot(hostRoot string, serverRoot string, recursive bool, includeDirs bool, limit int) ([]map[string]any, error) {
	hostRootAbs, err := ensureDir(hostRoot)
	if err != nil {
		return nil, err
	}

	type row struct {
		entry listedEntry
	}
	rows := make([]row, 0)

	addEntry := func(path string, info os.FileInfo) error {
		rel, err := filepath.Rel(hostRootAbs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		serverPath := ""
		if serverRoot != "" {
			serverPath = filepath.ToSlash(filepath.Join(serverRoot, rel))
		}

		rows = append(rows, row{
			entry: listedEntry{
				Name:         info.Name(),
				RelativePath: rel,
				HostPath:     path,
				ServerPath:   serverPath,
				SizeBytes:    info.Size(),
				ModifiedAt:   info.ModTime().UTC().Format(time.RFC3339),
				IsDir:        info.IsDir(),
			},
		})
		return nil
	}

	if recursive {
		err = filepath.Walk(hostRootAbs, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == hostRootAbs {
				return nil
			}
			if info.IsDir() && !includeDirs {
				return nil
			}
			return addEntry(path, info)
		})
		if err != nil {
			return nil, err
		}
	} else {
		entries, err := os.ReadDir(hostRootAbs)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			if info.IsDir() && !includeDirs {
				continue
			}
			if err := addEntry(filepath.Join(hostRootAbs, e.Name()), info); err != nil {
				return nil, err
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].entry.RelativePath < rows[j].entry.RelativePath
	})

	if limit <= 0 {
		limit = 500
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"name":          r.entry.Name,
			"relative_path": r.entry.RelativePath,
			"host_path":     r.entry.HostPath,
			"server_path":   r.entry.ServerPath,
			"size_bytes":    r.entry.SizeBytes,
			"modified_at":   r.entry.ModifiedAt,
			"is_dir":        r.entry.IsDir,
		})
	}
	return out, nil
}

func suggestSimilarInputPaths(root string, wanted string, limit int) []string {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}

	if limit <= 0 {
		limit = 8
	}

	wanted = filepath.ToSlash(strings.TrimSpace(wanted))
	wantedBase := filepath.Base(wanted)
	wantedExt := strings.ToLower(filepath.Ext(wantedBase))
	wantedKey := fuzzyPathKey(wanted)
	wantedBaseKey := fuzzyPathKey(wantedBase)

	type candidate struct {
		rel   string
		score int
	}

	var candidates []candidate

	_ = filepath.Walk(rootAbs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		base := filepath.Base(rel)
		baseExt := strings.ToLower(filepath.Ext(base))
		relKey := fuzzyPathKey(rel)
		baseKey := fuzzyPathKey(base)

		score := 0

		switch {
		case rel == wanted:
			score = 1000
		case base == wantedBase:
			score = 950
		case relKey == wantedKey:
			score = 900
		case baseKey == wantedBaseKey:
			score = 850
		case strings.Contains(relKey, wantedKey) || strings.Contains(wantedKey, relKey):
			score = 700
		case strings.Contains(baseKey, wantedBaseKey) || strings.Contains(wantedBaseKey, baseKey):
			score = 650
		}

		if score > 0 {
			if wantedExt != "" && baseExt == wantedExt {
				score += 25
			}
			candidates = append(candidates, candidate{rel: rel, score: score})
		}
		return nil
	})

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].rel < candidates[j].rel
		}
		return candidates[i].score > candidates[j].score
	})

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, limit)
	for _, c := range candidates {
		if _, ok := seen[c.rel]; ok {
			continue
		}
		seen[c.rel] = struct{}{}
		out = append(out, c.rel)
		if len(out) >= limit {
			break
		}
	}

	return out
}

func fuzzyPathKey(s string) string {
	s = strings.ToLower(filepath.ToSlash(s))
	replacer := strings.NewReplacer(
		" ", "",
		"_", "",
		"-", "",
		".", "",
		"(", "",
		")", "",
		"[", "",
		"]", "",
		",", "",
		";", "",
		"+", "",
	)
	return replacer.Replace(s)
}
