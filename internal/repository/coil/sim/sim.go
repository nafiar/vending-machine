// Package sim is a coil driver without hardware, for development, tests and
// as a demo fallback. It waits as long as the real board would.
package sim

import (
	"sync"
	"time"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/coil"
)

// Sim simulates the motor board. Build one in cmd/main.
type Sim struct {
	mu         sync.Mutex // one vend at a time, like the real board
	modeMu     sync.Mutex
	slots      model.SlotMap
	mode       string
	turn       time.Duration
	ackTimeout time.Duration
	endTimeout time.Duration
}

var _ coil.Simulator = (*Sim)(nil)

// New returns a simulator. turn is how long a successful turn takes
// (measured on VM-01: about 2.5 s).
func New(slots model.SlotMap, mode string, turn time.Duration) (*Sim, error) {
	s := &Sim{slots: slots, turn: turn, ackTimeout: coil.AckTimeout, endTimeout: coil.EndTimeout}
	if mode == "" {
		mode = model.SimOK
	}
	if err := s.SetMode(mode); err != nil {
		return nil, err
	}
	return s, nil
}

// SetMode switches the behaviour for the next vends.
func (s *Sim) SetMode(mode string) error {
	if err := model.CheckSimMode(mode); err != nil {
		return err
	}
	s.modeMu.Lock()
	s.mode = mode
	s.modeMu.Unlock()
	return nil
}

// Mode returns the current behaviour.
func (s *Sim) Mode() string {
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	return s.mode
}

// Describe names the board for logs and the status page.
func (s *Sim) Describe() string { return "simulator (" + s.Mode() + ")" }

// Close does nothing.
func (s *Sim) Close() error { return nil }

// Vend pretends to turn the slot's coil.
func (s *Sim) Vend(slot int) model.VendResult {
	code, ok := s.slots[slot]
	if !ok {
		return model.VendResult{Slot: slot, Reason: model.ReasonUnknownSlot}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	echo := 70 * time.Millisecond // as measured
	switch s.Mode() {
	case model.SimNoAck:
		time.Sleep(s.ackTimeout)
		return model.VendResult{Slot: slot, Code: code, Reason: model.ReasonNoAck}
	case model.SimJam:
		time.Sleep(s.endTimeout)
		return model.VendResult{Slot: slot, Code: code, Reason: model.ReasonMotorTimeout, AckMs: echo.Milliseconds()}
	default:
		time.Sleep(s.turn)
		return model.VendResult{OK: true, Slot: slot, Code: code, AckMs: echo.Milliseconds(), EndMs: s.turn.Milliseconds()}
	}
}
