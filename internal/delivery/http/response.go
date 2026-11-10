package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"vending-machine/internal/model"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AGENT_ERROR", err.Error())
		return
	}
	writeRaw(w, status, b)
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError writes the page's error shape: {"code", "message"}.
func writeError(w http.ResponseWriter, status int, code, message string) {
	b, _ := json.Marshal(map[string]string{"code": code, "message": message})
	writeRaw(w, status, b)
}

// writeErr maps a usecase error to the page's answer. This is the only place
// errors become HTTP statuses.
func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	var closed *model.TransactionClosedError
	apiErr, isAPI := model.AsAPIError(err)
	switch {
	case isAPI && apiErr.KeyProblem():
		writeOffline(w, apiErr.Code, apiErr.Message)
	case isAPI:
		// A server refusal goes to the page as it came.
		body := apiErr.Body
		if len(body) == 0 {
			body, _ = json.Marshal(map[string]string{"code": apiErr.Code, "message": apiErr.Message})
		}
		writeRaw(w, apiErr.Status, body)
	case model.IsOffline(err):
		writeOffline(w, "", "")
	case errors.As(err, &closed):
		msg := "Transaksi ini sudah ditutup. Silakan ulangi pembelian."
		if closed.Status != "" {
			msg = "Transaksi sudah " + strings.ToLower(closed.Status) + "."
		}
		writeError(w, http.StatusConflict, "TRANSACTION_CLOSED", msg)
	case errors.Is(err, model.ErrJournal):
		writeError(w, http.StatusInternalServerError, "AGENT_ERROR", "Mesin tidak dapat mencatat transaksi. Hubungi petugas.")
	case errors.Is(err, model.ErrNotSimulator):
		writeError(w, http.StatusNotFound, "NOT_SIMULATOR", `The board is the real one ("board": "serial" in config.json).`)
	case errors.Is(err, model.ErrInvalidSimMode):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_INPUT", err.Error())
	case errors.Is(err, model.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Tidak ditemukan.")
	default:
		writeError(w, http.StatusBadGateway, "AGENT_ERROR", err.Error())
	}
}

// writeOffline is 503 OFFLINE, optionally with the reason the server refuses
// this machine and its message.
func writeOffline(w http.ResponseWriter, reason, message string) {
	body := map[string]string{"code": "OFFLINE", "message": "Server tidak terjangkau."}
	if reason != "" {
		body["reason"] = reason
	}
	if message != "" {
		body["message"] = message
	}
	b, _ := json.Marshal(body)
	writeRaw(w, http.StatusServiceUnavailable, b)
}
