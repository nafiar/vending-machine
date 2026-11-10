package purchase

import (
	"context"
	"fmt"
	"time"

	"vending-machine/internal/model"
)

// Pay is PAID on the server, then the coil, then the result.
func (s *Service) Pay(ctx context.Context, id, requestID string) (model.PurchaseResult, error) {
	// The purchase carries on even if the page stops waiting: once money is
	// taken, the coil turns and the result is recorded.
	ctx = context.WithoutCancel(ctx)

	s.vendMu.Lock()
	defer s.vendMu.Unlock()

	// A purchase this machine already took through the board: answer from
	// the journal and never turn the coil again (BR-14).
	if e, ok := s.journal.Get(id); ok {
		switch e.Stage {
		case model.StageResult:
			s.wakeFlusher()
			return model.NewPurchaseResult(id, e.Status, e.Reason), nil
		case model.StageVending: // only while a vend is in progress, which the lock excludes
			return model.NewPurchaseResult(id, model.StatusFailed, model.ReasonAgentRestart), nil
		case model.StagePaying:
			// An earlier pay never got an answer. It is settled without dispensing.
			if err := s.settleUnanswered(ctx, e); model.IsOffline(err) {
				return model.PurchaseResult{}, err
			}
			return model.PurchaseResult{}, &model.TransactionClosedError{}
		}
	}

	slot := s.slots.get(id)
	if err := s.journal.Put(model.JournalEntry{TxID: id, Slot: slot, Stage: model.StagePaying}); err != nil {
		s.log.Error("journal: cannot save the payment step, so the payment is not taken", "tx", id, "err", err)
		_ = s.journal.Delete(id)
		return model.PurchaseResult{}, fmt.Errorf("%w: %v", model.ErrJournal, err)
	}

	st, err := s.pay(ctx, id, requestID)
	if err != nil {
		if apiErr, ok := model.AsAPIError(err); ok {
			if apiErr.Status >= 500 && !apiErr.KeyProblem() {
				// A server fault: the payment may or may not have been saved, so it
				// is settled like a lost answer, without dispensing.
				s.log.Warn("pay failed on the server; the coil was not turned, the transaction is settled later", "tx", id, "code", apiErr.Code)
				s.wakeFlusher()
				return model.PurchaseResult{}, apiErr
			}
			_ = s.journal.Delete(id) // the server refused: nothing was paid
			return model.PurchaseResult{}, apiErr
		}
		s.log.Warn("pay got no answer; the coil was not turned. The transaction is settled once the server answers", "tx", id, "err", err)
		s.wakeFlusher()
		if !model.IsOffline(err) {
			err = model.Offline(err)
		}
		return model.PurchaseResult{}, err
	}
	switch st.Status {
	case model.StatusPaid:
	case model.StatusDispensed, model.StatusFailed: // finished earlier: a repeated request
		_ = s.journal.Delete(id)
		return model.NewPurchaseResult(id, st.Status, st.Reason()), nil
	default:
		_ = s.journal.Delete(id)
		return model.PurchaseResult{}, &model.TransactionClosedError{Status: st.Status}
	}
	if st.SlotNumber > 0 {
		if slot != 0 && slot != st.SlotNumber {
			s.log.Warn("the server and the agent disagree on the slot; using the server's", "tx", id, "agent_slot", slot, "server_slot", st.SlotNumber)
		}
		slot = st.SlotNumber
	}

	// BR-14: the transaction is on disk before the command goes out.
	e := model.JournalEntry{TxID: id, Slot: slot, Stage: model.StageVending}
	if err := s.journal.Put(e); err != nil {
		s.log.Error("journal: cannot save before vending, so the coil is not turned (BR-14)", "tx", id, "err", err)
		e.Stage, e.Status, e.Reason = model.StageResult, model.StatusFailed, model.ReasonJournalError
		_ = s.journal.Put(e) // the memory copy, at least
		_ = s.report(ctx, e)
		return model.NewPurchaseResult(id, model.StatusFailed, model.ReasonJournalError), nil
	}

	if s.crashNext.CompareAndSwap(true, false) {
		s.simulatePowerCut(id, slot)
		return model.NewPurchaseResult(id, model.StatusFailed, model.ReasonAgentRestart), nil // only reached in tests
	}

	res := s.vend(slot)
	e.Stage, e.Board = model.StageResult, &res
	if res.OK {
		e.Status = model.StatusDispensed
	} else {
		e.Status, e.Reason = model.StatusFailed, res.Reason
	}
	if err := s.journal.Put(e); err != nil {
		s.log.Error("journal: cannot save the result; reporting it now", "tx", id, "err", err)
	}
	s.recent.add(model.VendRecord{At: s.now().UnixMilli(), TxID: id, Slot: slot, Code: res.Code, Status: e.Status, Reason: e.Reason, AckMs: res.AckMs, EndMs: res.EndMs})
	s.log.Info("vend", "tx", id, "slot", slot, "code", res.Code, "result", e.Status, "reason", e.Reason,
		"ack_ms", res.AckMs, "end_ms", res.EndMs, "extra", res.Extra, "board", s.coil.Describe())

	if err := s.report(ctx, e); err != nil {
		s.log.Warn("result not delivered yet; it stays in the journal and is resent", "tx", id, "err", err)
	}
	return model.NewPurchaseResult(id, e.Status, e.Reason), nil
}

// pay asks the server for PENDING -> PAID. Pay is safe to repeat (a repeat
// returns the current state), so attempts that got no answer are retried.
func (s *Service) pay(ctx context.Context, id, requestID string) (model.TxState, error) {
	var last error
	for attempt := 1; attempt <= s.t.PayAttempts; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, s.t.Pay)
		st, err := s.server.Pay(cctx, id, requestID)
		cancel()
		if err == nil {
			s.conn.Online()
			return st, nil
		}
		if apiErr, ok := model.AsAPIError(err); ok {
			s.conn.NoteAPIError(apiErr)
			return model.TxState{}, err
		}
		last = err
		if attempt < s.t.PayAttempts {
			time.Sleep(s.t.PayGap)
		}
	}
	s.conn.Offline(last)
	return model.TxState{}, last
}

func (s *Service) vend(slot int) model.VendResult {
	if slot <= 0 {
		return model.VendResult{Slot: slot, Reason: model.ReasonUnknownSlot, Extra: "the agent does not know this transaction's slot"}
	}
	return s.coil.Vend(slot)
}

// report sends a final result. It returns an error while the result stays in
// the journal (no answer, or a server fault); a refusal the server will never
// change is logged and dropped.
func (s *Service) report(ctx context.Context, e model.JournalEntry) error {
	cctx, cancel := context.WithTimeout(ctx, s.t.Result)
	defer cancel()
	err := s.server.Result(cctx, e.TxID, e.Status, e.Reason)
	if err == nil {
		s.conn.Online()
		_ = s.journal.Delete(e.TxID)
		s.log.Info("result recorded", "tx", e.TxID, "status", e.Status, "reason", e.Reason)
		s.catalog.Changed() // FR-02: reload after each sale
		return nil
	}
	if apiErr, ok := model.AsAPIError(err); ok {
		s.conn.NoteAPIError(apiErr)
		if !apiErr.KeyProblem() && apiErr.Status < 500 {
			s.log.Error("the server refused the result; removing it from the journal", "tx", e.TxID, "status", e.Status,
				"reason", e.Reason, "code", apiErr.Code, "message", apiErr.Message)
			_ = s.journal.Delete(e.TxID)
			return nil
		}
	} else {
		s.conn.Offline(err)
	}
	e.Attempts++
	e.LastError = err.Error()
	_ = s.journal.Put(e)
	return err
}

// settleUnanswered closes a payment whose answer never arrived. The coil
// never turned, so: still PENDING -> cancel it (EXPIRED); PAID -> FAILED with
// PAY_NO_REPLY; anything else is already final.
func (s *Service) settleUnanswered(ctx context.Context, e model.JournalEntry) error {
	cctx, cancel := context.WithTimeout(ctx, s.t.Call)
	defer cancel()
	st, err := s.server.Transaction(cctx, e.TxID)
	if err != nil {
		if apiErr, ok := model.AsAPIError(err); ok {
			s.conn.NoteAPIError(apiErr)
			if apiErr.Status == 404 {
				_ = s.journal.Delete(e.TxID)
				return nil
			}
		} else {
			s.conn.Offline(err)
		}
		return err
	}
	s.conn.Online()
	switch st.Status {
	case model.StatusPending:
		if err := s.server.Cancel(cctx, e.TxID, "TIMEOUT"); err != nil {
			if !model.IsOffline(err) {
				_ = s.journal.Delete(e.TxID)
			}
			return err
		}
		s.log.Info("unanswered payment settled: it never reached the server, closed as EXPIRED", "tx", e.TxID)
		_ = s.journal.Delete(e.TxID)
		return nil
	case model.StatusPaid:
		s.log.Warn("unanswered payment settled: the server has it as PAID but the coil never turned; reporting FAILED with PAY_NO_REPLY", "tx", e.TxID, "slot", e.Slot)
		e.Stage, e.Status, e.Reason = model.StageResult, model.StatusFailed, model.ReasonPayNoReply
		_ = s.journal.Put(e)
		return s.report(ctx, e)
	default:
		_ = s.journal.Delete(e.TxID)
		return nil
	}
}

func (s *Service) simulatePowerCut(id string, slot int) {
	s.log.Warn("simulated power cut: the agent stops right after sending the vend command (TC-37). Start it again to see AGENT_RESTART",
		"tx", id, "slot", slot)
	time.Sleep(300 * time.Millisecond) // the coil has started turning
	if s.exit != nil {
		s.exit(3)
	}
}
