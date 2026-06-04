package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

	item, err := app.store.Create(KindText, "old.txt", "text/plain; charset=utf-8", now.Add(time.Hour), strings.NewReader("old"))
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
