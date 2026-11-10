package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"vending-machine/internal/model"
)

// cache is FR-02: the catalog and its version, kept in memory and reloaded at
// boot, after each sale and each operator visit, daily at 03:00, and when the
// server reports a newer version. The kiosk page reads it from here, so the
// home screen appears without waiting for the server (NFR-01).
type cache struct {
	mu        sync.Mutex // held during a reload, so callers share one fetch
	body      []byte
	version   int64
	machine   model.Machine
	fetchedAt time.Time
	dirty     bool
}

func (c *cache) currentVersion() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

func (c *cache) info() model.Machine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.machine
}

func (c *cache) loaded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body != nil
}

func (c *cache) storeLocked(body []byte, now time.Time) error {
	var head struct {
		CatalogVersion int64           `json:"catalogVersion"`
		Machine        model.Machine   `json:"machine"`
		Slots          json.RawMessage `json:"slots"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return fmt.Errorf("the server's catalog is not readable: %w", err)
	}
	if head.Slots == nil {
		return errors.New("the server's catalog has no slots")
	}
	c.body = append([]byte(nil), body...)
	c.version, c.machine, c.fetchedAt, c.dirty = head.CatalogVersion, head.Machine, now, false
	return nil
}

// Catalog returns the cached catalog, or reloads it when it is missing, marked
// stale, older than the last 03:00, or when the server was last seen offline
// (the kiosk doesn't sell offline: D1, FR-10).
func (s *Service) Catalog(ctx context.Context, force bool) ([]byte, error) {
	c := &s.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if !force && c.body != nil && !c.dirty && s.conn.IsOnline() && !passed0300(c.fetchedAt, now) {
		return c.body, nil
	}
	cctx, cancel := context.WithTimeout(ctx, s.t.Catalog)
	defer cancel()
	body, err := s.server.Catalog(cctx)
	if err != nil {
		s.noteError(err)
		return nil, err
	}
	s.conn.Online()
	if err := c.storeLocked(body, now); err != nil {
		s.log.Error("catalog", "err", err)
		return nil, err
	}
	return c.body, nil
}

func (s *Service) Store(body []byte) error {
	s.cache.mu.Lock()
	defer s.cache.mu.Unlock()
	return s.cache.storeLocked(body, s.now())
}

func (s *Service) Changed() {
	s.cache.mu.Lock()
	s.cache.dirty = true
	s.cache.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = s.Catalog(ctx, false)
	}()
}

// passed0300 reports whether a 03:00 local time lies between fetched and now.
func passed0300(fetched, now time.Time) bool {
	boundary := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
	if now.Before(boundary) {
		boundary = boundary.AddDate(0, 0, -1)
	}
	return fetched.Before(boundary)
}

// ---------------------------------------------------------------- heartbeat (FR-28)

// Heartbeat records this machine's contact time on the server and learns the
// current catalog version.
func (s *Service) Heartbeat(ctx context.Context) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, s.t.Health)
	defer cancel()
	v, err := s.server.Heartbeat(cctx)
	if err != nil {
		s.noteError(err)
		return 0, err
	}
	s.conn.Online()
	if s.cache.loaded() && v != s.cache.currentVersion() {
		s.log.Info("the server has a newer catalog; reloading", "catalog_version", v)
		s.Changed()
	}
	return v, nil
}

func (s *Service) heartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(s.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_, _ = s.Heartbeat(ctx)
		}
	}
}

// noteError tells the connection what a failed call means.
func (s *Service) noteError(err error) {
	if apiErr, ok := model.AsAPIError(err); ok {
		s.conn.NoteAPIError(apiErr)
		return
	}
	s.conn.Offline(err)
}
