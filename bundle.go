package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) handleBundle(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes+10*1024*1024)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart upload")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	ttl, err := a.ttlFromRequest(r.Form.Get("ttl"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	passwordHash, err := hashItemPassword(r.Form.Get("password"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid password")
		return
	}

	sources, closeSources, err := a.bundleSourcesFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer closeSources()
	if len(sources) == 0 {
		writeError(w, http.StatusBadRequest, "add at least one file or text note")
		return
	}

	item, err := a.store.CreateBundle(r.Form.Get("name"), a.now().Add(ttl), passwordHash, sources)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save bundle")
		return
	}
	if item.Size > a.cfg.MaxUploadBytes {
		_ = a.store.Delete(item.ID)
		writeError(w, http.StatusRequestEntityTooLarge, "bundle is larger than max upload size")
		return
	}

	a.writeCreatedItem(w, r, item)
}

func (a *App) bundleSourcesFromRequest(r *http.Request) ([]BundleSource, func(), error) {
	var sources []BundleSource
	var closers []io.Closer

	text := r.Form.Get("text")
	if strings.TrimSpace(text) != "" {
		name := cleanFilename(r.Form.Get("textName"))
		if name == "" {
			name = "note.txt"
		}
		if filepath.Ext(name) == "" {
			name += ".txt"
		}
		contentType := "text/plain; charset=utf-8"
		if previewFormat(name, contentType) == "markdown" {
			contentType = "text/markdown; charset=utf-8"
		}
		sources = append(sources, BundleSource{
			Filename:    name,
			ContentType: contentType,
			Src:         strings.NewReader(text),
		})
	}

	if r.MultipartForm != nil {
		fileHeaders := append([]*multipart.FileHeader{}, r.MultipartForm.File["files"]...)
		fileHeaders = append(fileHeaders, multipartFiles(r, "files[]")...)
		for _, header := range fileHeaders {
			file, err := header.Open()
			if err != nil {
				for _, closer := range closers {
					_ = closer.Close()
				}
				return nil, func() {}, fmt.Errorf("could not read file %q", cleanFilename(header.Filename))
			}
			closers = append(closers, file)

			filename := cleanFilename(header.Filename)
			contentType := strings.TrimSpace(header.Header.Get("Content-Type"))
			if contentType == "" {
				contentType = mime.TypeByExtension(filepath.Ext(filename))
			}
			sources = append(sources, BundleSource{
				Filename:    filename,
				ContentType: contentType,
				Src:         file,
			})
		}
	}

	return sources, func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}, nil
}

func multipartFiles(r *http.Request, key string) []*multipart.FileHeader {
	if r.MultipartForm == nil || len(r.MultipartForm.File[key]) == 0 {
		return nil
	}
	return r.MultipartForm.File[key]
}

func (a *App) handleBundleArchive(w http.ResponseWriter, r *http.Request, id string) {
	item, ok := a.loadBundleForHTTP(w, r, id)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": archiveFilename(item.Filename)}))
	archive := zip.NewWriter(w)
	defer archive.Close()

	for _, entry := range item.Entries {
		file, err := a.openBundleEntry(item.ID, entry.ID)
		if err != nil {
			return
		}
		header := &zip.FileHeader{
			Name:   entry.Filename,
			Method: zip.Deflate,
		}
		header.SetModTime(item.CreatedAt)
		header.SetMode(0o600)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			_ = file.Close()
			return
		}
		_, copyErr := io.Copy(writer, file)
		_ = file.Close()
		if copyErr != nil {
			return
		}
	}
}

func (a *App) handleBundleEntryContent(w http.ResponseWriter, r *http.Request, id, entryID string) {
	item, entry, ok := a.loadBundleEntryForHTTP(w, r, id, entryID)
	if !ok {
		return
	}
	file, err := a.openBundleEntry(item.ID, entry.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "bundle entry not found")
		return
	}
	defer file.Close()

	contentType := entry.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(entry.Filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Filename}))
	}
	http.ServeContent(w, r, entry.Filename, item.CreatedAt, file)
}

func (a *App) handleBundleEntryPreview(w http.ResponseWriter, r *http.Request, id, entryID string) {
	item, entry, ok := a.loadBundleEntryForHTTP(w, r, id, entryID)
	if !ok {
		return
	}
	format := previewFormat(entry.Filename, entry.ContentType)
	if format == "" {
		writeError(w, http.StatusUnsupportedMediaType, "preview is only available for txt and md entries")
		return
	}

	content, truncated, err := readPreview(a.store.EntryContentPath(item.ID, entry.ID), a.cfg.PreviewBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read preview")
		return
	}

	resp := map[string]any{
		"format":    format,
		"text":      string(content),
		"truncated": truncated,
	}
	if format == "markdown" {
		var rendered bytes.Buffer
		if err := a.markdown.Convert(content, &rendered); err != nil {
			writeError(w, http.StatusInternalServerError, "could not render markdown")
			return
		}
		resp["html"] = string(a.sanitizer.SanitizeBytes(rendered.Bytes()))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) loadBundleForHTTP(w http.ResponseWriter, r *http.Request, id string) (Item, bool) {
	item, ok := a.loadItemForHTTP(w, id)
	if !ok {
		return Item{}, false
	}
	if item.Kind != KindBundle {
		writeError(w, http.StatusNotFound, "bundle not found")
		return Item{}, false
	}
	if !a.requireItemUnlock(w, r, item) {
		return Item{}, false
	}
	return item, true
}

func (a *App) loadBundleEntryForHTTP(w http.ResponseWriter, r *http.Request, id, entryID string) (Item, BundleEntry, bool) {
	if !validID(entryID) {
		writeError(w, http.StatusNotFound, "bundle entry not found")
		return Item{}, BundleEntry{}, false
	}
	item, ok := a.loadBundleForHTTP(w, r, id)
	if !ok {
		return Item{}, BundleEntry{}, false
	}
	for _, entry := range item.Entries {
		if entry.ID == entryID {
			return item, entry, true
		}
	}
	writeError(w, http.StatusNotFound, "bundle entry not found")
	return Item{}, BundleEntry{}, false
}

func (a *App) openBundleEntry(itemID, entryID string) (*os.File, error) {
	return os.Open(a.store.EntryContentPath(itemID, entryID))
}

func archiveFilename(name string) string {
	name = cleanFilename(name)
	if name == "" {
		name = "bundle"
	}
	if strings.EqualFold(filepath.Ext(name), ".zip") {
		return name
	}
	return name + ".zip"
}
