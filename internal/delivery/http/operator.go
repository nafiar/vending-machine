package http

import "net/http"

// The operator routes pass straight through: the page's body and operator
// token go to the server unchanged, and its answer comes back unchanged.

// operatorLogin: POST /api/operator/login {"username", "password"} (never logged).
func (h *Handler) operatorLogin(w http.ResponseWriter, r *http.Request) {
	h.forward(h.operator.Login)(w, r)
}

// operatorLogout: POST /api/operator/logout, Authorization: Bearer <token>.
func (h *Handler) operatorLogout(w http.ResponseWriter, r *http.Request) {
	h.forward(h.operator.Logout)(w, r)
}

// operatorSlots: GET /api/operator/slots, Authorization: Bearer <token>.
func (h *Handler) operatorSlots(w http.ResponseWriter, r *http.Request) {
	h.forward(h.operator.Slots)(w, r)
}

// createRestock: POST /api/restock-sessions {"id": <uuid>, "items": [...]},
// Authorization: Bearer <token>. Earlier sales are reported first.
func (h *Handler) createRestock(w http.ResponseWriter, r *http.Request) {
	h.forward(h.operator.Restock)(w, r)
}
