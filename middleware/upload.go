package middleware

import (
	"io"
	"net/http"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// UploadConfig controls file upload limits
type UploadConfig struct {
	MaxFileSize      int64  // Maximum file size in bytes
	MaxMemory        int64  // Maximum memory to use before temp file
	AllowedMimeTypes []string // Allowed MIME types (empty = allow all)
}

// DefaultUploadConfig returns default upload configuration
func DefaultUploadConfig() UploadConfig {
	return UploadConfig{
		MaxFileSize: 100 * 1024 * 1024, // 100 MB
		MaxMemory:   10 * 1024 * 1024,  // 10 MB
	}
}

// MaxUploadSize returns middleware that limits request body size
func MaxUploadSize(maxSize int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxSize)
			next.ServeHTTP(w, r)
		})
	}
}

// ValidateMultipartUpload validates multipart file uploads
func ValidateMultipartUpload(cfg UploadConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only apply to multipart/form-data requests
			if r.Header.Get("Content-Type") == "" ||
				(r.Method != http.MethodPost && r.Method != http.MethodPut) {
				next.ServeHTTP(w, r)
				return
			}

			// Parse multipart form with size limits
			if err := r.ParseMultipartForm(cfg.MaxMemory); err != nil {
				dxerrors.WriteError(w, dxerrors.NewValidation("failed to parse form", err.Error()))
				return
			}

			// Validate individual files
			if r.MultipartForm != nil && r.MultipartForm.File != nil {
				for fieldName, files := range r.MultipartForm.File {
					for _, fileHeader := range files {
						// Check file size
						if fileHeader.Size > cfg.MaxFileSize {
							dxerrors.WriteError(w, dxerrors.NewValidation(
								"file too large: "+fileHeader.Filename,
								"maximum size is "+formatBytes(cfg.MaxFileSize),
							))
							return
						}

						// Check MIME type if restricted
						if len(cfg.AllowedMimeTypes) > 0 {
							file, err := fileHeader.Open()
							if err != nil {
								dxerrors.WriteError(w, dxerrors.NewInternal(
									"failed to read file: "+fileHeader.Filename,
									err.Error(),
								))
								return
							}

							// Read magic bytes to determine actual MIME type
							buffer := make([]byte, 512)
							_, err = file.Read(buffer)
							file.Close()

							if err != nil && err != io.EOF {
								dxerrors.WriteError(w, dxerrors.NewInternal(
									"failed to read file: "+fileHeader.Filename,
									err.Error(),
								))
								return
							}

							mimeType := http.DetectContentType(buffer)
							allowed := false
							for _, allowed_type := range cfg.AllowedMimeTypes {
								if mimeType == allowed_type {
									allowed = true
									break
								}
							}

							if !allowed {
								dxerrors.WriteError(w, dxerrors.NewValidation(
									"invalid file type for "+fieldName,
									"file type "+mimeType+" not allowed",
								))
								return
							}
						}
					}
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// formatBytes formats a byte size in human-readable form
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes < KB:
		return "bytes"
	case bytes < MB:
		return string(rune(bytes/KB)) + " KB"
	case bytes < GB:
		return string(rune(bytes/MB)) + " MB"
	default:
		return string(rune(bytes/GB)) + " GB"
	}
}
