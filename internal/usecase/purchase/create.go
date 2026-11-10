package purchase

import (
	"context"
	"encoding/json"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/server"
)

// Create forwards "Beli". The agent remembers each new transaction's slot
// from the server's answer, and keeps the fresh catalog that comes with a
// CATALOG_STALE refusal (FR-02, FR-04).
func (s *Service) Create(ctx context.Context, req model.ForwardRequest) (*model.Response, error) {
	s.FlushQuick(ctx) // results from earlier sales first, so counts and blocks are current
	resp, err := s.conn.Forward(ctx, server.MethodPost, server.PathTransactions, req)
	if err != nil {
		return nil, err
	}
	if resp.Success() {
		var c struct {
			ID         string `json:"id"`
			SlotNumber int    `json:"slotNumber"`
			Amount     int64  `json:"amount"`
			Product    struct {
				Name string `json:"name"`
			} `json:"product"`
		}
		if json.Unmarshal(resp.Body, &c) == nil {
			s.slots.put(c.ID, c.SlotNumber, s.now())
			s.log.Info("transaction created", "tx", c.ID, "slot", c.SlotNumber, "product", c.Product.Name, "amount", c.Amount)
		}
		return resp, nil
	}
	var e struct {
		Code    string          `json:"code"`
		Catalog json.RawMessage `json:"catalog"`
	}
	if json.Unmarshal(resp.Body, &e) == nil {
		switch e.Code {
		case "CATALOG_STALE":
			if len(e.Catalog) > 0 && s.catalog.Store(e.Catalog) == nil {
				s.log.Info("catalog was stale; the server sent the new one", "catalog_version", s.catalog.Version())
			}
		case "SOLD_OUT":
			s.catalog.Changed()
		}
	}
	return resp, nil
}

func (s *Service) Cancel(ctx context.Context, txID string, req model.ForwardRequest) (*model.Response, error) {
	return s.conn.Forward(ctx, server.MethodPost, server.TransactionPath(txID, "cancel"), req)
}

// Report is "Produk tidak keluar?". The sale's result goes first if it is
// still in the journal, since the server only accepts reports on DISPENSED.
func (s *Service) Report(ctx context.Context, txID string, req model.ForwardRequest) (*model.Response, error) {
	if e, ok := s.journal.Get(txID); ok && e.Stage == model.StageResult {
		cctx, cancel := context.WithTimeout(ctx, s.t.FlushQuick)
		_ = s.flushOne(cctx, txID)
		cancel()
	}
	resp, err := s.conn.Forward(ctx, server.MethodPost, server.TransactionPath(txID, "report"), req)
	if err == nil && resp.Success() {
		s.log.Info("customer reported \"Produk tidak keluar\"", "tx", txID)
	}
	return resp, err
}
