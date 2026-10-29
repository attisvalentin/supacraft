package handler

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/storage"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"github.com/go-chi/chi/v5"
)

var folderRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+(/[a-zA-Z0-9_-]+)*$`)

func HandleFileUpload(pool *tenant.PoolManager, serverCfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.APISettings.FileEndpoints.Upload.IsEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		if cfg.APISettings.FileEndpoints.Upload.AuthRequired() {
			if _, err := validateJWT(r, cfg, pool); err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}

		if cfg.APISettings.Storage == nil || cfg.APISettings.Storage.Driver == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file storage not configured"})
			return
		}

		// Determine effective max file size
		maxSize := serverCfg.MaxFileSize
		if cfg.APISettings.Storage.MaxFileSize > 0 && cfg.APISettings.Storage.MaxFileSize < maxSize {
			maxSize = cfg.APISettings.Storage.MaxFileSize
		}

		// Limit request body size
		r.Body = http.MaxBytesReader(w, r.Body, maxSize)

		if err := r.ParseMultipartForm(maxSize); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too large or invalid multipart form"})
			return
		}
		defer r.MultipartForm.RemoveAll()

		file, header, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
			return
		}
		defer file.Close()

		folder := strings.TrimSpace(r.FormValue("folder"))
		if folder != "" && !folderRegex.MatchString(folder) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid folder name"})
			return
		}

		// Detect MIME type from content
		buf := make([]byte, 512)
		n, err := io.ReadAtLeast(file, buf, 1)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read file"})
			return
		}
		buf = buf[:n]
		detectedMIME := http.DetectContentType(buf)

		// Validate MIME against allowlist
		if len(cfg.APISettings.Storage.AllowedMimes) > 0 {
			allowed := false
			for _, m := range cfg.APISettings.Storage.AllowedMimes {
				if m == detectedMIME {
					allowed = true
					break
				}
			}
			if !allowed {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("file type %s not allowed", detectedMIME)})
				return
			}
		}

		// Generate UUID filename
		ext := extensionFromMIME(detectedMIME, header.Filename)
		uuid := generateUUID()
		filename := uuid + ext

		var storagePath string
		if folder != "" {
			storagePath = folder + "/" + filename
		} else {
			storagePath = filename
		}

		// Get tenant ID for local driver base path
		tenantID := r.Header.Get("X-Tenant-ID")
		localBase := filepath.Join(serverCfg.StoragePath, tenantID)

		driver, err := storage.NewDriver(cfg.APISettings.Storage.Driver, cfg.APISettings.Storage.Config, localBase)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("storage driver init failed: %v", err))
			return
		}

		// Reconstruct reader: 512-byte buffer + rest of file
		combinedReader := io.MultiReader(bytes.NewReader(buf), file)

		if err := driver.Put(r.Context(), storagePath, combinedReader, header.Size); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("file upload failed: %v", err))
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{
				"path": storagePath,
				"size": header.Size,
				"mime": detectedMIME,
			},
		})
	}
}

func HandleFileDownload(pool *tenant.PoolManager, serverCfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.APISettings.FileEndpoints.Download.IsEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		if cfg.APISettings.FileEndpoints.Download.AuthRequired() {
			if _, err := validateJWT(r, cfg, pool); err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}

		if cfg.APISettings.Storage == nil || cfg.APISettings.Storage.Driver == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file storage not configured"})
			return
		}

		filePath := chi.URLParam(r, "*")
		if err := validateFilePath(filePath); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		tenantID := r.Header.Get("X-Tenant-ID")
		localBase := filepath.Join(serverCfg.StoragePath, tenantID)

		driver, err := storage.NewDriver(cfg.APISettings.Storage.Driver, cfg.APISettings.Storage.Config, localBase)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("storage driver init failed: %v", err))
			return
		}

		reader, err := driver.Get(r.Context(), filePath)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		defer reader.Close()

		// Detect content type from first 512 bytes
		buf := make([]byte, 512)
		n, err := io.ReadAtLeast(reader, buf, 1)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, "failed to read file")
			return
		}
		buf = buf[:n]
		contentType := http.DetectContentType(buf)

		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)

		// Stream: first the buffered bytes, then the rest
		io.Copy(w, io.MultiReader(bytes.NewReader(buf), reader))
	}
}

func HandleFileDelete(pool *tenant.PoolManager, serverCfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.APISettings.FileEndpoints.Delete.IsEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		if cfg.APISettings.FileEndpoints.Delete.AuthRequired() {
			if _, err := validateJWT(r, cfg, pool); err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}

		if cfg.APISettings.Storage == nil || cfg.APISettings.Storage.Driver == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file storage not configured"})
			return
		}

		filePath := chi.URLParam(r, "*")
		if err := validateFilePath(filePath); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		tenantID := r.Header.Get("X-Tenant-ID")
		localBase := filepath.Join(serverCfg.StoragePath, tenantID)

		driver, err := storage.NewDriver(cfg.APISettings.Storage.Driver, cfg.APISettings.Storage.Config, localBase)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("storage driver init failed: %v", err))
			return
		}

		exists, err := driver.Exists(r.Context(), filePath)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("storage check failed: %v", err))
			return
		}
		if !exists {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}

		if err := driver.Delete(r.Context(), filePath); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("file delete failed: %v", err))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"deleted": true},
		})
	}
}

func validateFilePath(p string) error {
	if p == "" {
		return fmt.Errorf("file path is required")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("invalid file path")
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("invalid file path")
	}
	return nil
}

func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x%x%x%x%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// extensionFromMIME returns a file extension for the detected MIME type.
// Falls back to the original filename's extension if MIME lookup fails.
func extensionFromMIME(mimeType, originalFilename string) string {
	// Common MIME → extension map (stdlib sometimes returns less common variants)
	commonExts := map[string]string{
		"image/jpeg":             ".jpg",
		"image/png":              ".png",
		"image/gif":              ".gif",
		"image/webp":             ".webp",
		"image/svg+xml":          ".svg",
		"application/pdf":        ".pdf",
		"text/plain":             ".txt",
		"text/html":              ".html",
		"text/css":               ".css",
		"text/csv":               ".csv",
		"application/json":       ".json",
		"application/xml":        ".xml",
		"application/zip":        ".zip",
		"application/gzip":       ".gz",
		"video/mp4":              ".mp4",
		"audio/mpeg":             ".mp3",
		"application/javascript": ".js",
	}

	if ext, ok := commonExts[mimeType]; ok {
		return ext
	}

	// Try stdlib
	exts, err := mime.ExtensionsByType(mimeType)
	if err == nil && len(exts) > 0 {
		return exts[0]
	}

	// Fallback: use original filename extension
	if ext := filepath.Ext(originalFilename); ext != "" {
		return ext
	}

	return ".bin"
}
