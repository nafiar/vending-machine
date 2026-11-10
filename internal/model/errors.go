package model

import (
	"errors"
	"strings"
)

// Business errors. The delivery layer maps each to an HTTP status and the
// page's error code, so nothing matches on error strings. Server errors are
// ErrOffline and *APIError (server.go).
var (
	ErrNotFound          = errors.New("not found")
	ErrTransactionClosed = errors.New("transaction closed")
	ErrJournal           = errors.New("the journal could not be written")
	ErrNotSimulator      = errors.New("the board is not the simulator")
	ErrInvalidSimMode    = errors.New("simulator mode")
)

// TransactionClosedError is a purchase that can no longer be paid. Status is
// the server's state, or "" when the agent closed it itself.
type TransactionClosedError struct{ Status string }

func (e *TransactionClosedError) Error() string {
	if e.Status == "" {
		return "transaction closed"
	}
	return "transaction already " + strings.ToLower(e.Status)
}

func (e *TransactionClosedError) Is(target error) bool { return target == ErrTransactionClosed }
