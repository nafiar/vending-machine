// Package purchase owns the money-to-motor story: create, pay, dispense,
// report, and the journal that makes it safe across restarts (PRD D12, BR-14).
package purchase

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/coil"
	"vending-machine/internal/repository/journal"
	"vending-machine/internal/repository/server"
	"vending-machine/internal/usecase/catalog"
	"vending-machine/internal/usecase/connection"
)

// UseCase is the port the delivery layer and other usecases depend on.
type UseCase interface {
	// Recover runs once at start-up, before the kiosk page is served.
	Recover()
	// Start runs the journal flusher until ctx ends.
	Start(ctx context.Context)

	// Create forwards "Beli" and remembers the new transaction's slot.
	Create(ctx context.Context, req model.ForwardRequest) (*model.Response, error)
	// Pay is "Konfirmasi bayar": PAID on the server, then the coil, then the
	// result. It returns after the coil has turned (or failed).
	Pay(ctx context.Context, txID, requestID string) (model.PurchaseResult, error)
	Cancel(ctx context.Context, txID string, req model.ForwardRequest) (*model.Response, error)
	// Report is "Produk tidak keluar?".
	Report(ctx context.Context, txID string, req model.ForwardRequest) (*model.Response, error)

	// Flush delivers the journal, oldest first.
	Flush(ctx context.Context)
	// FlushQuick gives the journal a moment before work that depends on the
	// server's counts: a new purchase or a restock visit.
	FlushQuick(ctx context.Context)
	Pending() []model.JournalEntry
	PendingCount() int
	Recent() []model.VendRecord

	// ArmPowerCut makes the agent stop right after the next vend command, as
	// a power cut would (TC-37, simulator only).
	ArmPowerCut(on bool)
	PowerCutArmed() bool
}

// Timeouts of the purchase steps.
type Timeouts struct {
	Pay         time.Duration // one pay attempt
	PayAttempts int
	PayGap      time.Duration // pause between pay attempts
	Result      time.Duration // reporting a result while the customer waits
	Call        time.Duration // settling an unanswered payment
	FlushQuick  time.Duration // settling the journal before a new purchase or a restock
	FlushEvery  time.Duration
}

// DefaultTimeouts are the production values.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Pay:         3 * time.Second,
		PayAttempts: 3,
		PayGap:      500 * time.Millisecond,
		Result:      4 * time.Second,
		Call:        6 * time.Second,
		FlushQuick:  2 * time.Second,
		FlushEvery:  10 * time.Second,
	}
}

// Deps are what a Service is built from.
type Deps struct {
	Coil       coil.Repository
	Journal    journal.Repository
	Server     server.Repository
	Connection connection.UseCase
	Catalog    catalog.UseCase
	Log        *slog.Logger
	Exit       func(code int) // for the simulated power cut
	Timeouts   Timeouts       // zero: DefaultTimeouts
}

// Service is the singleton implementation. It depends only on interfaces,
// so every dependency can be faked in tests.
type Service struct {
	coil    coil.Repository
	journal journal.Repository
	server  server.Repository
	conn    connection.UseCase
	catalog catalog.UseCase
	log     *slog.Logger
	exit    func(int)
	t       Timeouts
	now     func() time.Time

	// vendMu allows one purchase at a time through the board (BR-14). The
	// journal flusher takes it too, so it never touches a purchase in progress.
	vendMu sync.Mutex

	slots     slotMemory
	recent    recentVends
	wake      chan struct{}
	crashNext atomic.Bool
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
	return &Service{
		coil:    d.Coil,
		journal: d.Journal,
		server:  d.Server,
		conn:    d.Connection,
		catalog: d.Catalog,
		log:     d.Log,
		exit:    d.Exit,
		t:       d.Timeouts,
		now:     time.Now,
		slots:   slotMemory{m: map[string]slotNote{}},
		wake:    make(chan struct{}, 1),
	}
}

func (s *Service) Pending() []model.JournalEntry { return s.journal.List() }
func (s *Service) PendingCount() int             { return s.journal.Len() }
func (s *Service) Recent() []model.VendRecord    { return s.recent.all() }
func (s *Service) ArmPowerCut(on bool)           { s.crashNext.Store(on) }
func (s *Service) PowerCutArmed() bool           { return s.crashNext.Load() }

// noteError tells the connection what a failed server call means.
func (s *Service) noteError(err error) {
	if apiErr, ok := model.AsAPIError(err); ok {
		s.conn.NoteAPIError(apiErr)
		return
	}
	s.conn.Offline(err)
}
