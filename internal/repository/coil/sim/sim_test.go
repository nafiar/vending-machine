package sim

import (
	"testing"
	"time"

	"vending-machine/internal/model"
)

func TestSimulatorModes(t *testing.T) {
	s, err := New(model.DefaultSlotMap(), model.SimOK, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	s.ackTimeout, s.endTimeout = 20*time.Millisecond, 60*time.Millisecond
	if r := s.Vend(1); !r.OK || r.Code != "AA" {
		t.Fatalf("ok: %+v", r)
	}
	_ = s.SetMode(model.SimJam)
	if r := s.Vend(1); r.Reason != model.ReasonMotorTimeout {
		t.Fatalf("jam: %+v", r)
	}
	_ = s.SetMode(model.SimNoAck)
	if r := s.Vend(1); r.Reason != model.ReasonNoAck {
		t.Fatalf("no_ack: %+v", r)
	}
	if err := s.SetMode("explode"); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
	if r := s.Vend(7); r.Reason != model.ReasonUnknownSlot {
		t.Fatalf("unknown slot: %+v", r)
	}
}
