// Package connection keeps track of whether the server is reachable and
// accepts this machine (FR-10: the kiosk shows "Mesin offline" otherwise),
// and passes the page's calls through to the server. Every other usecase
// reports what it saw of the server here.
package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/server"
)

// UseCase is the port other usecases depend on.
type UseCase interface {
	// Online records an answer from the server.
	Online()
	// Offline records that the server could not be reached.
	Offline(err error)
	// NoteAPIError records an error answer: the server is reachable, but it
	// may be refusing this machine.
	NoteAPIError(e *model.APIError)
	// IsOnline reports whether the server was last seen reachable and accepting.
	IsOnline() bool
	Snapshot() model.Connection

	// Forward passes one page call to the server and returns its answer
	// unchanged, error statuses included. The error is ErrOffline when the
	// server is unreachable, or the *model.APIError when it refuses this
	// machine's key.
	Forward(ctx context.Context, method, path string, req model.ForwardRequest) (*model.Response, error)
}

// Service is the singleton implementation.
type Service struct {
	server server.Repository
	log    *slog.Logger
	now    func() time.Time
	call   time.Duration // timeout of a forwarded call

	mu          sync.Mutex
	online      bool
	lastContact time.Time
	lastError   string
	problem     string
	problemMsg  string
}

var _ UseCase = (*Service)(nil)

// DefaultCallTimeout bounds one forwarded call.
const DefaultCallTimeout = 6 * time.Second

// New wires the dependencies. call is the forwarded-call timeout (0: the default).
func New(s server.Repository, log *slog.Logger, call time.Duration) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if call == 0 {
		call = DefaultCallTimeout
	}
	return &Service{server: s, log: log, now: time.Now, call: call}
}

func (s *Service) Online() {
	s.mu.Lock()
	was, hadProblem := s.online, s.problem
	s.online, s.lastContact, s.lastError, s.problem, s.problemMsg = true, s.now(), "", "", ""
	s.mu.Unlock()
	if !was || hadProblem != "" {
		s.log.Info("server reachable", "server", s.server.BaseURL())
	}
}

func (s *Service) Offline(err error) {
	s.mu.Lock()
	was := s.online
	s.online = false
	if err != nil {
		s.lastError = err.Error()
	}
	s.mu.Unlock()
	if was {
		s.log.Warn("server unreachable; the kiosk shows \"Mesin offline\"", "server", s.server.BaseURL(), "err", err)
	}
}

func (s *Service) NoteAPIError(e *model.APIError) {
	if !e.KeyProblem() {
		s.Online()
		return
	}
	s.mu.Lock()
	changed := s.problem != e.Code
	s.online, s.lastContact, s.problem, s.problemMsg = false, s.now(), e.Code, e.Message
	s.mu.Unlock()
	if changed {
		hint := "check machineKey in config.json"
		if e.Code == "MACHINE_INACTIVE" {
			hint = "activate the machine in the admin web"
		}
		s.log.Error("the server refuses this machine: "+hint, "code", e.Code, "message", e.Message)
	}
}

func (s *Service) IsOnline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online && s.problem == ""
}

func (s *Service) Snapshot() model.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return model.Connection{
		URL:            s.server.BaseURL(),
		Online:         s.online && s.problem == "",
		LastContact:    s.lastContact,
		LastError:      s.lastError,
		Problem:        s.problem,
		ProblemMessage: s.problemMsg,
	}
}

func (s *Service) Forward(ctx context.Context, method, path string, req model.ForwardRequest) (*model.Response, error) {
	cctx, cancel := context.WithTimeout(ctx, s.call)
	defer cancel()
	resp, err := s.server.Forward(cctx, method, path, req)
	if err != nil {
		s.Offline(err)
		return nil, err
	}
	if !resp.Success() {
		if apiErr := APIErrorOf(resp); apiErr.KeyProblem() {
			s.NoteAPIError(apiErr)
			return nil, apiErr
		}
	}
	s.Online()
	return resp, nil
}

// APIErrorOf reads the {"code", "message"} of an error answer.
func APIErrorOf(resp *model.Response) *model.APIError {
	e := &model.APIError{Status: resp.Status, Body: resp.Body}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(resp.Body, &body) == nil {
		e.Code, e.Message = body.Code, body.Message
	}
	if e.Code == "" {
		e.Code = fmt.Sprintf("HTTP_%d", resp.Status)
	}
	return e
}
