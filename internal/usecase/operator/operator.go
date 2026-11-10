// Package operator owns the operator's visit: login (FR-12, FR-13), the slot
// list, and the restock that follows (FR-15). The server decides everything;
// the agent passes the calls through and keeps the kiosk's catalog current.
package operator

import (
	"context"
	"encoding/json"
	"log/slog"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/server"
	"vending-machine/internal/usecase/catalog"
	"vending-machine/internal/usecase/connection"
	"vending-machine/internal/usecase/purchase"
)

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	Login(ctx context.Context, req model.ForwardRequest) (*model.Response, error)
	Logout(ctx context.Context, req model.ForwardRequest) (*model.Response, error)
	Slots(ctx context.Context, req model.ForwardRequest) (*model.Response, error)
	Restock(ctx context.Context, req model.ForwardRequest) (*model.Response, error)
}

// Service is the singleton implementation.
type Service struct {
	conn     connection.UseCase
	catalog  catalog.UseCase
	purchase purchase.UseCase
	log      *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in cmd/main.
func New(conn connection.UseCase, cat catalog.UseCase, p purchase.UseCase, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{conn: conn, catalog: cat, purchase: p, log: log}
}

// Login forwards the operator login. The body holds the password, so it is
// never logged.
func (s *Service) Login(ctx context.Context, req model.ForwardRequest) (*model.Response, error) {
	resp, err := s.conn.Forward(ctx, server.MethodPost, server.PathOperatorLogin, req)
	if err != nil {
		return nil, err
	}
	var who struct {
		Username string `json:"username"`
	}
	if resp.Success() && json.Unmarshal(resp.Body, &who) == nil {
		s.log.Info("operator logged in", "username", who.Username)
	}
	return resp, nil
}

func (s *Service) Logout(ctx context.Context, req model.ForwardRequest) (*model.Response, error) {
	return s.conn.Forward(ctx, server.MethodPost, server.PathOperatorLogout, req)
}

func (s *Service) Slots(ctx context.Context, req model.ForwardRequest) (*model.Response, error) {
	return s.conn.Forward(ctx, server.MethodGet, server.PathOperatorSlots, req)
}

// Restock forwards a confirmed visit. Results from earlier sales go first, so
// the server applies the visit on top of every sale before it. Nothing is
// queued here: offline, the page keeps the visit on screen and the operator
// confirms again (D1; an offline queue is future work).
func (s *Service) Restock(ctx context.Context, req model.ForwardRequest) (*model.Response, error) {
	s.purchase.FlushQuick(ctx)
	resp, err := s.conn.Forward(ctx, server.MethodPost, server.PathRestock, req)
	if err != nil {
		return nil, err
	}
	if resp.Status < 300 {
		var v struct {
			ID             string `json:"id"`
			CatalogVersion int64  `json:"catalogVersion"`
		}
		_ = json.Unmarshal(resp.Body, &v)
		s.log.Info("restock visit saved", "visit", v.ID, "catalog_version", v.CatalogVersion)
		s.catalog.Changed() // FR-02, FR-15: the kiosk reloads right after the visit
	}
	return resp, nil
}
