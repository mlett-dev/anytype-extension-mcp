package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func (s *mcpServer) toolListObjectsCompact(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	listID, err := requiredString(args, "list_id")
	if err != nil {
		return nil, err
	}
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}

	offset := optionalInt(args, "offset", 0)
	if offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0")
	}
	limit := optionalInt(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}

	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))
	if err := addFiltersQuery(query, args); err != nil {
		return nil, err
	}

	payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID)+"/lists/"+url.PathEscape(listID)+"/views/"+url.PathEscape(viewID)+"/objects", query, nil)
	if err != nil {
		return nil, err
	}
	opts := parseCompactOptions(args)

	rawData, _ := payload["data"].([]any)
	objects := make([]map[string]any, 0, len(rawData))
	for _, raw := range rawData {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		objects = append(objects, compactListObject(obj, opts))
	}

	out := map[string]any{
		"space_id":            spaceID,
		"list_id":             listID,
		"view_id":             viewID,
		"offset":              offset,
		"limit":               limit,
		"count":               len(objects),
		"objects":             objects,
		"api_base_url":        s.cfg.apiBaseURL,
		"api_version":         s.cfg.apiVersion,
		"properties_included": opts.includeProperties,
	}
	copyIfPresent(out, payload, "pagination")
	copyIfPresent(out, payload, "total")
	copyIfPresent(out, payload, "has_more")
	return out, nil
}

func (s *mcpServer) toolObjectGetCompact(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	if format := optionalString(args, "format"); format != "" {
		query.Set("format", format)
	}

	payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID)+"/objects/"+url.PathEscape(objectID), query, nil)
	if err != nil {
		return nil, err
	}
	obj, err := objectFromPayload(payload, "object")
	if err != nil {
		return nil, err
	}
	opts := parseCompactOptions(args)
	return map[string]any{
		"space_id":     spaceID,
		"object_id":    objectID,
		"object":       compactListObject(obj, opts),
		"api_base_url": s.cfg.apiBaseURL,
		"api_version":  s.cfg.apiVersion,
	}, nil
}

func (s *mcpServer) toolObjectsGetCompactMany(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectIDs := optionalStringSlice(args, "object_ids")
	if len(objectIDs) == 0 {
		return nil, fmt.Errorf("object_ids is required")
	}

	query := url.Values{}
	if format := optionalString(args, "format"); format != "" {
		query.Set("format", format)
	}

	opts := parseCompactOptions(args)
	stopOnError := optionalBool(args, "stop_on_error", false)
	results := make([]map[string]any, 0, len(objectIDs))
	okCount := 0
	errorCount := 0

	for i, objectID := range objectIDs {
		payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID)+"/objects/"+url.PathEscape(objectID), query, nil)
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
		obj, err := objectFromPayload(payload, "object")
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
		results = append(results, map[string]any{
			"index":     i,
			"object_id": objectID,
			"object":    compactListObject(obj, opts),
		})
		okCount++
	}

	return map[string]any{
		"space_id":            spaceID,
		"total":               len(objectIDs),
		"ok_count":            okCount,
		"error_count":         errorCount,
		"objects":             results,
		"api_base_url":        s.cfg.apiBaseURL,
		"api_version":         s.cfg.apiVersion,
		"properties_included": opts.includeProperties,
	}, nil
}

func (s *mcpServer) toolObjectsCreateCompactMany(args map[string]any) (map[string]any, error) {
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

	opts := parseCompactOptions(args)
	stopOnError := optionalBool(args, "stop_on_error", false)
	results := make([]map[string]any, 0, len(itemMaps))
	okCount := 0
	errorCount := 0

	for i, item := range itemMaps {
		body, err := createObjectRequestBody(item)
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

		payload, err := s.anytypeAPIRequest(http.MethodPost, "/v1/spaces/"+url.PathEscape(spaceID)+"/objects", nil, body)
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
		obj, err := objectFromPayload(payload, "object")
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

		objectID := stringValue(obj["id"])
		results = append(results, map[string]any{
			"index":     i,
			"object_id": objectID,
			"created":   true,
			"object":    compactListObject(obj, opts),
		})
		okCount++
	}

	return map[string]any{
		"space_id":            spaceID,
		"total":               len(itemMaps),
		"ok_count":            okCount,
		"error_count":         errorCount,
		"objects":             results,
		"api_base_url":        s.cfg.apiBaseURL,
		"api_version":         s.cfg.apiVersion,
		"properties_included": opts.includeProperties,
	}, nil
}

func (s *mcpServer) toolObjectUpdateCompact(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, err
	}

	body, err := updateObjectRequestBody(args)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("at least one update field is required: icon, markdown, name, properties, type_key")
	}

	payload, err := s.anytypeAPIRequest(http.MethodPatch, "/v1/spaces/"+url.PathEscape(spaceID)+"/objects/"+url.PathEscape(objectID), nil, body)
	if err != nil {
		return nil, err
	}
	obj, err := objectFromPayload(payload, "object")
	if err != nil {
		return nil, err
	}
	opts := parseCompactOptions(args)
	return map[string]any{
		"space_id":     spaceID,
		"object_id":    objectID,
		"updated":      true,
		"object":       compactListObject(obj, opts),
		"api_base_url": s.cfg.apiBaseURL,
		"api_version":  s.cfg.apiVersion,
	}, nil
}

func (s *mcpServer) toolObjectsUpdateCompactMany(args map[string]any) (map[string]any, error) {
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

	opts := parseCompactOptions(args)
	stopOnError := optionalBool(args, "stop_on_error", false)
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

		body, err := updateObjectRequestBody(item)
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
		if len(body) == 0 {
			results = append(results, map[string]any{
				"index":     i,
				"object_id": objectID,
				"error":     "at least one update field is required: icon, markdown, name, properties, type_key",
			})
			errorCount++
			if stopOnError {
				break
			}
			continue
		}

		payload, err := s.anytypeAPIRequest(http.MethodPatch, "/v1/spaces/"+url.PathEscape(spaceID)+"/objects/"+url.PathEscape(objectID), nil, body)
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
		obj, err := objectFromPayload(payload, "object")
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
		results = append(results, map[string]any{
			"index":     i,
			"object_id": objectID,
			"updated":   true,
			"object":    compactListObject(obj, opts),
		})
		okCount++
	}

	return map[string]any{
		"space_id":            spaceID,
		"total":               len(itemMaps),
		"ok_count":            okCount,
		"error_count":         errorCount,
		"objects":             results,
		"api_base_url":        s.cfg.apiBaseURL,
		"api_version":         s.cfg.apiVersion,
		"properties_included": opts.includeProperties,
	}, nil
}

func createObjectRequestBody(args map[string]any) (map[string]any, error) {
	if _, err := requiredString(args, "type_key"); err != nil {
		return nil, err
	}
	if err := validatePropertyLinks(args["properties"]); err != nil {
		return nil, err
	}
	body := map[string]any{}
	for _, key := range []string{"body", "icon", "name", "properties", "template_id", "type_key"} {
		if value, ok := args[key]; ok {
			body[key] = value
		}
	}
	return body, nil
}

func updateObjectRequestBody(args map[string]any) (map[string]any, error) {
	if err := validatePropertyLinks(args["properties"]); err != nil {
		return nil, err
	}
	body := map[string]any{}
	for _, key := range []string{"icon", "markdown", "name", "properties", "type_key"} {
		if value, ok := args[key]; ok {
			body[key] = value
		}
	}
	return body, nil
}

func (s *mcpServer) toolSearchCompact(args map[string]any, requireSpace bool) (map[string]any, error) {
	offset, limit, err := paginationArgs(args, 50)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))

	body := map[string]any{}
	for _, key := range []string{"query", "sort", "types"} {
		if value, ok := args[key]; ok && value != nil {
			body[key] = value
		}
	}
	path := "/v1/search"
	spaceID := optionalString(args, "space_id")
	if requireSpace {
		var err error
		spaceID, err = requiredString(args, "space_id")
		if err != nil {
			return nil, err
		}
		path = "/v1/spaces/" + url.PathEscape(spaceID) + "/search"
	} else if spaceID != "" {
		path = "/v1/spaces/" + url.PathEscape(spaceID) + "/search"
	}

	payload, err := s.anytypeAPIRequest(http.MethodPost, path, query, body)
	if err != nil {
		return nil, err
	}
	return compactPaginatedObjectsPayload(payload, parseCompactOptions(args), map[string]any{
		"space_id":     spaceID,
		"offset":       offset,
		"limit":        limit,
		"scope":        map[bool]string{true: "space", false: "global"}[spaceID != ""],
		"api_base_url": s.cfg.apiBaseURL,
		"api_version":  s.cfg.apiVersion,
	}), nil
}

func (s *mcpServer) toolSpaceObjectsCompact(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	offset, limit, err := paginationArgs(args, 50)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))
	if err := addFiltersQuery(query, args); err != nil {
		return nil, err
	}

	payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID)+"/objects", query, nil)
	if err != nil {
		return nil, err
	}
	return compactPaginatedObjectsPayload(payload, parseCompactOptions(args), map[string]any{
		"space_id":     spaceID,
		"offset":       offset,
		"limit":        limit,
		"api_base_url": s.cfg.apiBaseURL,
		"api_version":  s.cfg.apiVersion,
	}), nil
}

func (s *mcpServer) toolListTypesCompact(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	offset, limit, err := paginationArgs(args, 50)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))
	if err := addFiltersQuery(query, args); err != nil {
		return nil, err
	}

	payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID)+"/types", query, nil)
	if err != nil {
		return nil, err
	}
	opts := parseTypeCompactOptions(args)
	rawData, _ := payload["data"].([]any)
	types := make([]map[string]any, 0, len(rawData))
	for _, raw := range rawData {
		typeObj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		types = append(types, compactTypeDefinition(typeObj, opts))
	}

	out := map[string]any{
		"space_id":            spaceID,
		"offset":              offset,
		"limit":               limit,
		"count":               len(types),
		"types":               types,
		"api_base_url":        s.cfg.apiBaseURL,
		"api_version":         s.cfg.apiVersion,
		"properties_included": opts.includeProperties,
	}
	copyIfPresent(out, payload, "pagination")
	copyIfPresent(out, payload, "total")
	copyIfPresent(out, payload, "has_more")
	return out, nil
}

func (s *mcpServer) toolGetTypeCompact(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}

	payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID)+"/types/"+url.PathEscape(typeID), nil, nil)
	if err != nil {
		return nil, err
	}
	typeObj, err := objectFromPayload(payload, "type")
	if err != nil {
		return nil, err
	}
	opts := parseTypeCompactOptions(args)
	// A type's properties ARE its definition, and there are only a handful of
	// them, so fetching one type and getting no properties back is nearly
	// useless. They default to on here; list-types-compact keeps them opt-in
	// because that response multiplies them by the number of types.
	if _, explicit := args["include_properties"]; !explicit {
		opts.includeProperties = true
	}
	return map[string]any{
		"space_id":     spaceID,
		"type_id":      typeID,
		"type":         compactTypeDefinition(typeObj, opts),
		"api_base_url": s.cfg.apiBaseURL,
		"api_version":  s.cfg.apiVersion,
	}, nil
}

func (s *mcpServer) toolGetListViewsCompact(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	listID, err := requiredString(args, "list_id")
	if err != nil {
		return nil, err
	}
	offset, limit, err := paginationArgs(args, 50)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))

	payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID)+"/lists/"+url.PathEscape(listID)+"/views", query, nil)
	if err != nil {
		return nil, err
	}
	opts := parseViewCompactOptions(args)
	rawData, _ := payload["data"].([]any)
	views := make([]map[string]any, 0, len(rawData))
	for _, raw := range rawData {
		view, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		views = append(views, compactViewDefinition(view, opts))
	}

	out := map[string]any{
		"space_id":      spaceID,
		"list_id":       listID,
		"offset":        offset,
		"limit":         limit,
		"count":         len(views),
		"views":         views,
		"api_base_url":  s.cfg.apiBaseURL,
		"api_version":   s.cfg.apiVersion,
		"filters_shown": opts.includeFilters,
		"sorts_shown":   opts.includeSorts,
	}
	copyIfPresent(out, payload, "pagination")
	copyIfPresent(out, payload, "total")
	copyIfPresent(out, payload, "has_more")
	return out, nil
}

type compactOptions struct {
	fields            map[string]bool
	propertyKeys      map[string]bool
	includeProperties bool
	includeType       bool
	includeIcon       bool
	maxProperties     int
	maxStringLength   int
}

type typeCompactOptions struct {
	fields            map[string]bool
	propertyKeys      map[string]bool
	includeProperties bool
	includeIcon       bool
	maxProperties     int
	maxStringLength   int
}

type viewCompactOptions struct {
	fields          map[string]bool
	includeFilters  bool
	includeSorts    bool
	maxFilters      int
	maxSorts        int
	maxStringLength int
}

func parseCompactOptions(args map[string]any) compactOptions {
	fields := asStringSet(optionalStringSlice(args, "fields"))
	if len(fields) == 0 {
		fields = asStringSet([]string{"id", "name", "snippet", "layout", "archived", "space_id", "object"})
	}
	propertyKeys := asStringSet(optionalStringSlice(args, "property_keys"))
	maxProperties := optionalInt(args, "max_properties", 20)
	if maxProperties < 0 {
		maxProperties = 0
	}
	maxStringLength := optionalInt(args, "max_string_length", 500)
	if maxStringLength < 0 {
		maxStringLength = 0
	}
	return compactOptions{
		fields:            fields,
		propertyKeys:      propertyKeys,
		includeProperties: optionalBool(args, "include_properties", false) || len(propertyKeys) > 0 || fields["properties"],
		includeType:       optionalBool(args, "include_type", false) || fields["type"],
		includeIcon:       optionalBool(args, "include_icon", false) || fields["icon"],
		maxProperties:     maxProperties,
		maxStringLength:   maxStringLength,
	}
}

func parseTypeCompactOptions(args map[string]any) typeCompactOptions {
	fields := asStringSet(optionalStringSlice(args, "fields"))
	if len(fields) == 0 {
		fields = asStringSet([]string{"id", "key", "name", "plural_name", "layout", "archived", "object"})
	}
	propertyKeys := asStringSet(optionalStringSlice(args, "property_keys"))
	maxProperties := optionalInt(args, "max_properties", 20)
	if maxProperties < 0 {
		maxProperties = 0
	}
	maxStringLength := optionalInt(args, "max_string_length", 500)
	if maxStringLength < 0 {
		maxStringLength = 0
	}
	return typeCompactOptions{
		fields:            fields,
		propertyKeys:      propertyKeys,
		includeProperties: optionalBool(args, "include_properties", false) || len(propertyKeys) > 0 || fields["properties"],
		includeIcon:       optionalBool(args, "include_icon", false) || fields["icon"],
		maxProperties:     maxProperties,
		maxStringLength:   maxStringLength,
	}
}

func parseViewCompactOptions(args map[string]any) viewCompactOptions {
	fields := asStringSet(optionalStringSlice(args, "fields"))
	if len(fields) == 0 {
		fields = asStringSet([]string{"id", "name", "layout"})
	}
	maxFilters := optionalInt(args, "max_filters", 20)
	if maxFilters < 0 {
		maxFilters = 0
	}
	maxSorts := optionalInt(args, "max_sorts", 20)
	if maxSorts < 0 {
		maxSorts = 0
	}
	maxStringLength := optionalInt(args, "max_string_length", 500)
	if maxStringLength < 0 {
		maxStringLength = 0
	}
	return viewCompactOptions{
		fields:          fields,
		includeFilters:  optionalBool(args, "include_filters", false) || fields["filters"],
		includeSorts:    optionalBool(args, "include_sorts", false) || fields["sorts"],
		maxFilters:      maxFilters,
		maxSorts:        maxSorts,
		maxStringLength: maxStringLength,
	}
}

func compactListObject(obj map[string]any, opts compactOptions) map[string]any {
	out := make(map[string]any, len(opts.fields)+3)
	for field := range opts.fields {
		if field == "properties" || field == "type" || field == "icon" {
			continue
		}
		copyIfPresent(out, obj, field)
	}
	if opts.includeIcon {
		copyIfPresent(out, obj, "icon")
	}
	if opts.includeType {
		if typeObj, ok := obj["type"].(map[string]any); ok {
			out["type"] = compactTypeObject(typeObj, opts.maxStringLength)
		} else if value, ok := obj["type"]; ok {
			out["type"] = truncateAny(value, opts.maxStringLength)
		}
	}
	if opts.includeProperties {
		out["properties"] = compactProperties(obj["properties"], opts.propertyKeys, opts.maxProperties, opts.maxStringLength)
	}
	return truncateAny(out, opts.maxStringLength).(map[string]any)
}

func compactTypeDefinition(typeObj map[string]any, opts typeCompactOptions) map[string]any {
	out := make(map[string]any, len(opts.fields)+2)
	for field := range opts.fields {
		if field == "properties" || field == "icon" {
			continue
		}
		copyIfPresent(out, typeObj, field)
	}
	if opts.includeIcon {
		copyIfPresent(out, typeObj, "icon")
	}
	if opts.includeProperties {
		out["properties"] = compactPropertyDefinitions(typeObj["properties"], opts.propertyKeys, opts.maxProperties, opts.maxStringLength)
	}
	return truncateAny(out, opts.maxStringLength).(map[string]any)
}

func compactViewDefinition(view map[string]any, opts viewCompactOptions) map[string]any {
	out := make(map[string]any, len(opts.fields)+2)
	for field := range opts.fields {
		if field == "filters" || field == "sorts" {
			continue
		}
		copyIfPresent(out, view, field)
	}
	if opts.includeFilters {
		out["filters"] = compactArray(view["filters"], opts.maxFilters, opts.maxStringLength)
	}
	if opts.includeSorts {
		out["sorts"] = compactArray(view["sorts"], opts.maxSorts, opts.maxStringLength)
	}
	return truncateAny(out, opts.maxStringLength).(map[string]any)
}

func compactArray(raw any, limit int, maxStringLength int) []any {
	values, ok := raw.([]any)
	if !ok {
		return []any{}
	}
	if limit <= 0 || len(values) == 0 {
		return []any{}
	}
	if len(values) > limit {
		values = values[:limit]
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, truncateAny(value, maxStringLength))
	}
	return out
}

// anytypeAPIRequest returns the response as an object. A few endpoints answer
// with a bare JSON string instead (the list add/remove endpoints reply e.g.
// "Objects added successfully"), so non-object responses are wrapped under
// "result" rather than failing to decode.
func (s *mcpServer) anytypeAPIRequest(method string, path string, query url.Values, body any) (map[string]any, error) {
	raw, err := s.anytypeAPIRequestAny(method, path, query, body)
	if err != nil {
		return nil, err
	}
	if obj, ok := raw.(map[string]any); ok {
		return obj, nil
	}
	return map[string]any{"result": raw}, nil
}

func (s *mcpServer) anytypeAPIRequestAny(method string, path string, query url.Values, body any) (any, error) {
	if s.cfg.apiKey == "" {
		return nil, fmt.Errorf("ANYTYPE_API_KEY is required for compact Anytype REST tools")
	}

	endpoint, err := url.Parse(strings.TrimRight(s.cfg.apiBaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid ANYTYPE_API_BASE_URL: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode Anytype API request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.apiKey)
	req.Header.Set("Anytype-Version", s.cfg.apiVersion)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 64*1024*1024)
	var payload any
	if err := json.NewDecoder(limited).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode Anytype API response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Anytype API returned %s: %s", resp.Status, compactJSON(payload, 2000))
	}
	return payload, nil
}

func objectFromPayload(payload map[string]any, key string) (map[string]any, error) {
	obj, ok := payload[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Anytype API response did not contain %q object", key)
	}
	return obj, nil
}

func compactPaginatedObjectsPayload(payload map[string]any, opts compactOptions, base map[string]any) map[string]any {
	rawData, _ := payload["data"].([]any)
	objects := make([]map[string]any, 0, len(rawData))
	for _, raw := range rawData {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		objects = append(objects, compactListObject(obj, opts))
	}

	out := map[string]any{
		"count":               len(objects),
		"objects":             objects,
		"properties_included": opts.includeProperties,
	}
	for key, value := range base {
		if value != "" {
			out[key] = value
		}
	}
	copyIfPresent(out, payload, "pagination")
	copyIfPresent(out, payload, "total")
	copyIfPresent(out, payload, "has_more")
	return out
}

func paginationArgs(args map[string]any, defaultLimit int) (int, int, error) {
	offset := optionalInt(args, "offset", 0)
	if offset < 0 {
		return 0, 0, fmt.Errorf("offset must be >= 0")
	}
	limit := optionalInt(args, "limit", defaultLimit)
	if limit <= 0 {
		limit = defaultLimit
	}
	return offset, limit, nil
}

func addFiltersQuery(query url.Values, args map[string]any) error {
	rawFilters, ok := args["filters"]
	if !ok || rawFilters == nil {
		return nil
	}
	filterMap, ok := rawFilters.(map[string]any)
	if !ok {
		return fmt.Errorf("filters must be an object")
	}
	for key, raw := range filterMap {
		key = strings.TrimSpace(key)
		if key == "" || raw == nil {
			continue
		}
		addQueryValue(query, key, raw)
	}
	return nil
}

// withOverrides replaces individual entries of an already-built input schema.
//
// The compact*ToolSchema helpers are shared by several tools so that the common
// options stay worded identically. Where one tool genuinely differs — a default
// that is right for it and wrong for its neighbour — the entry is overridden
// here rather than by forking the helper, which would let the shared wording
// drift apart.
func withOverrides(schema map[string]any, overrides map[string]any) map[string]any {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return schema
	}
	for key, value := range overrides {
		props[key] = value
	}
	return schema
}

func compactObjectToolSchema(required []string, properties map[string]any) map[string]any {
	for key, value := range compactOptionSchemaProperties() {
		properties[key] = value
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func compactTypeToolSchema(required []string, properties map[string]any) map[string]any {
	for key, value := range compactTypeOptionSchemaProperties() {
		properties[key] = value
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func compactViewToolSchema(required []string, properties map[string]any) map[string]any {
	for key, value := range compactViewOptionSchemaProperties() {
		properties[key] = value
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func propertyLinkValueSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": propertyLinkValueDescription,
		"properties": map[string]any{
			"key": map[string]any{
				"type":        "string",
				"description": "Property key, property ID, or linked property name accepted by Anytype. Prefer the technical key from get-type-compact.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Text property value.",
			},
			"number": map[string]any{
				"type":        "number",
				"description": "Number property value.",
			},
			"select": map[string]any{
				"type":        "string",
				"description": "Select tag key or ID.",
			},
			"multi_select": map[string]any{
				"type":        "array",
				"description": "Select tag keys or IDs. Use [] to clear.",
				"items":       map[string]any{"type": "string"},
				"uniqueItems": false,
			},
			"date": map[string]any{
				"type":        "string",
				"description": "Date value, either RFC3339 datetime or YYYY-MM-DD.",
			},
			"files": map[string]any{
				"type":        "array",
				"description": "File object IDs. Use [] to clear file attachments.",
				"items":       map[string]any{"type": "string"},
				"uniqueItems": false,
			},
			"checkbox": map[string]any{
				"type":        "boolean",
				"description": "Checkbox property value.",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "URL property value.",
			},
			"email": map[string]any{
				"type":        "string",
				"description": "Email property value.",
			},
			"phone": map[string]any{
				"type":        "string",
				"description": "Phone property value.",
			},
			"objects": map[string]any{
				"type":        "array",
				"description": "Related object IDs for object/relation properties. Use [] to clear links.",
				"items":       map[string]any{"type": "string"},
				"uniqueItems": false,
			},
		},
		"required":             []string{"key"},
		"additionalProperties": false,
	}
}

func compactOptionSchemaProperties() map[string]any {
	return map[string]any{
		"fields": map[string]any{
			"type":        "array",
			"description": "Top-level object fields to include. Defaults to id, name, snippet, layout, archived, space_id, object. Examples: id, name, snippet, layout, archived, space_id, object, type, icon, properties, markdown. Do not put property keys or visible property names here.",
			"items":       map[string]any{"type": "string"},
		},
		"property_keys": map[string]any{
			"type":        "array",
			"description": "Property keys, property IDs, or visible property names to include in the compact properties object. This automatically enables property output. Do not put top-level fields like id or name here.",
			"items":       map[string]any{"type": "string"},
		},
		"include_properties": map[string]any{
			"type":        "boolean",
			"description": "Include simplified properties. Prefer property_keys when you know the exact properties. If property_keys is empty, only max_properties are included.",
			"default":     false,
		},
		"include_type": map[string]any{
			"type":        "boolean",
			"description": "Include compact object type metadata.",
			"default":     false,
		},
		"include_icon": map[string]any{
			"type":        "boolean",
			"description": "Include icon data.",
			"default":     false,
		},
		"max_properties": map[string]any{
			"type":        "integer",
			"description": "Maximum number of properties to include when include_properties is true and property_keys is empty.",
			"default":     20,
		},
		"max_string_length": map[string]any{
			"type":        "integer",
			"description": "Truncate returned string values longer than this many characters. Use 0 to disable truncation.",
			"default":     500,
		},
	}
}

func compactTypeOptionSchemaProperties() map[string]any {
	return map[string]any{
		"fields": map[string]any{
			"type":        "array",
			"description": "Top-level type fields to include. Defaults to id, key, name, plural_name, layout, archived, object. Do not put linked property keys here.",
			"items":       map[string]any{"type": "string"},
		},
		"property_keys": map[string]any{
			"type":        "array",
			"description": "Linked property keys, property IDs, or visible names to include in the compact type properties object. Use this to inspect formats before create/update property payloads.",
			"items":       map[string]any{"type": "string"},
		},
		"include_properties": map[string]any{
			"type":        "boolean",
			"description": "Include compact linked property definitions. If property_keys is empty, only max_properties are included.",
			"default":     false,
		},
		"include_icon": map[string]any{
			"type":        "boolean",
			"description": "Include icon data.",
			"default":     false,
		},
		"max_properties": map[string]any{
			"type":        "integer",
			"description": "Maximum number of linked properties to include when include_properties is true and property_keys is empty.",
			"default":     20,
		},
		"max_string_length": map[string]any{
			"type":        "integer",
			"description": "Truncate returned string values longer than this many characters. Use 0 to disable truncation.",
			"default":     500,
		},
	}
}

func compactViewOptionSchemaProperties() map[string]any {
	return map[string]any{
		"fields": map[string]any{
			"type":        "array",
			"description": "Top-level view fields to include. Defaults to id, name, layout. Do not put filters or sorts here; use include_filters/include_sorts.",
			"items":       map[string]any{"type": "string"},
		},
		"include_filters": map[string]any{
			"type":        "boolean",
			"description": "Include compact view filter definitions in the response. These are returned metadata, not request filters.",
			"default":     false,
		},
		"include_sorts": map[string]any{
			"type":        "boolean",
			"description": "Include compact view sort definitions in the response. These are returned metadata, not request sorts.",
			"default":     false,
		},
		"max_filters": map[string]any{
			"type":        "integer",
			"description": "Maximum number of filters to include when include_filters is true.",
			"default":     20,
		},
		"max_sorts": map[string]any{
			"type":        "integer",
			"description": "Maximum number of sorts to include when include_sorts is true.",
			"default":     20,
		},
		"max_string_length": map[string]any{
			"type":        "integer",
			"description": "Truncate returned string values longer than this many characters. Use 0 to disable truncation.",
			"default":     500,
		},
	}
}

func compactTypeObject(typeObj map[string]any, maxStringLength int) map[string]any {
	out := map[string]any{}
	for _, field := range []string{"id", "key", "name", "plural_name", "layout", "archived", "object"} {
		copyIfPresent(out, typeObj, field)
	}
	return truncateAny(out, maxStringLength).(map[string]any)
}

func compactProperties(raw any, propertyKeys map[string]bool, maxProperties int, maxStringLength int) map[string]any {
	rawProperties := normalizeRawProperties(raw)
	if len(rawProperties) == 0 {
		return map[string]any{}
	}

	out := map[string]any{}
	for _, rawProperty := range rawProperties {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		key := propertyOutputKey(property)
		if key == "" {
			continue
		}
		if len(propertyKeys) > 0 && !propertyMatches(property, propertyKeys) {
			continue
		}
		if len(propertyKeys) == 0 {
			if maxProperties == 0 || len(out) >= maxProperties {
				break
			}
		}

		entry := map[string]any{}
		for _, field := range []string{"id", "key", "name", "format"} {
			copyIfPresent(entry, property, field)
		}
		entry["value"] = compactPropertyValue(property, maxStringLength)
		out[key] = truncateAny(entry, maxStringLength)
	}
	return out
}

func compactPropertyDefinitions(raw any, propertyKeys map[string]bool, maxProperties int, maxStringLength int) map[string]any {
	rawProperties := normalizeRawProperties(raw)
	if len(rawProperties) == 0 {
		return map[string]any{}
	}

	out := map[string]any{}
	for _, rawProperty := range rawProperties {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		key := propertyOutputKey(property)
		if key == "" {
			continue
		}
		if len(propertyKeys) > 0 && !propertyMatches(property, propertyKeys) {
			continue
		}
		if len(propertyKeys) == 0 {
			if maxProperties == 0 || len(out) >= maxProperties {
				break
			}
		}

		entry := map[string]any{}
		for _, field := range []string{"object", "id", "key", "name", "format"} {
			copyIfPresent(entry, property, field)
		}
		out[key] = truncateAny(entry, maxStringLength)
	}
	return out
}

func propertyOutputKey(property map[string]any) string {
	key := stringValue(property["key"])
	if key != "" {
		return key
	}
	return stringValue(property["id"])
}

func normalizeRawProperties(raw any) []any {
	switch v := raw.(type) {
	case []any:
		return v
	case map[string]any:
		out := make([]any, 0, len(v))
		keys := make([]string, 0, len(v))
		for mapKey := range v {
			keys = append(keys, mapKey)
		}
		sort.Strings(keys)
		for _, mapKey := range keys {
			mapValue := v[mapKey]
			property, ok := mapValue.(map[string]any)
			if !ok {
				property = map[string]any{
					"key":   mapKey,
					"value": mapValue,
				}
			} else if stringValue(property["key"]) == "" {
				property["key"] = mapKey
			}
			out = append(out, property)
		}
		return out
	default:
		return nil
	}
}

func propertyMatches(property map[string]any, selectors map[string]bool) bool {
	for _, field := range []string{"key", "id", "name"} {
		value := stringValue(property[field])
		if value == "" {
			continue
		}
		if selectors[value] || selectors[normalizeSelector(value)] {
			return true
		}
	}
	return false
}

func compactPropertyValue(property map[string]any, maxStringLength int) any {
	valueFields := []string{
		"text",
		"number",
		"select",
		"multi_select",
		"selected",
		"selected_objects",
		"date",
		"files",
		"checkbox",
		"url",
		"email",
		"phone",
		"objects",
		"value",
	}
	values := map[string]any{}
	for _, field := range valueFields {
		value, ok := property[field]
		if !ok || value == nil {
			continue
		}
		values[field] = truncateAny(value, maxStringLength)
	}
	if len(values) == 1 {
		for _, value := range values {
			return value
		}
	}
	if len(values) > 0 {
		return values
	}
	return nil
}

func copyIfPresent(dst map[string]any, src map[string]any, key string) {
	if value, ok := src[key]; ok {
		dst[key] = value
	}
}

func addQueryValue(query url.Values, key string, raw any) {
	switch v := raw.(type) {
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, stringValue(item))
		}
		query.Set(key, strings.Join(parts, ","))
	case []string:
		query.Set(key, strings.Join(v, ","))
	default:
		query.Set(key, stringValue(v))
	}
}

func compactJSON(payload any, maxLength int) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("%v", payload)
	}
	text := string(data)
	if maxLength > 0 && len(text) > maxLength {
		return text[:maxLength] + "...(truncated)"
	}
	return text
}

func stringValue(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func truncateAny(value any, maxStringLength int) any {
	switch v := value.(type) {
	case string:
		if maxStringLength > 0 && len(v) > maxStringLength {
			return v[:maxStringLength] + "...(truncated)"
		}
		return v
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, truncateAny(item, maxStringLength))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = truncateAny(item, maxStringLength)
		}
		return out
	default:
		return value
	}
}

func optionalStringSlice(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			value := stringValue(item)
			if value != "" {
				out = append(out, value)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			value := strings.TrimSpace(item)
			if value != "" {
				out = append(out, value)
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}

func asStringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values)*2)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
			out[normalizeSelector(value)] = true
		}
	}
	return out
}

func normalizeSelector(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// propertyLinkValueFields are the typed value fields of a property-link entry,
// in the order anytype-heart tries them.
//
// The order is not cosmetic: heart's PropertyLinkWithValue.UnmarshalJSON
// (core/api/model/property.go) is a first-match switch over exactly these keys,
// so an entry carrying two of them is not refused — heart silently takes the
// first one in THIS order and drops the rest, and the write comes back 200 as
// though everything had landed.
var propertyLinkValueFields = []string{
	"text", "number", "select", "multi_select", "date",
	"files", "checkbox", "url", "email", "phone", "objects",
}

// validatePropertyLinks enforces the "key plus exactly one typed value field"
// rule that the tool descriptions promise and nothing else checks.
//
// Both halves of the rule need a guard, for opposite reasons. Zero value fields
// is caught by heart with a 400 ("could not determine property link value
// type"), but only after a round trip and with no mention of which entry was
// wrong. Two value fields is the dangerous half: heart accepts it, writes one
// of them and discards the other without a word, so a caller that sends
// {"key":"x","text":"a","number":1} is told it succeeded and loses the number.
//
// Presence is what counts, not the value: heart tests aux[field] != nil on a
// map[string]json.RawMessage, where an explicit null is still a present key.
// Matching that here keeps the two from disagreeing about what "sent" means.
func validatePropertyLinks(raw any) error {
	if raw == nil {
		return nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("properties must be a list of {key, <one typed value field>} objects")
	}
	for i, element := range entries {
		entry, ok := element.(map[string]any)
		if !ok {
			return fmt.Errorf("properties[%d]: must be an object with a key and one typed value field", i)
		}
		label := strconv.Itoa(i)
		if key := stringValue(entry["key"]); key != "" {
			label = fmt.Sprintf("%d (%q)", i, key)
		}
		present := make([]string, 0, 2)
		for _, field := range propertyLinkValueFields {
			if _, ok := entry[field]; ok {
				present = append(present, field)
			}
		}
		switch {
		case len(present) == 1:
			continue
		case len(present) == 0:
			extra := make([]string, 0, len(entry))
			for field := range entry {
				if field != "key" {
					extra = append(extra, field)
				}
			}
			sort.Strings(extra)
			msg := fmt.Sprintf("properties[%s]: needs exactly one typed value field (%s)",
				label, strings.Join(propertyLinkValueFields, ", "))
			if len(extra) > 0 {
				msg += fmt.Sprintf("; %s is not one of them", strings.Join(extra, ", "))
			}
			return fmt.Errorf("%s. Check the property's format with get-type-compact or list-properties", msg)
		default:
			return fmt.Errorf("properties[%s]: carries %d typed value fields (%s), but only one is allowed. "+
				"Anytype would keep %q and discard the rest without an error — split them into one entry per property",
				label, len(present), strings.Join(present, ", "), present[0])
		}
	}
	return nil
}
