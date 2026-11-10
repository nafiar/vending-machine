// Package http is the agent's web surface: the kiosk page, its API on
// localhost, and the developer page. Handlers are thin: decode, call a
// usecase, encode. Business rules live in internal/usecase.
package http

import (
	"io/fs"
	"log/slog"
	"net/http"

	"vending-machine/internal/usecase/catalog"
	"vending-machine/internal/usecase/operator"
	"vending-machine/internal/usecase/purchase"
	"vending-machine/internal/usecase/status"
)

// Deps are the usecases and files the handler serves.
type Deps struct {
	Purchase purchase.UseCase
	Catalog  catalog.UseCase
	Operator operator.UseCase
	Status   status.UseCase
	Web      fs.FS // web/ with kiosk/ and dev/
	Log      *slog.Logger
}

// Handler holds usecase interfaces only, so a test can pass fakes.
type Handler struct {
	purchase purchase.UseCase
	catalog  catalog.UseCase
	operator operator.UseCase
	status   status.UseCase
	web      fs.FS
	log      *slog.Logger
}

// New builds the handler. Called once in cmd/main.
func New(d Deps) *Handler {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Handler{purchase: d.Purchase, catalog: d.Catalog, operator: d.Operator, status: d.Status, web: d.Web, log: d.Log}
}

// Routes maps every HTTP route to its handler. This is the one place to read
// which requests the agent accepts.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// catalog.go
	mux.HandleFunc("GET /api/catalog", h.getCatalog)
	mux.HandleFunc("GET /uploads/", h.getUpload)

	// purchase.go
	mux.HandleFunc("POST /api/transactions", h.createTransaction)
	mux.HandleFunc("POST /api/transactions/{id}/pay", h.payTransaction)
	mux.HandleFunc("POST /api/transactions/{id}/cancel", h.cancelTransaction)
	mux.HandleFunc("POST /api/transactions/{id}/report", h.reportTransaction)

	// operator.go
	mux.HandleFunc("POST /api/operator/login", h.operatorLogin)
	mux.HandleFunc("POST /api/operator/logout", h.operatorLogout)
	mux.HandleFunc("GET /api/operator/slots", h.operatorSlots)
	mux.HandleFunc("POST /api/restock-sessions", h.createRestock)

	// status.go
	mux.HandleFunc("GET /api/health", h.getHealth)
	mux.HandleFunc("GET /api/agent/status", h.getStatus)
	mux.HandleFunc("POST /api/agent/sim", h.setSim)

	mux.HandleFunc("GET /api/", h.apiNotFound)
	mux.HandleFunc("POST /api/", h.apiNotFound)
	mux.Handle("GET /dev/", http.StripPrefix("/dev", h.static("dev")))
	mux.Handle("GET /", h.static("kiosk"))
	return h.middleware(mux)
}

func (h *Handler) apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "NOT_FOUND", "Alamat API tidak ditemukan: "+r.Method+" "+r.URL.Path)
}

// static serves one folder of web/. Files change with each release, so the
// browser always revalidates them.
func (h *Handler) static(dir string) http.Handler {
	sub, err := fs.Sub(h.web, dir)
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
