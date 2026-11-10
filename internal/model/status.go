package model

import "time"

// Health is the page's connection check.
type Health struct {
	CatalogVersion int64
	Board          string
	Pending        int
}

// AgentStatus is everything the developer page shows.
type AgentStatus struct {
	Version        string
	StartedAt      time.Time
	Listen         string
	ConfigFile     string
	DataDir        string
	Server         Connection
	Machine        Machine
	CatalogVersion int64
	Board          BoardStatus
	Journal        []JournalEntry
	Recent         []VendRecord
}

// BoardStatus describes the motor board, or the simulator.
type BoardStatus struct {
	Kind       string // "serial" or "sim", from the configuration
	Name       string
	SlotMap    string
	Simulated  bool
	SimMode    string
	CrashArmed bool
}

// SimState is the simulator after a switch.
type SimState struct {
	Mode       string
	CrashArmed bool
}
