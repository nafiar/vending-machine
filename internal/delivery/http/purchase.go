package http

import (
	"context"
	"net/http"

	"vending-machine/internal/model"
)

// createTransaction: POST /api/transactions
// {"id": <uuid>, "slotNumber": <int>, "catalogVersion": <int>} — passed to the
// server unchanged; the server's answer comes back unchanged.
func (h *Handler) createTransaction(w http.ResponseWriter, r *http.Request) {
	h.forward(h.purchase.Create)(w, r)
}

// payTransaction: POST /api/transactions/{id}/pay  (no body)
// Answers {"id", "status": "DISPENSED"|"FAILED", "failureReason"} after the
// coil has turned (or failed).
func (h *Handler) payTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := txID(w, r)
	if !ok {
		return
	}
	out, err := h.purchase.Pay(r.Context(), id, requestID(r))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// cancelTransaction: POST /api/transactions/{id}/cancel — passed through.
func (h *Handler) cancelTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := txID(w, r)
	if !ok {
		return
	}
	h.forward(func(ctx context.Context, req model.ForwardRequest) (*model.Response, error) {
		return h.purchase.Cancel(ctx, id, req)
	})(w, r)
}

// reportTransaction: POST /api/transactions/{id}/report — "Produk tidak keluar?", passed through.
func (h *Handler) reportTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := txID(w, r)
	if !ok {
		return
	}
	h.forward(func(ctx context.Context, req model.ForwardRequest) (*model.Response, error) {
		return h.purchase.Report(ctx, id, req)
	})(w, r)
}

// txID reads {id} from the path; an id that isn't a transaction's is 404.
func txID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := model.NormalizeTxID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Transaksi tidak ditemukan.")
	}
	return id, ok
}
