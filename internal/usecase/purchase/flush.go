package purchase

import (
	"context"
	"time"

	"vending-machine/internal/model"
)

// Recover runs once at start-up, before the kiosk page is served. A purchase
// left in stage VENDING may or may not have turned its coil (the product may
// have fallen), so it becomes FAILED with AGENT_RESTART and is never turned
// again (BR-14, TC-37). The flusher then reports it.
func (s *Service) Recover() {
	for _, e := range s.journal.List() {
		switch e.Stage {
		case model.StageVending:
			s.log.Warn("restarted during a vend: reporting FAILED with AGENT_RESTART; the coil is never turned again (BR-14)",
				"tx", e.TxID, "slot", e.Slot, "since", e.UpdatedAt.Local().Format(time.DateTime))
			e.Stage, e.Status, e.Reason = model.StageResult, model.StatusFailed, model.ReasonAgentRestart
			if err := s.journal.Put(e); err != nil {
				s.log.Error("journal: cannot save the AGENT_RESTART result; it is reported from memory", "tx", e.TxID, "err", err)
			}
			s.recent.add(model.VendRecord{At: s.now().UnixMilli(), TxID: e.TxID, Slot: e.Slot, Status: model.StatusFailed, Reason: model.ReasonAgentRestart})
		case model.StageResult:
			s.log.Info("result from before the restart still to report", "tx", e.TxID, "status", e.Status, "reason", e.Reason)
		case model.StagePaying:
			s.log.Info("payment from before the restart without an answer; it is settled without dispensing", "tx", e.TxID)
		}
	}
}

// Start delivers the journal: at once, every FlushEvery, and when nudged.
func (s *Service) Start(ctx context.Context) { go s.flushLoop(ctx) }

func (s *Service) flushLoop(ctx context.Context) {
	tick := time.NewTicker(s.t.FlushEvery)
	defer tick.Stop()
	for {
		if s.journal.Len() > 0 {
			s.Flush(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.wake:
		}
	}
}

func (s *Service) wakeFlusher() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Flush goes through the journal oldest first and stops at the first sign
// that the server is unreachable.
func (s *Service) Flush(ctx context.Context) {
	for _, e := range s.journal.List() {
		if ctx.Err() != nil {
			return
		}
		if err := s.flushOne(ctx, e.TxID); model.IsOffline(err) {
			return
		}
	}
}

func (s *Service) flushOne(ctx context.Context, id string) error {
	s.vendMu.Lock()
	defer s.vendMu.Unlock()
	e, ok := s.journal.Get(id) // read again under the lock
	if !ok {
		return nil
	}
	switch e.Stage {
	case model.StageResult:
		return s.report(ctx, e)
	case model.StagePaying:
		return s.settleUnanswered(ctx, e)
	}
	return nil // VENDING exists only while a purchase holds the lock, or before Recover
}

func (s *Service) FlushQuick(ctx context.Context) {
	if s.journal.Len() == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, s.t.FlushQuick)
	defer cancel()
	s.Flush(cctx)
}
