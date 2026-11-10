package purchase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/journal"
	"vending-machine/internal/repository/server"
	"vending-machine/internal/usecase/catalog"
	"vending-machine/internal/usecase/connection"
)

// Unit tests: the purchase rules with every port faked. No disk, no network.
// The end-to-end suite in internal/delivery/http covers the wired agent.

const tx = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

// memJournal is an in-memory journal.Repository that can fail on demand.
type memJournal struct {
	mu      sync.Mutex
	entries map[string]model.JournalEntry
	failOn  string // a stage whose Put fails
}

var _ journal.Repository = (*memJournal)(nil)

func newMemJournal() *memJournal { return &memJournal{entries: map[string]model.JournalEntry{}} }

func (j *memJournal) Put(e model.JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries[e.TxID] = e
	if e.Stage == j.failOn {
		return errors.New("disk full")
	}
	return nil
}
func (j *memJournal) Get(id string) (model.JournalEntry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.entries[id]
	return e, ok
}
func (j *memJournal) Delete(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.entries, id)
	return nil
}
func (j *memJournal) List() []model.JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []model.JournalEntry
	for _, e := range j.entries {
		out = append(out, e)
	}
	return out
}
func (j *memJournal) Len() int { return len(j.List()) }

// stageCoil records the journal stage at the moment each vend is sent.
type stageCoil struct {
	journal *memJournal
	stages  []string
}

func (c *stageCoil) Vend(slot int) model.VendResult {
	e, _ := c.journal.Get(tx)
	c.stages = append(c.stages, e.Stage)
	return model.VendResult{OK: true, Slot: slot, Code: "AA"}
}
func (c *stageCoil) Describe() string { return "fake" }
func (c *stageCoil) Close() error     { return nil }

// fakeServer answers pay with PAID and records results.
type fakeServer struct {
	server.Repository // unused methods panic
	payStatus         string
	results           []string
}

func (s *fakeServer) BaseURL() string { return "fake" }
func (s *fakeServer) Pay(context.Context, string, string) (model.TxState, error) {
	return model.TxState{ID: tx, Status: s.payStatus, SlotNumber: 1}, nil
}
func (s *fakeServer) Result(_ context.Context, _ string, status, reason string) error {
	s.results = append(s.results, status+" "+reason)
	return nil
}

// quietCatalog ignores reload requests.
type quietCatalog struct{ catalog.UseCase }

func (quietCatalog) Changed() {}

func newService(j *memJournal, c *stageCoil, s *fakeServer) *Service {
	return New(Deps{Coil: c, Journal: j, Server: s, Connection: connection.New(s, nil, 0), Catalog: quietCatalog{}})
}

// BR-14: the transaction is on disk as VENDING before the command goes out,
// the coil turns exactly once, and the journal is empty once the server has
// the result.
func TestPayJournalsBeforeVending(t *testing.T) {
	j := newMemJournal()
	c := &stageCoil{journal: j}
	s := &fakeServer{payStatus: model.StatusPaid}
	out, err := newService(j, c, s).Pay(context.Background(), tx, "rid")
	if err != nil || out.Status != model.StatusDispensed {
		t.Fatalf("%+v %v", out, err)
	}
	if len(c.stages) != 1 || c.stages[0] != model.StageVending {
		t.Fatalf("stages at vend time %v, want exactly one, at VENDING", c.stages)
	}
	if len(s.results) != 1 || s.results[0] != "DISPENSED " || j.Len() != 0 {
		t.Fatalf("results %v journal %v", s.results, j.List())
	}
}

// BR-14: if the journal can't record VENDING, the coil does not turn.
func TestPayDoesNotVendWithoutTheJournal(t *testing.T) {
	j := newMemJournal()
	j.failOn = model.StageVending
	c := &stageCoil{journal: j}
	s := &fakeServer{payStatus: model.StatusPaid}
	out, err := newService(j, c, s).Pay(context.Background(), tx, "rid")
	if err != nil || out.Status != model.StatusFailed || out.FailureReason == nil || *out.FailureReason != model.ReasonJournalError {
		t.Fatalf("%+v %v", out, err)
	}
	if len(c.stages) != 0 {
		t.Fatal("the coil must not turn")
	}
}

// A transaction the server already closed is refused without vending.
func TestPayRefusesAClosedTransaction(t *testing.T) {
	j := newMemJournal()
	c := &stageCoil{journal: j}
	_, err := newService(j, c, &fakeServer{payStatus: "CANCELLED"}).Pay(context.Background(), tx, "rid")
	var closed *model.TransactionClosedError
	if !errors.As(err, &closed) || closed.Status != "CANCELLED" || len(c.stages) != 0 || j.Len() != 0 {
		t.Fatalf("%v stages %v journal %d", err, c.stages, j.Len())
	}
}
