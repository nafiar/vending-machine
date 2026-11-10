package http

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"vending-machine/internal/model"
)

// getCatalog: GET /api/catalog — the catalog JSON as the server sent it.
func (h *Handler) getCatalog(w http.ResponseWriter, r *http.Request) {
	body, err := h.catalog.Catalog(r.Context(), false)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeRaw(w, http.StatusOK, body)
}

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".svg": "image/svg+xml", ".webp": "image/webp",
}

// getUpload: GET /uploads/<dir>/<name>.<png|jpg|jpeg|svg|webp> — a product
// image. Errors carry no body: the page shows its "no image" tile.
func (h *Handler) getUpload(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/uploads/")
	data, err := h.catalog.Image(r.Context(), rel)
	if err != nil {
		apiErr, isAPI := model.AsAPIError(err)
		switch {
		case errors.Is(err, model.ErrNotFound):
			http.NotFound(w, r)
		case model.IsOffline(err):
			w.WriteHeader(http.StatusServiceUnavailable)
		case isAPI:
			w.WriteHeader(apiErr.Status)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", imageTypes[strings.ToLower(filepath.Ext(rel))])
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write(data)
}
