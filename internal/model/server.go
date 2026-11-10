package model

import (
	"errors"
	"fmt"
	"time"
)

// Machine is this machine as the server's catalog names it.
type Machine struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// ForwardRequest is one page call passed through to the server unchanged.
type ForwardRequest struct {
	Body          []byte // nil for a GET
	Authorization string // the operator token, if any
	RequestID     string // sent as X-Request-ID, so a call can be found in both logs
}

// Response is the server's answer, as received.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

// Success reports a 2xx answer.
func (r *Response) Success() bool { return r.Status >= 200 && r.Status <= 299 }

// Connection is what the agent knows about its link to the server.
type Connection struct {
	URL            string
	Online         bool
	LastContact    time.Time // zero before the first answer
	LastError      string
	Problem        string // INVALID_MACHINE_KEY or MACHINE_INACTIVE
	ProblemMessage string
}

// ErrOffline means the server could not be reached: no network, a timeout,
// or the proxy in front of it answering because the server itself is down.
var ErrOffline = errors.New("server unreachable")

type offlineError struct{ cause error }

func (e *offlineError) Error() string        { return "server unreachable: " + e.cause.Error() }
func (e *offlineError) Is(target error) bool { return target == ErrOffline }
func (e *offlineError) Unwrap() error        { return e.cause }

// Offline wraps cause as an ErrOffline.
func Offline(cause error) error { return &offlineError{cause: cause} }

// IsOffline reports whether err means the server could not be reached.
func IsOffline(err error) bool { return errors.Is(err, ErrOffline) }

// APIError is an error answer from the server application: a non-2xx status
// with its JSON body {"code", "message", ...}.
type APIError struct {
	Status  int
	Code    string
	Message string
	Body    []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server answered %d %s: %s", e.Status, e.Code, e.Message)
}

// KeyProblem reports whether the server refuses this machine (wrong key, or
// deactivated in the admin web), so the kiosk can't sell until an admin fixes it.
func (e *APIError) KeyProblem() bool {
	return e.Code == "INVALID_MACHINE_KEY" || e.Code == "MACHINE_INACTIVE"
}

// AsAPIError returns the *APIError inside err.
func AsAPIError(err error) (*APIError, bool) {
	var e *APIError
	ok := errors.As(err, &e)
	return e, ok
}
