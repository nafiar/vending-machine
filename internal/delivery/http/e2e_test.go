package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"vending-machine/internal/common/config"
	delivery "vending-machine/internal/delivery/http"
	"vending-machine/internal/model"
	"vending-machine/internal/repository/coil"
	"vending-machine/internal/repository/coil/sim"
	imagefile "vending-machine/internal/repository/imagecache/file"
	journalfile "vending-machine/internal/repository/journal/file"
	serverhttp "vending-machine/internal/repository/server/http"
	"vending-machine/internal/usecase/catalog"
	"vending-machine/internal/usecase/connection"
	"vending-machine/internal/usecase/operator"
	"vending-machine/internal/usecase/purchase"
	"vending-machine/internal/usecase/status"
)

// These tests run the whole agent as cmd/main wires it: the real usecases,
// journal and server client, against an in-memory copy of vending-server and
// a scripted motor board. They pin the behaviour the kiosk page relies on.

// scriptedBoard answers each vend with what the test decides, and counts the
// commands, so a test can prove a coil turned exactly once.
type scriptedBoard struct {
	mu     sync.Mutex
	vends  []int
	answer func(slot int) model.VendResult
}

var _ coil.Repository = (*scriptedBoard)(nil)

func (b *scriptedBoard) Vend(slot int) model.VendResult {
	b.mu.Lock()
	b.vends = append(b.vends, slot)
	answer := b.answer
	b.mu.Unlock()
	if answer != nil {
		return answer(slot)
	}
	return model.VendResult{OK: true, Slot: slot, Code: model.DefaultSlotMap()[slot], AckMs: 70, EndMs: 2400}
}
func (b *scriptedBoard) Describe() string { return "scripted" }
func (b *scriptedBoard) Close() error     { return nil }
func (b *scriptedBoard) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.vends)
}

type harness struct {
	t        *testing.T
	fake     *fakeServer
	board    *scriptedBoard
	journal  *journalfile.File
	catalog  *catalog.Service
	purchase *purchase.Service
	http     *httptest.Server
	dir      string
	exited   []int
}

var testWeb = fstest.MapFS{
	"kiosk/index.html": {Data: []byte("<!doctype html><title>kiosk</title>")},
	"dev/index.html":   {Data: []byte("<!doctype html><title>dev</title>")},
}

type harnessOptions struct {
	dir string
	key string
	sim coil.Simulator // the board, when set
}

func newHarness(t *testing.T, fake *fakeServer, o harnessOptions) *harness {
	t.Helper()
	if o.dir == "" {
		o.dir = t.TempDir()
	}
	if o.key == "" {
		o.key = fake.key
	}
	j, problems, err := journalfile.New(filepath.Join(o.dir, "journal"))
	if err != nil || len(problems) > 0 {
		t.Fatal(err, problems)
	}
	srv, err := serverhttp.New(fake.ts.URL, o.key, false, "test")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, fake: fake, board: &scriptedBoard{}, journal: j, dir: o.dir}
	var board coil.Repository = h.board
	if o.sim != nil {
		board = o.sim
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{ServerURL: fake.ts.URL, MachineKey: o.key, Board: "serial", Slots: model.DefaultSlotMap(), DataDir: o.dir, HeartbeatS: 300}

	conn := connection.New(srv, log, time.Second)
	h.catalog = catalog.New(catalog.Deps{Server: srv, Images: imagefile.New(filepath.Join(o.dir, "cache", "uploads")), Connection: conn, Log: log,
		Heartbeat: cfg.Heartbeat(), Timeouts: catalog.Timeouts{Catalog: time.Second, Health: time.Second, Image: time.Second}})
	pt := purchase.DefaultTimeouts()
	pt.Pay, pt.PayGap, pt.Result, pt.Call, pt.FlushQuick = 500*time.Millisecond, 10*time.Millisecond, 500*time.Millisecond, time.Second, time.Second
	h.purchase = purchase.New(purchase.Deps{Coil: board, Journal: j, Server: srv, Connection: conn, Catalog: h.catalog, Log: log,
		Exit: func(code int) { h.exited = append(h.exited, code) }, Timeouts: pt})
	op := operator.New(conn, h.catalog, h.purchase, log)
	st := status.New(status.Deps{Config: cfg, Version: "test", Coil: board, Sim: o.sim, Connection: conn, Catalog: h.catalog, Purchase: h.purchase, Log: log})
	handler := delivery.New(delivery.Deps{Purchase: h.purchase, Catalog: h.catalog, Operator: op, Status: st, Web: testWeb, Log: log})

	h.http = httptest.NewServer(handler.Routes())
	t.Cleanup(h.http.Close)
	return h
}

func (h *harness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.http.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const txA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const txB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

// buy creates a transaction for slot through the agent, as the page does.
func (h *harness) buy(id string, slot int) {
	h.t.Helper()
	code, out := h.call("POST", "/api/transactions", map[string]any{"id": id, "slotNumber": slot, "catalogVersion": h.fake.version})
	if code != 201 {
		h.t.Fatalf("create: %d %v", code, out)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TC-01: pay, one turn of the right coil, DISPENSED recorded, journal empty.
func TestPurchaseDispenses(t *testing.T) {
	for _, slotInPay := range []bool{true, false} { // new server, and one without slotNumber in pay
		fake := newFakeServer(t)
		fake.slotInPay = slotInPay
		h := newHarness(t, fake, harnessOptions{})
		h.buy(txA, 3)
		code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
		if code != 200 || out["status"] != "DISPENSED" || out["failureReason"] != nil {
			t.Fatalf("pay: %d %v", code, out)
		}
		if len(h.board.vends) != 1 || h.board.vends[0] != 3 {
			t.Fatalf("vends %v, want exactly one on slot 3", h.board.vends)
		}
		if tx := fake.tx(txA); tx.status != "DISPENSED" || fake.slots[3].qty != 0 {
			t.Fatalf("server: %+v qty %d", tx, fake.slots[3].qty)
		}
		if h.journal.Len() != 0 {
			t.Fatalf("journal: %+v", h.journal.List())
		}
	}
}

// TC-06 / FR-08: a jam is FAILED with MOTOR_TIMEOUT and the slot is blocked.
func TestJamIsReportedAsFailed(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.board.answer = func(slot int) model.VendResult {
		return model.VendResult{Slot: slot, Code: "AA", Reason: model.ReasonMotorTimeout, AckMs: 70}
	}
	h.buy(txA, 1)
	code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if code != 200 || out["status"] != "FAILED" || out["failureReason"] != "MOTOR_TIMEOUT" {
		t.Fatalf("%d %v", code, out)
	}
	if tx := fake.tx(txA); tx.status != "FAILED" || tx.reason != "MOTOR_TIMEOUT" || !fake.slots[1].blocked || fake.slots[1].qty != 3 {
		t.Fatalf("server: %+v %+v", tx, fake.slots[1])
	}
}

// The server goes away while the coil turns: the customer still gets the
// product, the result waits in the journal, and is delivered later.
func TestResultWaitsInTheJournalUntilTheServerIsBack(t *testing.T) {
	fake := newFakeServer(t)
	dir := t.TempDir()
	h := newHarness(t, fake, harnessOptions{dir: dir})
	h.buy(txA, 1)
	h.board.answer = func(slot int) model.VendResult {
		fake.set(func(f *fakeServer) { f.down = true })
		return model.VendResult{OK: true, Slot: slot, Code: "AA", AckMs: 70, EndMs: 2400}
	}
	code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if code != 200 || out["status"] != "DISPENSED" {
		t.Fatalf("the page must hear DISPENSED: %d %v", code, out)
	}
	e, ok := h.journal.Get(txA)
	if !ok || e.Stage != model.StageResult || e.Status != "DISPENSED" || e.Attempts != 1 {
		t.Fatalf("journal: %+v", e)
	}
	if _, err := os.Stat(filepath.Join(dir, "journal", txA+".json")); err != nil {
		t.Fatal("the result must be on disk:", err)
	}

	fake.set(func(f *fakeServer) { f.down = false })
	h.purchase.Flush(context.Background())
	if tx := fake.tx(txA); tx.status != "DISPENSED" || h.journal.Len() != 0 || h.board.count() != 1 {
		t.Fatalf("after the server is back: %+v, journal %d, vends %d", tx, h.journal.Len(), h.board.count())
	}
}

// TC-37 / BR-14: a restart after the command was sent reports AGENT_RESTART and never turns the coil again.
func TestRestartDuringVendReportsAgentRestart(t *testing.T) {
	fake := newFakeServer(t)
	dir := t.TempDir()
	first := newHarness(t, fake, harnessOptions{dir: dir})
	first.buy(txA, 1)
	fake.set(func(f *fakeServer) { f.txs[txA].status = "PAID" })
	if err := first.journal.Put(model.JournalEntry{TxID: txA, Slot: 1, Stage: model.StageVending}); err != nil {
		t.Fatal(err)
	}

	again := newHarness(t, fake, harnessOptions{dir: dir}) // the agent starts again on the same folder
	again.purchase.Recover()
	again.purchase.Flush(context.Background())
	if tx := fake.tx(txA); tx.status != "FAILED" || tx.reason != "AGENT_RESTART" || !fake.slots[1].blocked {
		t.Fatalf("server: %+v", tx)
	}
	if again.board.count() != 0 || again.journal.Len() != 0 {
		t.Fatalf("vends %d, journal %d", again.board.count(), again.journal.Len())
	}
	// A late pay for the same transaction (e.g. a page reload) answers without vending.
	code, out := again.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if code != 200 || out["status"] != "FAILED" || again.board.count() != 0 {
		t.Fatalf("late pay: %d %v, vends %d", code, out, again.board.count())
	}
}

// The simulated power cut (TC-37 on a computer): the agent stops right after the command.
func TestSimulatedPowerCut(t *testing.T) {
	fake := newFakeServer(t)
	dir := t.TempDir()
	s, _ := sim.New(model.DefaultSlotMap(), model.SimOK, 10*time.Millisecond)
	h := newHarness(t, fake, harnessOptions{dir: dir, sim: s})
	if code, out := h.call("POST", "/api/agent/sim", map[string]any{"mode": "crash"}); code != 200 || out["crashArmed"] != true {
		t.Fatalf("arm: %d %v", code, out)
	}
	h.buy(txA, 1)
	h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if len(h.exited) != 1 || h.exited[0] != 3 {
		t.Fatalf("exit codes %v", h.exited)
	}
	if e, ok := h.journal.Get(txA); !ok || e.Stage != model.StageVending {
		t.Fatalf("the power cut leaves VENDING on disk: %+v", e)
	}
}

// Pay got no answer at all: the coil is not turned; once the server answers,
// a payment it took becomes FAILED PAY_NO_REPLY, one it never got is closed.
func TestPayWithoutAnswerIsSettledWithoutDispensing(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.buy(txA, 1)
	h.buy(txB, 3)

	fake.set(func(f *fakeServer) { f.dropPay = true }) // the server takes the payment, the answer is lost
	code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if code != 503 || out["code"] != "OFFLINE" || h.board.count() != 0 {
		t.Fatalf("pay: %d %v, vends %d", code, out, h.board.count())
	}
	fake.set(func(f *fakeServer) { f.dropPay = false; f.down = true }) // this one never reaches the server
	if code, _ := h.call("POST", "/api/transactions/"+txB+"/pay", map[string]any{}); code != 503 {
		t.Fatalf("pay B: %d", code)
	}
	if e, _ := h.journal.Get(txA); e.Stage != model.StagePaying {
		t.Fatalf("journal: %+v", h.journal.List())
	}

	fake.set(func(f *fakeServer) { f.down = false })
	h.purchase.Flush(context.Background())
	if a, b := fake.tx(txA), fake.tx(txB); a.status != "FAILED" || a.reason != "PAY_NO_REPLY" || b.status != "EXPIRED" {
		t.Fatalf("settled: A %+v, B %+v", a, b)
	}
	if h.board.count() != 0 || h.journal.Len() != 0 {
		t.Fatalf("vends %d, journal %v", h.board.count(), h.journal.List())
	}
}

// A server fault on pay: the payment may have been saved, so it is settled
// like a lost answer (never dispensed later).
func TestPayServerFaultIsSettledWithoutDispensing(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.buy(txA, 1)
	fake.set(func(f *fakeServer) { f.payFault = true })
	if code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{}); code != 500 || out["code"] != "INTERNAL" || h.board.count() != 0 {
		t.Fatalf("%d %v vends %d", code, out, h.board.count())
	}
	fake.set(func(f *fakeServer) { f.payFault = false })
	h.purchase.Flush(context.Background())
	if tx := fake.tx(txA); tx.status != "FAILED" || tx.reason != "PAY_NO_REPLY" || h.board.count() != 0 || h.journal.Len() != 0 {
		t.Fatalf("%+v vends %d journal %d", tx, h.board.count(), h.journal.Len())
	}
}

// A pay retried by the agent after a lost answer still dispenses, once.
func TestPayRetryAfterALostAnswerDispensesOnce(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.buy(txA, 1)
	var once sync.Once
	fake.set(func(f *fakeServer) { f.dropPay = true })
	go func() { // the network comes back after the first attempt
		for {
			if fake.tx(txA).status == "PAID" {
				once.Do(func() { fake.set(func(f *fakeServer) { f.dropPay = false }) })
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if code != 200 || out["status"] != "DISPENSED" || h.board.count() != 1 {
		t.Fatalf("%d %v, vends %d", code, out, h.board.count())
	}
}

// TC-05: an expired payment is refused and the coil stays still.
func TestExpiredPaymentIsRefused(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.buy(txA, 1)
	fake.set(func(f *fakeServer) { f.txs[txA].created = time.Now().Add(-61 * time.Second) })
	code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if code != 410 || out["code"] != "EXPIRED" || h.board.count() != 0 || h.journal.Len() != 0 {
		t.Fatalf("%d %v vends %d journal %d", code, out, h.board.count(), h.journal.Len())
	}
}

// TC-08 seen from the agent: paying again never turns the coil again.
func TestRepeatedPayNeverVendsTwice(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.buy(txA, 1)
	h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	code, out := h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	if code != 200 || out["status"] != "DISPENSED" || h.board.count() != 1 {
		t.Fatalf("%d %v vends %d", code, out, h.board.count())
	}
}

// FR-02: served from the cache; reloaded after a sale; a CATALOG_STALE refusal refreshes it.
func TestCatalogCache(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	if code, out := h.call("GET", "/api/catalog", nil); code != 200 || out["catalogVersion"] != float64(7) {
		t.Fatalf("%d %v", code, out)
	}
	h.call("GET", "/api/catalog", nil)
	if fake.catalogReads != 1 {
		t.Fatalf("catalog read %d times, want once (cache)", fake.catalogReads)
	}

	h.buy(txA, 3)
	h.call("POST", "/api/transactions/"+txA+"/pay", map[string]any{})
	eventually(t, "the reload after the sale", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.catalogReads >= 2
	})
	_, out := h.call("GET", "/api/catalog", nil)
	if slot3 := out["slots"].([]any)[2].(map[string]any); slot3["available"] != false {
		t.Fatalf("slot 3 sold its last item: %v", slot3)
	}

	fake.set(func(f *fakeServer) { f.version = 9 }) // an admin changed a price
	code, stale := h.call("POST", "/api/transactions", map[string]any{"id": txB, "slotNumber": 1, "catalogVersion": 7})
	if code != 409 || stale["code"] != "CATALOG_STALE" || h.catalog.Version() != 9 {
		t.Fatalf("%d %v cache v%d", code, stale["code"], h.catalog.Version())
	}
}

// FR-10: offline, and a refused machine key, both show "Mesin offline".
func TestOfflineAndRefusedKey(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	fake.set(func(f *fakeServer) { f.down = true })
	if code, out := h.call("GET", "/api/catalog", nil); code != 503 || out["code"] != "OFFLINE" {
		t.Fatalf("down: %d %v", code, out)
	}
	if code, out := h.call("GET", "/api/health", nil); code != 503 || out["code"] != "OFFLINE" {
		t.Fatalf("health down: %d %v", code, out)
	}
	fake.set(func(f *fakeServer) { f.down = false })
	if code, _ := h.call("GET", "/api/health", nil); code != 200 {
		t.Fatalf("health back: %d", code)
	}

	wrong := newHarness(t, fake, harnessOptions{key: "vmk_wrong"})
	code, out := wrong.call("GET", "/api/catalog", nil)
	if code != 503 || out["code"] != "OFFLINE" || out["reason"] != "INVALID_MACHINE_KEY" || out["message"] == "" {
		t.Fatalf("wrong key: %d %v", code, out)
	}
}

// Sales before a restock visit are recorded before the visit.
func TestRestockGoesAfterPendingResults(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.buy(txA, 1)
	fake.set(func(f *fakeServer) { f.txs[txA].status = "PAID" })
	_ = h.journal.Put(model.JournalEntry{TxID: txA, Slot: 1, Stage: model.StageResult, Status: "DISPENSED"})

	req, _ := http.NewRequest("POST", h.http.URL+"/api/restock-sessions", strings.NewReader(`{"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","items":[]}`))
	req.Header.Set("Authorization", "Bearer op-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp.StatusCode)
	}
	log := fake.callLog()
	result, restock := -1, -1
	for i, c := range log {
		if c == "POST /api/machine/transactions/"+txA+"/result DISPENSED" {
			result = i
		}
		if c == "POST /api/machine/restock-sessions" {
			restock = i
		}
	}
	if result < 0 || restock < 0 || result > restock {
		t.Fatalf("the result must reach the server before the visit: %v", log)
	}
}

// "Produk tidak keluar?" right after a sale whose result was still in the journal.
func TestReportSendsThePendingResultFirst(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	h.buy(txA, 1)
	fake.set(func(f *fakeServer) { f.txs[txA].status = "PAID" })
	_ = h.journal.Put(model.JournalEntry{TxID: txA, Slot: 1, Stage: model.StageResult, Status: "DISPENSED"})
	if code, out := h.call("POST", "/api/transactions/"+txA+"/report", map[string]any{}); code != 200 || out["reported"] != true {
		t.Fatalf("%d %v", code, out)
	}
}

func TestImagesAreKeptOnDisk(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	png := []byte("\x89PNG fake")
	fake.set(func(f *fakeServer) { f.images["/uploads/products/p1.png"] = png })
	get := func() (int, []byte, string) {
		resp, err := http.Get(h.http.URL + "/uploads/products/p1.png")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header.Get("Content-Type")
	}
	if code, b, ct := get(); code != 200 || !bytes.Equal(b, png) || ct != "image/png" {
		t.Fatalf("%d %q %s", code, b, ct)
	}
	fake.set(func(f *fakeServer) { f.down = true })
	if code, b, _ := get(); code != 200 || !bytes.Equal(b, png) {
		t.Fatalf("from disk: %d %q", code, b)
	}
	for _, bad := range []string{"/uploads/../config.json", "/uploads/products/x.exe", "/uploads/a/b/c/d.png"} {
		resp, _ := http.Get(h.http.URL + bad)
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestPagesAndUnknownAPI(t *testing.T) {
	fake := newFakeServer(t)
	h := newHarness(t, fake, harnessOptions{})
	for path, want := range map[string]string{"/": "kiosk", "/dev/": "dev"} {
		resp, err := http.Get(h.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), want) {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
	}
	if code, out := h.call("GET", "/api/nope", nil); code != 404 || out["code"] != "NOT_FOUND" {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := h.call("POST", "/api/agent/sim", map[string]any{"mode": "jam"}); code != 404 || out["code"] != "NOT_SIMULATOR" {
		t.Fatalf("sim on a real board: %d %v", code, out)
	}
}
