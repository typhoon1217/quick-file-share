package main

import (
	"path/filepath"
	"strings"
	"time"
)

const (
	KindFile = "file"
	KindText = "text"
)

type Item struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"contentType"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	DeleteToken string    `json:"deleteToken"`
}

type PublicItem struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	Filename         string    `json:"filename"`
	ContentType      string    `json:"contentType"`
	Size             int64     `json:"size"`
	SizeLabel        string    `json:"sizeLabel"`
	CreatedAt        time.Time `json:"createdAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
	SecondsRemaining int64     `json:"secondsRemaining"`
	Previewable      bool      `json:"previewable"`
	PreviewFormat    string    `json:"previewFormat"`
	DownloadURL      string    `json:"downloadUrl"`
	ContentURL       string    `json:"contentUrl"`
	QRURL            string    `json:"qrUrl"`
}

func (item Item) Public(now time.Time) PublicItem {
	remaining := int64(time.Until(item.ExpiresAt).Seconds())
	if !now.IsZero() {
		remaining = int64(item.ExpiresAt.Sub(now).Seconds())
	}
	if remaining < 0 {
		remaining = 0
	}
	format := item.PreviewFormat()
	return PublicItem{
		ID:               item.ID,
		Kind:             item.Kind,
		Filename:         item.Filename,
		ContentType:      item.ContentType,
		Size:             item.Size,
		SizeLabel:        formatBytes(item.Size),
		CreatedAt:        item.CreatedAt,
		ExpiresAt:        item.ExpiresAt,
		SecondsRemaining: remaining,
		Previewable:      format != "",
		PreviewFormat:    format,
		DownloadURL:      "/api/items/" + item.ID + "/content?download=1",
		ContentURL:       "/api/items/" + item.ID + "/content",
		QRURL:            "/api/items/" + item.ID + "/qr.png",
	}
}

func (item Item) Expired(now time.Time) bool {
	return !item.ExpiresAt.After(now)
}

func (item Item) PreviewFormat() string {
	ext := strings.ToLower(filepath.Ext(item.Filename))
	contentType := strings.ToLower(strings.Split(item.ContentType, ";")[0])

	switch {
	case ext == ".md" || ext == ".markdown" || contentType == "text/markdown":
		return "markdown"
	case ext == ".txt" || contentType == "text/plain":
		return "text"
	default:
		return ""
	}
}
