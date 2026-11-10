// Package model holds the plain data every layer shares: vends, slots,
// transactions, the journal entry, the server's answers and the business
// errors. It does no I/O and imports nothing from internal/.
package model

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Failure reasons from the motor board, as the server records them on a FAILED transaction.
const (
	ReasonNoAck        = "NO_ACK"        // no echo: cable, port or board down
	ReasonMotorTimeout = "MOTOR_TIMEOUT" // echo but no END: jam, no motor, no power
	ReasonPortError    = "PORT_ERROR"    // the serial port could not be opened, read or written
	ReasonUnknownSlot  = "UNKNOWN_SLOT"  // the slot number is not in the slot map
)

// VendResult is the outcome of one vend command.
type VendResult struct {
	OK     bool   `json:"ok"`
	Slot   int    `json:"slot"`
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
	AckMs  int64  `json:"ackMs,omitempty"` // milliseconds until the echo
	EndMs  int64  `json:"endMs,omitempty"` // milliseconds until END
	Extra  string `json:"extra,omitempty"` // unexpected lines from the board, or the port error
}

// VendRecord is one recent vend, as the developer page lists it.
type VendRecord struct {
	At     int64  `json:"at"`
	TxID   string `json:"txId"`
	Slot   int    `json:"slot"`
	Code   string `json:"code"`
	Status string `json:"status"`
	Reason string `json:"failureReason,omitempty"`
	AckMs  int64  `json:"ackMs,omitempty"`
	EndMs  int64  `json:"endMs,omitempty"`
}

// Simulator modes (FR-29): the three board behaviours, switchable at any time.
const (
	SimOK    = "ok"     // echo, then END after the turn time: DISPENSED
	SimJam   = "jam"    // echo, no END: MOTOR_TIMEOUT after 5 s
	SimNoAck = "no_ack" // no echo: NO_ACK after 0.5 s
)

// SimModes lists the valid simulator modes.
var SimModes = []string{SimOK, SimJam, SimNoAck}

// CheckSimMode returns an error wrapping ErrInvalidSimMode for an unknown mode.
func CheckSimMode(mode string) error {
	switch mode {
	case SimOK, SimJam, SimNoAck:
		return nil
	}
	return fmt.Errorf("%w %q: use ok, jam or no_ack", ErrInvalidSimMode, mode)
}

// SlotMap maps the slot number the server and kiosk use (1..N) to the board
// code of the coil wired to it. The wiring is per machine, so it lives in the
// agent's configuration and the server keeps using plain slot numbers.
type SlotMap map[int]string

// DefaultSlotMap is VM-01's wiring as measured:
// top row 1 = AA, 2 = BA; middle row 3 = AB, 4 = BB; bottom row 5 = AC, 6 = BC.
func DefaultSlotMap() SlotMap {
	return SlotMap{1: "AA", 2: "BA", 3: "AB", 4: "BB", 5: "AC", 6: "BC"}
}

// ParseSlotMap reads the "slotMap" setting, e.g. {"1": "AA", "2": "BA"}.
// An empty setting gives the default map.
func ParseSlotMap(raw map[string]string) (SlotMap, error) {
	if len(raw) == 0 {
		return DefaultSlotMap(), nil
	}
	m := SlotMap{}
	usedBy := map[string]int{}
	for k, v := range raw {
		n, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || n < 1 || n > 60 {
			return nil, fmt.Errorf("slotMap: %q is not a slot number (1 to 60)", k)
		}
		code := strings.ToUpper(strings.TrimSpace(v))
		if !ValidCode(code) {
			return nil, fmt.Errorf("slotMap: slot %d: %q is not a board code (column A-H, then row A-F, e.g. AB)", n, v)
		}
		if other, dup := usedBy[code]; dup {
			a, b := min(n, other), max(n, other)
			return nil, fmt.Errorf("slotMap: slots %d and %d both use %s", a, b, code)
		}
		usedBy[code] = n
		m[n] = code
	}
	return m, nil
}

// ValidCode reports whether code is a column A-H followed by a row A-F.
func ValidCode(code string) bool {
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'H' && code[1] >= 'A' && code[1] <= 'F'
}

// String lists the map in slot order, e.g. "1=AA 2=BA".
func (m SlotMap) String() string {
	slots := make([]int, 0, len(m))
	for s := range m {
		slots = append(slots, s)
	}
	sort.Ints(slots)
	parts := make([]string, len(slots))
	for i, s := range slots {
		parts[i] = fmt.Sprintf("%d=%s", s, m[s])
	}
	return strings.Join(parts, " ")
}
