// Package server is the port to vending-server's machine API (/api/machine/...),
// authenticated with the machine key. The HTTP client lives in server/http.
//
// Every server path the agent uses is named here, so the repository layer is
// the only place that knows the server's URLs.
package server

import (
	"context"

	"vending-machine/internal/model"
)

// Methods for Forward, so callers need not import net/http.
const (
	MethodGet  = "GET"
	MethodPost = "POST"
)

// Server paths for the calls the agent passes through unchanged (Forward).
const (
	PathTransactions   = "/api/machine/transactions"
	PathOperatorLogin  = "/api/machine/operator/login"
	PathOperatorLogout = "/api/machine/operator/logout"
	PathOperatorSlots  = "/api/machine/operator/slots"
	PathRestock        = "/api/machine/restock-sessions"
)

// TransactionPath is /api/machine/transactions/<id>[/<action>].
func TransactionPath(id, action string) string {
	p := PathTransactions + "/" + id
	if action != "" {
		p += "/" + action
	}
	return p
}

// Repository is the server. Errors are model.ErrOffline (unreachable) or a
// *model.APIError (the server answered with an error).
type Repository interface {
	BaseURL() string

	// Heartbeat records this machine's contact time and returns the server's
	// catalog version (FR-28).
	Heartbeat(ctx context.Context) (catalogVersion int64, err error)
	// Catalog returns the catalog JSON as the server sent it.
	Catalog(ctx context.Context) ([]byte, error)
	Transaction(ctx context.Context, txID string) (model.TxState, error)
	// Pay asks for PENDING -> PAID. A repeat returns the current state.
	Pay(ctx context.Context, txID, requestID string) (model.TxState, error)
	Cancel(ctx context.Context, txID, reason string) error
	// Result reports a final DISPENSED or FAILED (reason only for FAILED).
	Result(ctx context.Context, txID, status, reason string) error
	// Image returns a product image from /uploads/<rel>. A non-200 answer
	// is a *model.APIError carrying only the status.
	Image(ctx context.Context, rel string) ([]byte, error)

	// Forward passes a page call to path unchanged and returns the server's
	// answer unchanged, error statuses included. Only an unreachable server
	// is an error.
	Forward(ctx context.Context, method, path string, req model.ForwardRequest) (*model.Response, error)
}
