// Package journal is the port to the purchase journal: the purchase steps
// that must survive a crash or a power cut, kept until the server has the
// final result. The on-disk implementation lives in journal/file.
//
// BR-14: the agent saves the transaction before sending the vend command. A
// transaction found at start-up in stage VENDING may or may not have turned
// its coil, so it is reported FAILED with AGENT_RESTART and is never turned
// again.
package journal

import "vending-machine/internal/model"

// Repository stores unfinished purchases by transaction id.
type Repository interface {
	// Put saves an entry. On a storage error the in-memory copy stays updated
	// and the error is returned.
	Put(e model.JournalEntry) error
	Get(txID string) (model.JournalEntry, bool)
	Delete(txID string) error
	// List returns the entries, oldest first.
	List() []model.JournalEntry
	Len() int
}
