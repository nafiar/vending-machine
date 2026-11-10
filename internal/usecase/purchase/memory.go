package purchase

import (
	"sync"
	"time"

	"vending-machine/internal/model"
)

// slotMemory remembers each transaction's slot from the create answer, for
// servers whose pay answer doesn't carry it.
type slotMemory struct {
	mu sync.Mutex
	m  map[string]slotNote
}

type slotNote struct {
	slot int
	at   time.Time
}

func (s *slotMemory) put(id string, slot int, now time.Time) {
	id, ok := model.NormalizeTxID(id)
	if !ok || slot <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.m {
		if now.Sub(v.at) > 15*time.Minute {
			delete(s.m, k)
		}
	}
	s.m[id] = slotNote{slot: slot, at: now}
}

func (s *slotMemory) get(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id].slot
}

// recentVends is the developer page's list of the last 20 vends.
type recentVends struct {
	mu   sync.Mutex
	list []model.VendRecord // newest first
}

func (r *recentVends) add(v model.VendRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append([]model.VendRecord{v}, r.list...)
	if len(r.list) > 20 {
		r.list = r.list[:20]
	}
}

func (r *recentVends) all() []model.VendRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.VendRecord{}, r.list...)
}
