// Package catalog owns what the kiosk shows: the catalog cache (FR-02), the
// heartbeat that learns of a newer catalog (FR-28), and the product images.
package catalog

import (
	"context"
	"log/slog"
	"time"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/imagecache"
	"vending-machine/internal/repository/server"
	"vending-machine/internal/usecase/connection"
)

// UseCase is the port the delivery layer and other usecases depend on.
type UseCase interface {
	// Start runs the first heartbeat and catalog load (FR-02 "at boot"),
	// then the heartbeat every interval, until ctx ends.
	Start(ctx context.Context)
	// Catalog returns the catalog JSON, from the cache when it is fresh.
	Catalog(ctx context.Context, force bool) ([]byte, error)
	// Store keeps a fresh catalog, e.g. the one a CATALOG_STALE refusal carries.
	Store(body []byte) error
	// Changed marks the catalog stale and reloads it in the background.
	Changed()
	// Heartbeat asks the server live and returns its catalog version.
	Heartbeat(ctx context.Context) (int64, error)
	Version() int64
	Machine() model.Machine
	// Image returns a product image under /uploads/: from disk, else from the
	// server (and then kept). model.ErrNotFound for a path that isn't one.
	Image(ctx context.Context, rel string) ([]byte, error)
}

// Timeouts of the catalog's server calls.
type Timeouts struct {
	Catalog time.Duration
	Health  time.Duration
	Image   time.Duration
}

// DefaultTimeouts are the production values.
func DefaultTimeouts() Timeouts {
	return Timeouts{Catalog: 5 * time.Second, Health: 5 * time.Second, Image: 8 * time.Second}
}

// Deps are what a Service is built from.
type Deps struct {
	Server     server.Repository
	Images     imagecache.Repository
	Connection connection.UseCase
	Log        *slog.Logger
	Heartbeat  time.Duration // interval between heartbeats
	Timeouts   Timeouts      // zero: DefaultTimeouts
}

// Service is the singleton implementation.
type Service struct {
	server    server.Repository
	images    imagecache.Repository
	conn      connection.UseCase
	log       *slog.Logger
	heartbeat time.Duration
	t         Timeouts
	now       func() time.Time

	cache cache
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in cmd/main.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Timeouts == (Timeouts{}) {
		d.Timeouts = DefaultTimeouts()
	}
	if d.Heartbeat <= 0 {
		d.Heartbeat = 5 * time.Minute
	}
	return &Service{server: d.Server, images: d.Images, conn: d.Connection, log: d.Log, heartbeat: d.Heartbeat, t: d.Timeouts, now: time.Now}
}

func (s *Service) Start(ctx context.Context) {
	go func() {
		if _, err := s.Heartbeat(ctx); err != nil {
			s.log.Warn("server not reachable at start; the kiosk shows \"Mesin offline\" until it is", "server", s.server.BaseURL(), "err", err)
		}
		if _, err := s.Catalog(ctx, true); err == nil {
			m := s.cache.info()
			s.log.Info("catalog loaded", "machine", m.Code, "name", m.Name, "catalog_version", s.cache.currentVersion())
		}
	}()
	go s.heartbeatLoop(ctx)
}

func (s *Service) Version() int64         { return s.cache.currentVersion() }
func (s *Service) Machine() model.Machine { return s.cache.info() }
