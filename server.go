package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/skip2/go-qrcode"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

//go:embed web/*
var embeddedWeb embed.FS

type App struct {
	cfg       Config
	store     *Store
	authToken string
	markdown  goldmark.Markdown
	sanitizer *bluemonday.Policy
	index     *template.Template
	now       func() time.Time
}

func NewApp(cfg Config) (*App, error) {
	now := func() time.Time { return time.Now().UTC() }
	store := NewStore(cfg.DataDir, now)
	if err := store.Init(); err != nil {
		return nil, err
	}

	authToken, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	index, err := template.ParseFS(embeddedWeb, "web/index.html")
	if err != nil {
		return nil, err
	}

	return &App{
		cfg:       cfg,
		store:     store,
		authToken: authToken,
		markdown: goldmark.New(
			goldmark.WithExtensions(extension.GFM),
			goldmark.WithRendererOptions(html.WithUnsafe()),
		),
		sanitizer: bluemonday.UGCPolicy(),
		index:     index,
		now:       now,
	}, nil
}

func (a *App) Handler() http.Handler {
	webRoot, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		panic(err)
	}

	public := http.NewServeMux()
	public.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(webRoot))))
	public.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	public.HandleFunc("/login", a.handleLogin)
	public.HandleFunc("/logout", a.handleLogout)

	protected := http.NewServeMux()
	protected.HandleFunc("/", a.requireMethod(http.MethodGet, a.handleIndex))
	protected.HandleFunc("/s/", a.requireMethod(http.MethodGet, a.handleIndex))
	protected.HandleFunc("/api/config", a.requireMethod(http.MethodGet, a.handleConfig))
	protected.HandleFunc("/api/upload", a.requireMethod(http.MethodPost, a.handleUpload))
	protected.HandleFunc("/api/text", a.requireMethod(http.MethodPost, a.handleText))
	protected.HandleFunc("/api/items/", a.handleItems)
	public.Handle("/", a.requireAuth(protected))

	handler := http.Handler(public)
	if a.cfg.BasePath != "" {
		prefixed := http.NewServeMux()
		prefixed.HandleFunc(a.cfg.BasePath, func(w http.ResponseWriter, r *http.Request) {
			target := a.publicPath("/")
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		})
		prefixed.Handle(a.cfg.BasePath+"/", http.StripPrefix(a.cfg.BasePath, handler))
		handler = prefixed
	}

	return a.logRequests(a.securityHeaders(handler))
}

func (a *App) StartCleanup(ctx context.Context) {
	if removed, err := a.store.CleanupExpired(); err != nil {
		log.Printf("cleanup failed: %v", err)
	} else if removed > 0 {
		log.Printf("cleanup removed %d expired item(s)", removed)
	}

	ticker := time.NewTicker(a.cfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if removed, err := a.store.CleanupExpired(); err != nil {
				log.Printf("cleanup failed: %v", err)
			} else if removed > 0 {
				log.Printf("cleanup removed %d expired item(s)", removed)
			}
		}
	}
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/s/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.index.ExecuteTemplate(w, "index.html", map[string]string{"BasePath": a.cfg.BasePath})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.cfg.AccessPassword == "" {
		http.Redirect(w, r, a.publicPath("/"), http.StatusSeeOther)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginTemplate.Execute(w, map[string]string{
			"BasePath": a.cfg.BasePath,
			"Error":    r.URL.Query().Get("error"),
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, a.publicPath("/login?error=bad-request"), http.StatusSeeOther)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Form.Get("password")), []byte(a.cfg.AccessPassword)) != 1 {
			http.Redirect(w, r, a.publicPath("/login?error=invalid"), http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "qfs_session",
			Value:    a.authToken,
			Path:     a.cookiePath(),
			HttpOnly: true,
			Secure:   a.requestIsHTTPS(r),
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, a.publicPath("/"), http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "qfs_session",
		Value:    "",
		Path:     a.cookiePath(),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, a.publicPath("/login"), http.StatusSeeOther)
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"maxUploadBytes":  a.cfg.MaxUploadBytes,
		"maxUploadLabel":  formatBytes(a.cfg.MaxUploadBytes),
		"defaultTTL":      formatDurationLabel(a.cfg.DefaultTTL),
		"defaultTTLValue": durationFormValue(a.cfg.DefaultTTL),
		"passwordEnabled": a.cfg.AccessPassword != "",
		"basePath":        a.cfg.BasePath,
	})
}

func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
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

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	if header.Size > a.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file is larger than max upload size")
		return
	}

	ttl, err := a.ttlFromRequest(r.Form.Get("ttl"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	filename := cleanFilename(header.Filename)
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(filename))
	}

	passwordHash, err := hashItemPassword(r.Form.Get("password"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid password")
		return
	}

	item, err := a.store.Create(KindFile, filename, contentType, a.now().Add(ttl), passwordHash, io.LimitReader(file, a.cfg.MaxUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save upload")
		return
	}
	if item.Size > a.cfg.MaxUploadBytes {
		_ = a.store.Delete(item.ID)
		writeError(w, http.StatusRequestEntityTooLarge, "file is larger than max upload size")
		return
	}

	a.writeCreatedItem(w, r, item)
}

func (a *App) handleText(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes)
	var req struct {
		Text     string `json:"text"`
		Name     string `json:"name"`
		TTL      string `json:"ttl"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if int64(len(req.Text)) > a.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "text is larger than max upload size")
		return
	}

	ttl, err := a.ttlFromRequest(req.TTL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	filename := cleanFilename(req.Name)
	if filename == "" {
		filename = "note.txt"
	}
	if filepath.Ext(filename) == "" {
		filename += ".txt"
	}

	contentType := "text/plain; charset=utf-8"
	if format := (Item{Filename: filename, ContentType: contentType}).PreviewFormat(); format == "markdown" {
		contentType = "text/markdown; charset=utf-8"
	}

	passwordHash, err := hashItemPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid password")
		return
	}

	item, err := a.store.Create(KindText, filename, contentType, a.now().Add(ttl), passwordHash, strings.NewReader(req.Text))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save text")
		return
	}

	a.writeCreatedItem(w, r, item)
}

func (a *App) handleItems(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/items/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" || !validID(parts[0]) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}

	id := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		a.handleItemMetadata(w, r, id)
	case len(parts) == 1 && (r.Method == http.MethodDelete || r.Method == http.MethodPost):
		a.handleItemDelete(w, r, id)
	case len(parts) == 2 && parts[1] == "content" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		a.handleItemContent(w, r, id)
	case len(parts) == 2 && parts[1] == "preview" && r.Method == http.MethodGet:
		a.handleItemPreview(w, r, id)
	case len(parts) == 2 && parts[1] == "unlock" && r.Method == http.MethodPost:
		a.handleItemUnlock(w, r, id)
	case len(parts) == 2 && parts[1] == "qr.png" && r.Method == http.MethodGet:
		a.handleItemQR(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "item not found")
	}
}

func (a *App) handleItemMetadata(w http.ResponseWriter, r *http.Request, id string) {
	item, ok := a.loadItemForHTTP(w, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.publicItem(r, item))
}

func (a *App) handleItemContent(w http.ResponseWriter, r *http.Request, id string) {
	item, ok := a.loadItemForHTTP(w, id)
	if !ok {
		return
	}
	if !a.requireItemUnlock(w, r, item) {
		return
	}

	file, err := os.Open(a.store.ContentPath(id))
	if err != nil {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	defer file.Close()

	contentType := item.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(item.Filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Filename}))
	}
	http.ServeContent(w, r, item.Filename, item.CreatedAt, file)
}

func (a *App) handleItemPreview(w http.ResponseWriter, r *http.Request, id string) {
	item, ok := a.loadItemForHTTP(w, id)
	if !ok {
		return
	}
	if !a.requireItemUnlock(w, r, item) {
		return
	}
	format := item.PreviewFormat()
	if format == "" {
		writeError(w, http.StatusUnsupportedMediaType, "preview is only available for txt and md files")
		return
	}

	content, truncated, err := readPreview(a.store.ContentPath(id), a.cfg.PreviewBytes)
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

func (a *App) handleItemUnlock(w http.ResponseWriter, r *http.Request, id string) {
	item, ok := a.loadItemForHTTP(w, id)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !itemPasswordMatches(item.PasswordHash, req.Password) {
		writeError(w, http.StatusForbidden, "invalid password")
		return
	}
	a.setItemAccessCookie(w, r, item)
	writeJSON(w, http.StatusOK, map[string]bool{"unlocked": true})
}

func (a *App) handleItemQR(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := a.loadItemForHTTP(w, id); !ok {
		return
	}
	png, err := qrcode.Encode(a.absoluteURL(r, "/s/"+id), qrcode.Medium, 256)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate qr code")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (a *App) handleItemDelete(w http.ResponseWriter, r *http.Request, id string) {
	item, ok := a.loadItemForHTTP(w, id)
	if !ok {
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" && r.Method == http.MethodPost {
		var req struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		token = req.Token
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(item.DeleteToken)) != 1 {
		writeError(w, http.StatusForbidden, "invalid delete token")
		return
	}
	if err := a.store.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete item")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *App) writeCreatedItem(w http.ResponseWriter, r *http.Request, item Item) {
	public := item.Public(a.now(), a.cfg.BasePath)
	if item.PasswordProtected() {
		public.Unlocked = true
		a.setItemAccessCookie(w, r, item)
	}
	sharePath := "/s/" + item.ID
	writeJSON(w, http.StatusCreated, map[string]any{
		"item":        public,
		"shareUrl":    a.absoluteURL(r, sharePath),
		"deleteUrl":   a.absoluteURL(r, "/api/items/"+item.ID+"?token="+url.QueryEscape(item.DeleteToken)),
		"deleteToken": item.DeleteToken,
	})
}

func (a *App) loadItemForHTTP(w http.ResponseWriter, id string) (Item, bool) {
	item, err := a.store.Get(id)
	if err == nil {
		return item, true
	}
	switch {
	case errors.Is(err, ErrExpired):
		writeError(w, http.StatusGone, "item expired")
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "item not found")
	default:
		writeError(w, http.StatusInternalServerError, "could not load item")
	}
	return Item{}, false
}

func (a *App) publicItem(r *http.Request, item Item) PublicItem {
	public := item.Public(a.now(), a.cfg.BasePath)
	if item.PasswordProtected() {
		public.Unlocked = a.itemUnlocked(r, item)
	}
	return public
}

func (a *App) requireItemUnlock(w http.ResponseWriter, r *http.Request, item Item) bool {
	if a.itemUnlocked(r, item) {
		return true
	}
	writeError(w, http.StatusUnauthorized, "item password required")
	return false
}

func (a *App) itemUnlocked(r *http.Request, item Item) bool {
	if !item.PasswordProtected() {
		return true
	}
	cookie, err := r.Cookie(a.itemCookieName(item.ID))
	if err != nil {
		return false
	}
	expected := a.itemAccessToken(item)
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expected)) == 1
}

func (a *App) setItemAccessCookie(w http.ResponseWriter, r *http.Request, item Item) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.itemCookieName(item.ID),
		Value:    a.itemAccessToken(item),
		Path:     a.cookiePath(),
		HttpOnly: true,
		Secure:   a.requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) itemCookieName(id string) string {
	return "qfs_item_" + id
}

func (a *App) itemAccessToken(item Item) string {
	mac := hmac.New(sha256.New, []byte(a.authToken))
	_, _ = mac.Write([]byte(item.ID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(item.PasswordHash))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) ttlFromRequest(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return a.cfg.DefaultTTL, nil
	}
	ttl, err := parseDurationWithDays(raw)
	if err != nil || ttl <= 0 {
		return 0, fmt.Errorf("invalid ttl")
	}
	return ttl, nil
}

func (a *App) absoluteURL(r *http.Request, path string) string {
	if a.cfg.PublicBaseURL != "" {
		return a.cfg.PublicBaseURL + a.publicPath(path)
	}
	scheme := "http"
	if a.requestIsHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	return scheme + "://" + host + a.publicPath(path)
}

func (a *App) publicPath(path string) string {
	return joinBasePath(a.cfg.BasePath, path)
}

func (a *App) cookiePath() string {
	if a.cfg.BasePath == "" {
		return "/"
	}
	return a.cfg.BasePath
}

func (a *App) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		return strings.EqualFold(strings.TrimSpace(strings.Split(forwarded, ",")[0]), "https")
	}
	return false
}

func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.AccessPassword == "" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("qfs_session")
		if err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.authToken)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		http.Redirect(w, r, a.publicPath("/login"), http.StatusSeeOther)
	})
}

func (a *App) requireMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next(w, r)
	}
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; script-src 'self'; style-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func readPreview(path string, limit int64) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	limited := io.LimitReader(file, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Quick File Share</title>
  <link rel="stylesheet" href="{{.BasePath}}/assets/styles.css">
</head>
<body class="login-body">
  <main class="login-panel">
    <h1>Quick File Share</h1>
    <form method="post" action="{{.BasePath}}/login" class="login-form">
      <label for="password">Access password</label>
      <input id="password" name="password" type="password" autofocus autocomplete="current-password">
      {{if .Error}}<p class="error-text">Invalid password</p>{{end}}
      <button type="submit">Enter</button>
    </form>
  </main>
</body>
</html>`))
