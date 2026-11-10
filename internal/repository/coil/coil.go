// Package coil is the port to the vending machine's motor board: one coil
// turn per call. The drivers live in subpackages: serial (the CH340 board)
// and sim (no hardware).
package coil

import (
	"time"

	"vending-machine/internal/model"
)

// Timeouts every driver keeps to, real or simulated.
const (
	AckTimeout = 500 * time.Millisecond // FR-08: no echo within 0.5 s -> NO_ACK
	EndTimeout = 5 * time.Second        // FR-08: no END within 5 s of the command -> MOTOR_TIMEOUT
)

// Repository turns exactly one coil per call. Implementations allow one vend
// at a time and never resend a command (BR-14).
type Repository interface {
	Vend(slot int) model.VendResult
	Describe() string
	Close() error
}

// Simulator is a Repository whose behaviour can be switched (FR-29).
type Simulator interface {
	Repository
	Mode() string
	SetMode(mode string) error
}
