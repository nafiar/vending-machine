package model

import (
	"regexp"
	"strings"
	"time"
)

// Transaction states (PRD, "Purchase sequence and transaction states").
const (
	StatusPending   = "PENDING"
	StatusPaid      = "PAID"
	StatusDispensed = "DISPENSED"
	StatusFailed    = "FAILED"
)

// Failure reasons the agent adds to the board's.
const (
	ReasonAgentRestart = "AGENT_RESTART" // BR-14: restarted after the vend command may have gone out
	ReasonPayNoReply   = "PAY_NO_REPLY"  // the server took the payment but its answer never arrived, so the coil never turned
	ReasonJournalError = "JOURNAL_ERROR" // the journal could not be written, so the coil was not turned
)

// TxState is a transaction as the server returns it.
type TxState struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	FailureReason *string `json:"failureReason"`
	SlotNumber    int     `json:"slotNumber"` // sent by servers that include it; else the agent's own record is used
}

// Reason is the failure reason, or "".
func (t TxState) Reason() string {
	if t.FailureReason == nil {
		return ""
	}
	return *t.FailureReason
}

// PurchaseResult is what the kiosk page reads after "Konfirmasi bayar".
type PurchaseResult struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	FailureReason *string `json:"failureReason"`
}

// NewPurchaseResult builds a result; an empty reason is left out.
func NewPurchaseResult(id, status, reason string) PurchaseResult {
	r := PurchaseResult{ID: id, Status: status}
	if reason != "" {
		r.FailureReason = &reason
	}
	return r
}

// Journal stages of one purchase.
const (
	StagePaying  = "PAYING"  // pay sent to the server, no answer yet: the coil has not turned
	StageVending = "VENDING" // the vend command may have reached the board
	StageResult  = "RESULT"  // the board answered; the server has not acknowledged the result yet
)

// JournalEntry is one unfinished purchase.
type JournalEntry struct {
	TxID      string      `json:"txId"`
	Slot      int         `json:"slot"`
	Stage     string      `json:"stage"`
	Status    string      `json:"status,omitempty"`        // DISPENSED or FAILED, in stage RESULT
	Reason    string      `json:"failureReason,omitempty"` // for FAILED
	Board     *VendResult `json:"board,omitempty"`         // what the board answered, for the log
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
	Attempts  int         `json:"attempts,omitempty"` // tries to report the result
	LastError string      `json:"lastError,omitempty"`
}

var txIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidTxID reports whether id is a lowercase UUID, the only transaction ids
// the agent accepts (and the only names the journal uses).
func ValidTxID(id string) bool { return txIDPattern.MatchString(id) }

// NormalizeTxID lowercases and trims id, and reports whether it is valid.
func NormalizeTxID(id string) (string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	return id, ValidTxID(id)
}
