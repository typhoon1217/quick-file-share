package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("item not found")
	ErrExpired  = errors.New("item expired")
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)
)

type Store struct {
	dataDir string
	now     func() time.Time
}

func NewStore(dataDir string, now func() time.Time) *Store {
	return &Store{dataDir: dataDir, now: now}
}

func (s *Store) Init() error {
	return os.MkdirAll(s.itemsDir(), 0o755)
}

func (s *Store) Create(kind, filename, contentType string, expiresAt time.Time, src io.Reader) (Item, error) {
	for attempts := 0; attempts < 5; attempts++ {
		id, err := randomToken(12)
		if err != nil {
			return Item{}, err
		}
		deleteToken, err := randomToken(24)
		if err != nil {
			return Item{}, err
		}

		dir := s.itemDir(id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return Item{}, err
		}

		item := Item{
			ID:          id,
			Kind:        kind,
			Filename:    cleanFilename(filename),
			ContentType: strings.TrimSpace(contentType),
			CreatedAt:   s.now().UTC(),
			ExpiresAt:   expiresAt.UTC(),
			DeleteToken: deleteToken,
		}
		if item.Filename == "" {
			item.Filename = "download"
		}
		if item.ContentType == "" {
			item.ContentType = "application/octet-stream"
		}

		size, err := writeFileAtomic(s.contentPath(id), src)
		if err != nil {
			_ = os.RemoveAll(dir)
			return Item{}, err
		}
		item.Size = size

		if err := s.saveMeta(item); err != nil {
			_ = os.RemoveAll(dir)
			return Item{}, err
		}
		return item, nil
	}

	return Item{}, errors.New("could not allocate item id")
}

func (s *Store) Get(id string) (Item, error) {
	if !validID(id) {
		return Item{}, ErrNotFound
	}
	data, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Item{}, ErrNotFound
		}
		return Item{}, err
	}

	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		return Item{}, err
	}
	if item.ID == "" {
		item.ID = id
	}
	if item.Expired(s.now()) {
		_ = s.Delete(id)
		return Item{}, ErrExpired
	}
	return item, nil
}

func (s *Store) Delete(id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	if err := os.RemoveAll(s.itemDir(id)); err != nil {
		return err
	}
	return nil
}

func (s *Store) CleanupExpired() (int, error) {
	entries, err := os.ReadDir(s.itemsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		item, err := s.getWithoutExpiryCheck(entry.Name())
		if err != nil {
			continue
		}
		if item.Expired(s.now()) {
			if err := s.Delete(entry.Name()); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

func (s *Store) ContentPath(id string) string {
	return s.contentPath(id)
}

func (s *Store) saveMeta(item Item) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	path := s.metaPath(item.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) getWithoutExpiryCheck(id string) (Item, error) {
	if !validID(id) {
		return Item{}, ErrNotFound
	}
	data, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Item{}, ErrNotFound
		}
		return Item{}, err
	}
	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (s *Store) itemsDir() string {
	return filepath.Join(s.dataDir, "items")
}

func (s *Store) itemDir(id string) string {
	return filepath.Join(s.itemsDir(), id)
}

func (s *Store) contentPath(id string) string {
	return filepath.Join(s.itemDir(id), "content")
}

func (s *Store) metaPath(id string) string {
	return filepath.Join(s.itemDir(id), "meta.json")
}

func validID(id string) bool {
	return idPattern.MatchString(id)
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func cleanFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(strings.ReplaceAll(name, "\x00", ""))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

func writeFileAtomic(path string, src io.Reader) (int64, error) {
	tmp := path + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	size, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return 0, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return 0, closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("commit file: %w", err)
	}
	return size, nil
}
