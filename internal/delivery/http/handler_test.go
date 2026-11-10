package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"vending-machine/internal/model"
	"vending-machine/internal/usecase/purchase"
)

// Unit tests: the handlers with a fake usecase. They pin the page's contract,
// status codes and error bodies, without any repository.

type fakePurchase struct {
	purchase.UseCase // unused methods panic
	err              error
	gotID            string
}

func (f *fakePurchase) Pay(_ context.Context, id, _ string) (model.PurchaseResult, error) {
	f.gotID = id
	return model.NewPurchaseResult(id, model.StatusDispensed, ""), f.err
}

func pay(t *testing.T, p *fakePurchase, id string) (int, map[string]any) {
	t.Helper()
	web := fstest.MapFS{"kiosk/index.html": {}, "dev/index.html": {}}
	h := New(Deps{Purchase: p, Web: web}).Routes()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/transactions/"+id+"/pay", nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func TestPayAnswersTheResult(t *testing.T) {
	p := &fakePurchase{}
	code, out := pay(t, p, "%20AAAAAAAA-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if code != 200 || out["status"] != "DISPENSED" || p.gotID != id {
		t.Fatalf("%d %v id %q", code, out, p.gotID)
	}
}

func TestPayRejectsABadID(t *testing.T) {
	p := &fakePurchase{}
	if code, out := pay(t, p, "not-a-uuid"); code != 404 || out["code"] != "NOT_FOUND" || p.gotID != "" {
		t.Fatalf("%d %v", code, out)
	}
}

func TestErrorsMapToThePageContract(t *testing.T) {
	cases := []struct {
		err        error
		status     int
		code, more string
	}{
		{model.Offline(errors.New("dial tcp")), 503, "OFFLINE", ""},
		{&model.APIError{Status: 401, Code: "INVALID_MACHINE_KEY", Message: "Kunci salah"}, 503, "OFFLINE", "INVALID_MACHINE_KEY"},
		{&model.APIError{Status: 410, Code: "EXPIRED", Message: "Waktu habis"}, 410, "EXPIRED", ""},
		{&model.TransactionClosedError{}, 409, "TRANSACTION_CLOSED", "Transaksi ini sudah ditutup. Silakan ulangi pembelian."},
		{&model.TransactionClosedError{Status: "CANCELLED"}, 409, "TRANSACTION_CLOSED", "Transaksi sudah cancelled."},
		{fmt.Errorf("%w: disk full", model.ErrJournal), 500, "AGENT_ERROR", ""},
	}
	for _, c := range cases {
		code, out := pay(t, &fakePurchase{err: c.err}, id)
		if code != c.status || out["code"] != c.code {
			t.Errorf("%v: %d %v", c.err, code, out)
		}
		if c.more != "" && out["reason"] != c.more && out["message"] != c.more {
			t.Errorf("%v: %v, want %q", c.err, out, c.more)
		}
	}
}
