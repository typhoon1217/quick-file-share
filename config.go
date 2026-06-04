package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr            string
	DataDir         string
	PublicBaseURL   string
	BasePath        string
	AccessPassword  string
	MaxUploadBytes  int64
	DefaultTTL      time.Duration
	CleanupInterval time.Duration
	PreviewBytes    int64
}

func LoadConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("quick-file-share", flag.ContinueOnError)

	var cfg Config
	var maxUpload string
	var defaultTTL string
	var cleanupInterval string
	var previewBytes string

	fs.StringVar(&cfg.Addr, "addr", getenv("QFS_ADDR", "0.0.0.0:8080"), "HTTP bind address")
	fs.StringVar(&cfg.DataDir, "data-dir", getenv("QFS_DATA_DIR", "./data"), "directory for uploaded files and metadata")
	fs.StringVar(&cfg.PublicBaseURL, "public-base-url", getenv("QFS_PUBLIC_BASE_URL", ""), "external base URL used for generated links and QR codes")
	fs.StringVar(&cfg.BasePath, "base-path", getenv("QFS_BASE_PATH", ""), "optional URL path prefix, e.g. /qfs")
	fs.StringVar(&cfg.AccessPassword, "access-password", getenv("QFS_ACCESS_PASSWORD", ""), "optional site-wide access password")
	fs.StringVar(&maxUpload, "max-upload-size", getenv("QFS_MAX_UPLOAD_SIZE", "5GB"), "maximum upload size, e.g. 512MB, 5GB")
	fs.StringVar(&defaultTTL, "default-ttl", getenv("QFS_DEFAULT_TTL", "24h"), "default expiry, e.g. 24h, 3d")
	fs.StringVar(&cleanupInterval, "cleanup-interval", getenv("QFS_CLEANUP_INTERVAL", "10m"), "expired-file cleanup interval")
	fs.StringVar(&previewBytes, "preview-size", getenv("QFS_PREVIEW_SIZE", "4MB"), "maximum bytes returned by text/markdown preview")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	var err error
	cfg.MaxUploadBytes, err = parseHumanBytes(maxUpload)
	if err != nil {
		return Config{}, fmt.Errorf("invalid max upload size: %w", err)
	}
	cfg.DefaultTTL, err = parseDurationWithDays(defaultTTL)
	if err != nil {
		return Config{}, fmt.Errorf("invalid default ttl: %w", err)
	}
	cfg.CleanupInterval, err = parseDurationWithDays(cleanupInterval)
	if err != nil {
		return Config{}, fmt.Errorf("invalid cleanup interval: %w", err)
	}
	cfg.PreviewBytes, err = parseHumanBytes(previewBytes)
	if err != nil {
		return Config{}, fmt.Errorf("invalid preview size: %w", err)
	}
	if cfg.MaxUploadBytes <= 0 {
		return Config{}, errors.New("max upload size must be greater than zero")
	}
	if cfg.DefaultTTL <= 0 {
		return Config{}, errors.New("default ttl must be greater than zero")
	}
	if cfg.CleanupInterval <= 0 {
		return Config{}, errors.New("cleanup interval must be greater than zero")
	}
	if cfg.PreviewBytes <= 0 {
		return Config{}, errors.New("preview size must be greater than zero")
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")
	cfg.BasePath, err = normalizeBasePath(cfg.BasePath)
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func normalizeBasePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || value == "/" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	value = strings.TrimRight(value, "/")
	if strings.ContainsAny(value, "?#") || strings.Contains(value, "//") {
		return "", fmt.Errorf("invalid base path %q", raw)
	}
	return value, nil
}

func joinBasePath(basePath, path string) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return basePath + path
}

func parseDurationWithDays(raw string) (time.Duration, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return 0, errors.New("empty duration")
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(value)
}

func parseHumanBytes(raw string) (int64, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return 0, errors.New("empty size")
	}

	multipliers := []struct {
		suffix string
		value  float64
	}{
		{"tib", 1024 * 1024 * 1024 * 1024},
		{"tb", 1024 * 1024 * 1024 * 1024},
		{"gib", 1024 * 1024 * 1024},
		{"gb", 1024 * 1024 * 1024},
		{"mib", 1024 * 1024},
		{"mb", 1024 * 1024},
		{"kib", 1024},
		{"kb", 1024},
		{"t", 1024 * 1024 * 1024 * 1024},
		{"g", 1024 * 1024 * 1024},
		{"m", 1024 * 1024},
		{"k", 1024},
		{"b", 1},
	}

	for _, multiplier := range multipliers {
		if strings.HasSuffix(value, multiplier.suffix) {
			number := strings.TrimSpace(strings.TrimSuffix(value, multiplier.suffix))
			parsed, err := strconv.ParseFloat(number, 64)
			if err != nil {
				return 0, err
			}
			return int64(parsed * multiplier.value), nil
		}
	}

	return strconv.ParseInt(value, 10, 64)
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatDurationLabel(duration time.Duration) string {
	switch {
	case duration%(24*time.Hour) == 0:
		days := int(duration / (24 * time.Hour))
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case duration%time.Hour == 0:
		hours := int(duration / time.Hour)
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	case duration%time.Minute == 0:
		minutes := int(duration / time.Minute)
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	default:
		return duration.String()
	}
}

func durationFormValue(duration time.Duration) string {
	if duration == 24*time.Hour {
		return "24h"
	}
	if duration%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(duration/(24*time.Hour)))
	}
	return duration.String()
}
