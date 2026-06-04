package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseHumanBytes(t *testing.T) {
	got, err := parseHumanBytes("5GB")
	if err != nil {
		t.Fatal(err)
	}
	if got != 5*1024*1024*1024 {
		t.Fatalf("got %d", got)
	}
}

func TestTextCreatePreviewAndDelete(t *testing.T) {
	app := testApp(t)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	body := strings.NewReader(`{"name":"hello.md","text":"# Hello\n\ncopy me","ttl":"1h"}`)
	res, err := http.Post(server.URL+"/api/text", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", res.StatusCode)
	}

	var created struct {
		Item        PublicItem `json:"item"`
		DeleteToken string     `json:"deleteToken"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Item.PreviewFormat != "markdown" {
		t.Fatalf("preview format = %q", created.Item.PreviewFormat)
	}

	preview, err := http.Get(server.URL + "/api/items/" + created.Item.ID + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Body.Close()
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("preview status %d", preview.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/items/"+created.Item.ID+"?token="+created.DeleteToken, nil)
	deleted, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer deleted.Body.Close()
	if deleted.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d", deleted.StatusCode)
	}
}

func TestUploadRejectsTooLargeFile(t *testing.T) {
	app := testApp(t)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "too-big.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(bytes.Repeat([]byte("a"), int(app.cfg.MaxUploadBytes)+1))
	_ = writer.WriteField("ttl", "1h")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := http.Post(server.URL+"/api/upload", writer.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestExpiredItemIsDeletedOnRead(t *testing.T) {
	app := testApp(t)
	now := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	app.now = func() time.Time { return now }
	app.store.now = app.now

	item, err := app.store.Create(KindText, "old.txt", "text/plain; charset=utf-8", now.Add(time.Hour), "", strings.NewReader("old"))
	if err != nil {
		t.Fatal(err)
	}

	app.now = func() time.Time { return now.Add(2 * time.Hour) }
	app.store.now = app.now
	_, err = app.store.Get(item.ID)
	if err != ErrExpired {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(app.store.ContentPath(item.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("expected deleted content, stat err = %v", statErr)
	}
}

func TestItemPasswordProtectsPreviewAndContent(t *testing.T) {
	app := testApp(t)
	app.cfg.MaxUploadBytes = 1024
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	body := strings.NewReader(`{"name":"secret.txt","text":"copy me","ttl":"1h","password":"open sesame"}`)
	res, err := http.Post(server.URL+"/api/text", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", res.StatusCode)
	}

	var created struct {
		Item PublicItem `json:"item"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !created.Item.PasswordProtected {
		t.Fatal("expected passwordProtected")
	}
	if !created.Item.Unlocked {
		t.Fatal("creator response should be unlocked")
	}
	if len(res.Cookies()) == 0 {
		t.Fatal("expected item unlock cookie")
	}

	metadata, err := http.Get(server.URL + "/api/items/" + created.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Body.Close()
	if metadata.StatusCode != http.StatusOK {
		t.Fatalf("metadata status %d", metadata.StatusCode)
	}
	var public PublicItem
	if err := json.NewDecoder(metadata.Body).Decode(&public); err != nil {
		t.Fatal(err)
	}
	if !public.PasswordProtected || public.Unlocked {
		t.Fatalf("metadata lock state = protected:%v unlocked:%v", public.PasswordProtected, public.Unlocked)
	}

	preview, err := http.Get(server.URL + "/api/items/" + created.Item.ID + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Body.Close()
	if preview.StatusCode != http.StatusUnauthorized {
		t.Fatalf("locked preview status %d", preview.StatusCode)
	}

	wrong, err := postJSON(server.URL+"/api/items/"+created.Item.ID+"/unlock", `{"password":"wrong"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Body.Close()
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong unlock status %d", wrong.StatusCode)
	}

	unlock, err := postJSON(server.URL+"/api/items/"+created.Item.ID+"/unlock", `{"password":"open sesame"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock.Body.Close()
	if unlock.StatusCode != http.StatusOK {
		t.Fatalf("unlock status %d", unlock.StatusCode)
	}
	if len(unlock.Cookies()) == 0 {
		t.Fatal("expected unlock cookie")
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/items/"+created.Item.ID+"/preview", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range unlock.Cookies() {
		req.AddCookie(cookie)
	}
	unlockedPreview, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockedPreview.Body.Close()
	if unlockedPreview.StatusCode != http.StatusOK {
		t.Fatalf("unlocked preview status %d", unlockedPreview.StatusCode)
	}
}

func TestBundleCreatePreviewArchiveAndPassword(t *testing.T) {
	app := testApp(t)
	app.cfg.MaxUploadBytes = 4096
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("name", "handoff")
	_ = writer.WriteField("ttl", "1h")
	_ = writer.WriteField("password", "bundle secret")
	_ = writer.WriteField("textName", "note.md")
	_ = writer.WriteField("text", "# Bundle\n\ncopy me")
	writeMultipartFile(t, writer, "files", "photo.png", "image/png", []byte("\x89PNG\r\n\x1a\nimage"))
	writeMultipartFile(t, writer, "files", "readme.txt", "text/plain; charset=utf-8", []byte("plain file"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := http.Post(server.URL+"/api/bundle", writer.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", res.StatusCode)
	}

	var created struct {
		Item PublicItem `json:"item"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Item.Kind != KindBundle {
		t.Fatalf("kind = %q", created.Item.Kind)
	}
	if created.Item.EntryCount != 3 || len(created.Item.Entries) != 3 {
		t.Fatalf("entries = count:%d len:%d", created.Item.EntryCount, len(created.Item.Entries))
	}
	if !created.Item.PasswordProtected || !created.Item.Unlocked {
		t.Fatalf("created lock state = protected:%v unlocked:%v", created.Item.PasswordProtected, created.Item.Unlocked)
	}
	if !strings.HasSuffix(created.Item.DownloadURL, "/archive.zip") {
		t.Fatalf("download url = %q", created.Item.DownloadURL)
	}

	lockedArchive, err := http.Get(server.URL + "/api/items/" + created.Item.ID + "/archive.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer lockedArchive.Body.Close()
	if lockedArchive.StatusCode != http.StatusUnauthorized {
		t.Fatalf("locked archive status %d", lockedArchive.StatusCode)
	}

	unlock, err := postJSON(server.URL+"/api/items/"+created.Item.ID+"/unlock", `{"password":"bundle secret"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock.Body.Close()
	if unlock.StatusCode != http.StatusOK {
		t.Fatalf("unlock status %d", unlock.StatusCode)
	}

	var noteEntry PublicBundleEntry
	for _, entry := range created.Item.Entries {
		if entry.Filename == "note.md" {
			noteEntry = entry
		}
	}
	if noteEntry.ID == "" || noteEntry.PreviewURL == "" {
		t.Fatalf("note entry missing preview: %+v", noteEntry)
	}
	previewReq, err := http.NewRequest(http.MethodGet, server.URL+noteEntry.PreviewURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range unlock.Cookies() {
		previewReq.AddCookie(cookie)
	}
	preview, err := http.DefaultClient.Do(previewReq)
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Body.Close()
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("preview status %d", preview.StatusCode)
	}

	archiveReq, err := http.NewRequest(http.MethodGet, server.URL+"/api/items/"+created.Item.ID+"/archive.zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range unlock.Cookies() {
		archiveReq.AddCookie(cookie)
	}
	archiveRes, err := http.DefaultClient.Do(archiveReq)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveRes.Body.Close()
	if archiveRes.StatusCode != http.StatusOK {
		t.Fatalf("archive status %d", archiveRes.StatusCode)
	}
	archiveBytes, err := io.ReadAll(archiveRes.Body)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
	}
	for _, name := range []string{"note.md", "photo.png", "readme.txt"} {
		if !names[name] {
			t.Fatalf("archive missing %q, got %#v", name, names)
		}
	}
}

func TestAccessPasswordProtectsAPI(t *testing.T) {
	app := testApp(t)
	app.cfg.AccessPassword = "secret"
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	res, err := http.Get(server.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", res.StatusCode)
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginBody := strings.NewReader("password=secret")
	req, err := http.NewRequest(http.MethodPost, server.URL+"/login", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	loginRes, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer loginRes.Body.Close()
	if loginRes.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d", loginRes.StatusCode)
	}
	if len(loginRes.Cookies()) == 0 {
		t.Fatal("expected auth cookie")
	}

	authedReq, err := http.NewRequest(http.MethodGet, server.URL+"/api/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range loginRes.Cookies() {
		authedReq.AddCookie(cookie)
	}
	authedRes, err := http.DefaultClient.Do(authedReq)
	if err != nil {
		t.Fatal(err)
	}
	defer authedRes.Body.Close()
	if authedRes.StatusCode != http.StatusOK {
		t.Fatalf("authed status = %d", authedRes.StatusCode)
	}
}

func TestBasePathRoutesAndGeneratedURLs(t *testing.T) {
	app := testApp(t)
	app.cfg.BasePath = "/qfs"
	app.cfg.PublicBaseURL = "https://mtsak.duckdns.org"
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	body := strings.NewReader(`{"name":"path.md","text":"# Path","ttl":"1h"}`)
	res, err := http.Post(server.URL+"/qfs/api/text", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", res.StatusCode)
	}

	var created struct {
		Item      PublicItem `json:"item"`
		ShareURL  string     `json:"shareUrl"`
		DeleteURL string     `json:"deleteUrl"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ShareURL, "https://mtsak.duckdns.org/qfs/s/") {
		t.Fatalf("share url = %q", created.ShareURL)
	}
	if !strings.HasPrefix(created.DeleteURL, "https://mtsak.duckdns.org/qfs/api/items/") {
		t.Fatalf("delete url = %q", created.DeleteURL)
	}
	if !strings.HasPrefix(created.Item.DownloadURL, "/qfs/api/items/") {
		t.Fatalf("download url = %q", created.Item.DownloadURL)
	}

	page, err := http.Get(server.URL + "/qfs/s/" + created.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("page status %d", page.StatusCode)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(page.Body)
	if !strings.Contains(buf.String(), `href="/qfs/assets/styles.css"`) {
		t.Fatalf("page does not use prefixed assets: %s", buf.String())
	}
}

func TestBasePathRedirectsDuplicatedLeadingSlashes(t *testing.T) {
	app := testApp(t)
	app.cfg.BasePath = "/qfs"
	req := httptest.NewRequest(http.MethodGet, "http://example.com//qfs?x=1", nil)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/qfs/?x=1" {
		t.Fatalf("location = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "http://example.com///qfs/api/config", nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("nested status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/qfs/api/config" {
		t.Fatalf("nested location = %q", got)
	}
}

func writeMultipartFile(t *testing.T, writer *multipart.Writer, fieldName, filename, contentType string, content []byte) {
	t.Helper()
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+fieldName+`"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
}

func postJSON(url, body string, cookies []*http.Cookie) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	return http.DefaultClient.Do(req)
}

func testApp(t *testing.T) *App {
	t.Helper()
	cfg := Config{
		Addr:            "127.0.0.1:0",
		DataDir:         t.TempDir(),
		MaxUploadBytes:  64,
		DefaultTTL:      24 * time.Hour,
		CleanupInterval: time.Minute,
		PreviewBytes:    1024,
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}
