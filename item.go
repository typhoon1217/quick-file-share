package main

import (
	"path/filepath"
	"strings"
	"time"
)

const (
	KindFile   = "file"
	KindText   = "text"
	KindBundle = "bundle"
)

type Item struct {
	ID           string        `json:"id"`
	Kind         string        `json:"kind"`
	Filename     string        `json:"filename"`
	ContentType  string        `json:"contentType"`
	Size         int64         `json:"size"`
	CreatedAt    time.Time     `json:"createdAt"`
	ExpiresAt    time.Time     `json:"expiresAt"`
	DeleteToken  string        `json:"deleteToken"`
	PasswordHash string        `json:"passwordHash,omitempty"`
	Entries      []BundleEntry `json:"entries,omitempty"`
}

type BundleEntry struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

type PublicItem struct {
	ID                string              `json:"id"`
	Kind              string              `json:"kind"`
	Filename          string              `json:"filename"`
	ContentType       string              `json:"contentType"`
	Size              int64               `json:"size"`
	SizeLabel         string              `json:"sizeLabel"`
	CreatedAt         time.Time           `json:"createdAt"`
	ExpiresAt         time.Time           `json:"expiresAt"`
	SecondsRemaining  int64               `json:"secondsRemaining"`
	Previewable       bool                `json:"previewable"`
	PreviewFormat     string              `json:"previewFormat"`
	PasswordProtected bool                `json:"passwordProtected"`
	Unlocked          bool                `json:"unlocked"`
	EntryCount        int                 `json:"entryCount,omitempty"`
	Entries           []PublicBundleEntry `json:"entries,omitempty"`
	DownloadURL       string              `json:"downloadUrl"`
	ContentURL        string              `json:"contentUrl"`
	QRURL             string              `json:"qrUrl"`
}

type PublicBundleEntry struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	ContentType   string `json:"contentType"`
	Size          int64  `json:"size"`
	SizeLabel     string `json:"sizeLabel"`
	Previewable   bool   `json:"previewable"`
	PreviewFormat string `json:"previewFormat"`
	Image         bool   `json:"image"`
	DownloadURL   string `json:"downloadUrl"`
	ContentURL    string `json:"contentUrl"`
	PreviewURL    string `json:"previewUrl,omitempty"`
}

func (item Item) Public(now time.Time, basePath string) PublicItem {
	remaining := int64(time.Until(item.ExpiresAt).Seconds())
	if !now.IsZero() {
		remaining = int64(item.ExpiresAt.Sub(now).Seconds())
	}
	if remaining < 0 {
		remaining = 0
	}
	format := item.PreviewFormat()
	public := PublicItem{
		ID:                item.ID,
		Kind:              item.Kind,
		Filename:          item.Filename,
		ContentType:       item.ContentType,
		Size:              item.Size,
		SizeLabel:         formatBytes(item.Size),
		CreatedAt:         item.CreatedAt,
		ExpiresAt:         item.ExpiresAt,
		SecondsRemaining:  remaining,
		Previewable:       format != "",
		PreviewFormat:     format,
		PasswordProtected: item.PasswordProtected(),
		Unlocked:          !item.PasswordProtected(),
		DownloadURL:       joinBasePath(basePath, "/api/items/"+item.ID+"/content?download=1"),
		ContentURL:        joinBasePath(basePath, "/api/items/"+item.ID+"/content"),
		QRURL:             joinBasePath(basePath, "/api/items/"+item.ID+"/qr.png"),
	}
	if item.Kind == KindBundle {
		public.Previewable = len(item.Entries) > 0
		public.PreviewFormat = "bundle"
		public.EntryCount = len(item.Entries)
		public.DownloadURL = joinBasePath(basePath, "/api/items/"+item.ID+"/archive.zip")
		public.ContentURL = public.DownloadURL
		public.Entries = item.PublicEntries(basePath)
	}
	return public
}

func (item Item) PublicEntries(basePath string) []PublicBundleEntry {
	if len(item.Entries) == 0 {
		return nil
	}
	entries := make([]PublicBundleEntry, 0, len(item.Entries))
	for _, entry := range item.Entries {
		format := previewFormat(entry.Filename, entry.ContentType)
		contentURL := joinBasePath(basePath, "/api/items/"+item.ID+"/entries/"+entry.ID+"/content")
		public := PublicBundleEntry{
			ID:            entry.ID,
			Filename:      entry.Filename,
			ContentType:   entry.ContentType,
			Size:          entry.Size,
			SizeLabel:     formatBytes(entry.Size),
			Previewable:   format != "" || isImageContent(entry.Filename, entry.ContentType),
			PreviewFormat: format,
			Image:         isImageContent(entry.Filename, entry.ContentType),
			DownloadURL:   contentURL + "?download=1",
			ContentURL:    contentURL,
		}
		if format != "" {
			public.PreviewURL = joinBasePath(basePath, "/api/items/"+item.ID+"/entries/"+entry.ID+"/preview")
		}
		entries = append(entries, public)
	}
	return entries
}

func (item Item) Expired(now time.Time) bool {
	return !item.ExpiresAt.After(now)
}

func (item Item) PasswordProtected() bool {
	return strings.TrimSpace(item.PasswordHash) != ""
}

func (item Item) PreviewFormat() string {
	return previewFormat(item.Filename, item.ContentType)
}

func previewFormat(filename, rawContentType string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	contentType := strings.ToLower(strings.Split(rawContentType, ";")[0])

	switch {
	case ext == ".md" || ext == ".markdown" || contentType == "text/markdown":
		return "markdown"
	case ext == ".txt" || contentType == "text/plain":
		return "text"
	default:
		return ""
	}
}

func isImageContent(filename, rawContentType string) bool {
	contentType := strings.ToLower(strings.Split(rawContentType, ";")[0])
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".avif", ".gif", ".jpg", ".jpeg", ".png", ".svg", ".webp":
		return true
	default:
		return false
	}
}
