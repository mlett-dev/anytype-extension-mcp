package main

// Unsplash, talked to directly rather than through anytype-heart.
//
// heart has its own Unsplash support, but it cannot be used with an ordinary
// Unsplash API key. Three reasons, all verified against v0.50.8 and the live
// API:
//
//   - heart builds its client with oauth2.StaticTokenSource, which sends
//     "Authorization: Bearer <token>". Unsplash rejects an Access Key in that
//     form; access keys must go as "Authorization: Client-ID <key>". Same key,
//     same endpoint: Bearer gives 401, Client-ID gives results.
//   - A Bearer token can be minted from the key pair via the client_credentials
//     grant, but Unsplash issues those with expires_in 1800. heart reads
//     UNSPLASH_KEY once in package init(), so a static environment value would
//     work for half an hour and then quietly start failing.
//   - heart routes through Anytype's own proxy (unsplash.anytype.io). Even when
//     that works, it puts our traffic on their infrastructure.
//
// Going direct avoids all three and needs nothing on the Anytype server: the
// key lives in this server's environment, and images reach Anytype through the
// same staged-file upload that file-upload already uses.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const unsplashAPI = "https://api.unsplash.com"

type unsplashPhoto struct {
	ID               string
	Description      string
	ImageURL         string
	DownloadLocation string
	Artist           string
	ArtistURL        string
}

func (s *mcpServer) unsplashClient() (*http.Client, string, error) {
	if strings.TrimSpace(s.cfg.unsplashKey) == "" {
		return nil, "", fmt.Errorf(
			"no Unsplash key configured: set UNSPLASH_ACCESS_KEY for this server " +
				"(an Access Key from unsplash.com/developers)")
	}
	return &http.Client{Timeout: 30 * time.Second}, s.cfg.unsplashKey, nil
}

// unsplashGet performs an authenticated GET against the Unsplash API.
func (s *mcpServer) unsplashGet(rawURL string, into any) error {
	client, key, err := s.unsplashClient()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	// Client-ID, not Bearer: see the note at the top of this file.
	req.Header.Set("Authorization", "Client-ID "+key)
	req.Header.Set("Accept-Version", "v1")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Unsplash request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("Unsplash rejected the key (401). Check UNSPLASH_ACCESS_KEY is an Access Key from unsplash.com/developers")
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Unsplash refused the request (403); the demo tier allows 50 requests per hour: %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Unsplash returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(body, into)
}

func photoFromJSON(raw map[string]any) unsplashPhoto {
	photo := unsplashPhoto{ID: asString(raw["id"])}
	photo.Description = asString(raw["description"])
	if photo.Description == "" {
		photo.Description = asString(raw["alt_description"])
	}
	if urls, ok := raw["urls"].(map[string]any); ok {
		photo.ImageURL = asString(urls["regular"])
		if photo.ImageURL == "" {
			photo.ImageURL = asString(urls["full"])
		}
	}
	if links, ok := raw["links"].(map[string]any); ok {
		photo.DownloadLocation = asString(links["download_location"])
	}
	if user, ok := raw["user"].(map[string]any); ok {
		photo.Artist = asString(user["name"])
		if userLinks, ok := user["links"].(map[string]any); ok {
			photo.ArtistURL = asString(userLinks["html"])
		}
	}
	return photo
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *mcpServer) unsplashSearch(query string, limit int) ([]unsplashPhoto, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30 // Unsplash caps per_page at 30
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("per_page", strconv.Itoa(limit))

	var payload struct {
		Total   int              `json:"total"`
		Results []map[string]any `json:"results"`
	}
	if err := s.unsplashGet(unsplashAPI+"/search/photos?"+params.Encode(), &payload); err != nil {
		return nil, err
	}
	out := make([]unsplashPhoto, 0, len(payload.Results))
	for _, raw := range payload.Results {
		out = append(out, photoFromJSON(raw))
	}
	return out, nil
}

func (s *mcpServer) unsplashPhoto(pictureID string) (unsplashPhoto, error) {
	var raw map[string]any
	if err := s.unsplashGet(unsplashAPI+"/photos/"+url.PathEscape(pictureID), &raw); err != nil {
		return unsplashPhoto{}, err
	}
	return photoFromJSON(raw), nil
}

// trackDownload hits Unsplash's download endpoint.
//
// This is required by the Unsplash API terms whenever a photo is actually used
// — not when it is merely listed — and skipping it is grounds for having the
// key revoked. A failure here is reported but does not sink the download: the
// image is already on its way and the caller should still get it.
func (s *mcpServer) trackDownload(photo unsplashPhoto) error {
	if photo.DownloadLocation == "" {
		return nil
	}
	return s.unsplashGet(photo.DownloadLocation, nil)
}

// fetchUnsplashImage downloads the picture into the shared input directory and
// returns the host path plus the path the Anytype server sees.
func (s *mcpServer) fetchUnsplashImage(photo unsplashPhoto) (string, string, error) {
	if photo.ImageURL == "" {
		return "", "", fmt.Errorf("Unsplash returned no image URL for %s", photo.ID)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(photo.ImageURL)
	if err != nil {
		return "", "", fmt.Errorf("downloading the image failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("downloading the image returned HTTP %d", resp.StatusCode)
	}

	ext := ".jpg"
	switch {
	case strings.Contains(resp.Header.Get("Content-Type"), "png"):
		ext = ".png"
	case strings.Contains(resp.Header.Get("Content-Type"), "webp"):
		ext = ".webp"
	}
	// The picture id keeps the name collision-proof across concurrent calls.
	hostPath := filepath.Join(s.cfg.inRoot, "unsplash-"+photo.ID+ext)
	file, err := os.Create(hostPath)
	if err != nil {
		return "", "", fmt.Errorf("cannot stage the image in %s: %w", s.cfg.inRoot, err)
	}
	if _, err := io.Copy(file, io.LimitReader(resp.Body, 64<<20)); err != nil {
		file.Close()
		os.Remove(hostPath)
		return "", "", fmt.Errorf("writing the staged image failed: %w", err)
	}
	file.Close()

	serverPath, err := mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, hostPath)
	if err != nil {
		os.Remove(hostPath)
		return "", "", fmt.Errorf("failed to map the staged image for the server: %w", err)
	}
	return hostPath, serverPath, nil
}
