// Package status answers "what is the agent doing?": the page's connection
// check, and the developer page (/dev/), which also switches the simulator
// (FR-29: success, motor timeout or no echo on demand).
package status

import (
	"context"
	"log/slog"
	"time"

	"vending-machine/internal/common/config"
	"vending-machine/internal/model"
	"vending-machine/internal/repository/coil"
	"vending-machine/internal/usecase/catalog"
	"vending-machine/internal/usecase/connection"
	"vending-machine/internal/usecase/purchase"
)

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// Health asks the server live (while "Mesin offline" shows, and when a
	// product is tapped).
	Health(ctx context.Context) (model.Health, error)
	Status() model.AgentStatus
	// SetSimMode switches the simulator: "ok", "jam", "no_ack", or "crash" to
	// stop the agent right after the next vend command (TC-37).
	// model.ErrNotSimulator on the real board.
	SetSimMode(mode string) (model.SimState, error)
}

// Deps are what a Service is built from.
type Deps struct {
	Config     config.Config
	Version    string
	Coil       coil.Repository
	Sim        coil.Simulator // the same board when it is the simulator, else nil
	Connection connection.UseCase
	Catalog    catalog.UseCase
	Purchase   purchase.UseCase
	Log        *slog.Logger
}

// Service is the singleton implementation.
type Service struct {
	d       Deps
	started time.Time
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in cmd/main.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d, started: time.Now()}
}

func (s *Service) Health(ctx context.Context) (model.Health, error) {
	v, err := s.d.Catalog.Heartbeat(ctx)
	if err != nil {
		return model.Health{}, err
	}
	return model.Health{CatalogVersion: v, Board: s.d.Coil.Describe(), Pending: s.d.Purchase.PendingCount()}, nil
}

func (s *Service) Status() model.AgentStatus {
	b := model.BoardStatus{Kind: s.d.Config.Board, Name: s.d.Coil.Describe(), SlotMap: s.d.Config.Slots.String()}
	if s.d.Sim != nil {
		b.Simulated, b.SimMode, b.CrashArmed = true, s.d.Sim.Mode(), s.d.Purchase.PowerCutArmed()
	}
	return model.AgentStatus{
		Version:        s.d.Version,
		StartedAt:      s.started,
		Listen:         s.d.Config.Listen,
		ConfigFile:     s.d.Config.Path,
		DataDir:        s.d.Config.DataDir,
		Server:         s.d.Connection.Snapshot(),
		Machine:        s.d.Catalog.Machine(),
		CatalogVersion: s.d.Catalog.Version(),
		Board:          b,
		Journal:        s.d.Purchase.Pending(),
		Recent:         s.d.Purchase.Recent(),
	}
}

func (s *Service) SetSimMode(mode string) (model.SimState, error) {
	if s.d.Sim == nil {
		return model.SimState{}, model.ErrNotSimulator
	}
	if mode == "crash" {
		s.d.Purchase.ArmPowerCut(true)
		s.d.Log.Warn("simulator: the agent will stop right after the next vend command (power cut test)")
	} else {
		if err := s.d.Sim.SetMode(mode); err != nil {
			return model.SimState{}, err
		}
		s.d.Purchase.ArmPowerCut(false)
		s.d.Log.Info("simulator mode changed", "mode", mode)
	}
	return model.SimState{Mode: s.d.Sim.Mode(), CrashArmed: s.d.Purchase.PowerCutArmed()}, nil
}
