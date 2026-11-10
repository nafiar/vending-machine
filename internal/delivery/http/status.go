package http

import (
	"encoding/json"
	"io"
	"net/http"

	"vending-machine/internal/model"
)

// getHealth: GET /api/health — the page's live connection check.
// Answers {"ok": true, "catalogVersion", "board", "pending"}.
func (h *Handler) getHealth(w http.ResponseWriter, r *http.Request) {
	hl, err := h.status.Health(r.Context())
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "catalogVersion": hl.CatalogVersion, "board": hl.Board, "pending": hl.Pending})
}

// getStatus: GET /api/agent/status — what the developer page shows.
func (h *Handler) getStatus(w http.ResponseWriter, r *http.Request) {
	st := h.status.Status()
	srv := map[string]any{
		"url":       st.Server.URL,
		"online":    st.Server.Online,
		"lastError": st.Server.LastError,
		"problem":   st.Server.Problem,
		"message":   st.Server.ProblemMessage,
	}
	if !st.Server.LastContact.IsZero() {
		srv["lastContactAt"] = st.Server.LastContact.UnixMilli()
	}
	board := map[string]any{"kind": st.Board.Kind, "name": st.Board.Name, "slotMap": st.Board.SlotMap}
	if st.Board.Simulated {
		board["simMode"] = st.Board.SimMode
		board["simModes"] = model.SimModes
		board["crashArmed"] = st.Board.CrashArmed
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        st.Version,
		"startedAt":      st.StartedAt.UnixMilli(),
		"listen":         st.Listen,
		"configFile":     st.ConfigFile,
		"dataDir":        st.DataDir,
		"server":         srv,
		"machine":        st.Machine,
		"catalogVersion": st.CatalogVersion,
		"board":          board,
		"journal":        st.Journal,
		"recent":         st.Recent,
	})
}

// setSim: POST /api/agent/sim {"mode": "ok" | "jam" | "no_ack" | "crash"}
// Answers {"simMode", "crashArmed"}.
func (h *Handler) setSim(w http.ResponseWriter, r *http.Request) {
	if !h.status.Status().Board.Simulated {
		h.writeErr(w, model.ErrNotSimulator)
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", `Send {"mode": "ok"}, "jam", "no_ack" or "crash".`)
		return
	}
	st, err := h.status.SetSimMode(req.Mode)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"simMode": st.Mode, "crashArmed": st.CrashArmed})
}
