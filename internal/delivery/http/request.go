package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"vending-machine/internal/model"
)

// forwardFunc is a usecase call that passes a page call through to the server.
type forwardFunc func(ctx context.Context, req model.ForwardRequest) (*model.Response, error)

// forward reads the page's call (body and operator token), hands it to call,
// and writes the server's answer unchanged. An unreachable server and a
// refused machine key become 503 OFFLINE, which the page shows as "Mesin
// offline" (FR-10).
func (h *Handler) forward(call forwardFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := model.ForwardRequest{Authorization: r.Header.Get("Authorization"), RequestID: requestID(r)}
		if r.Method != http.MethodGet {
			b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Permintaan tidak terbaca.")
				return
			}
			req.Body = b
		}
		resp, err := call(r.Context(), req)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		if resp.ContentType != "" {
			w.Header().Set("Content-Type", resp.ContentType)
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	}
}

type ctxKey int

const requestIDKey ctxKey = 1

// requestID is sent to the server as X-Request-ID, so a kiosk call can be
// found in both logs.
func requestID(r *http.Request) string {
	v, _ := r.Context().Value(requestIDKey).(string)
	return v
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "vm-" + hex.EncodeToString(b[:])
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// middleware adds a request id, one log line per request and panic recovery.
// The page's polling and static files log at debug level only.
func (h *Handler) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := newRequestID()
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, rid))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		sw.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if p := recover(); p != nil {
				h.log.Error("panic", "err", p, "path", r.URL.Path, "stack", string(debug.Stack()))
				if !sw.wrote {
					writeError(sw, http.StatusInternalServerError, "AGENT_ERROR", "Terjadi kesalahan di mesin. Coba lagi.")
				}
			}
			level := slog.LevelDebug
			if strings.HasPrefix(r.URL.Path, "/api/") && !quiet(r) {
				level = slog.LevelInfo
			}
			h.log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds(), "request_id", rid)
		}()
		next.ServeHTTP(sw, r)
	})
}

func quiet(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/catalog", "/api/health", "/api/agent/status":
		return true
	}
	return false
}
