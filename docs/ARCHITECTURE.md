# Architecture & Code Convention

Clean Architecture (Uncle Bob) adapted to idiomatic Go, for the vending-machine
Kiosk Agent. This is the rulebook for **where code lives and why**. Read it
before adding a package.

The one sentence version: **dependencies point inward, and every boundary
between layers is a Go `interface` so the outer layer can be swapped for a mock
in tests.**

---

## 1. The layers and the dependency rule

```
                 cmd/            (composition root — wires everything, depends on all)
                   │
   ┌───────────────┼────────────────┐
   ▼               ▼                 ▼
delivery/  ──►  usecase/  ──►   repository/        (outer → inner, import direction)
   │               │                 │
   └───────────────┴────────┬────────┘
                            ▼
                     model/ + common/              (leaf: depend on nothing internal)
```

**The rule: an import may only point inward (toward `model`). Never outward.**

| Layer | Path | Holds | May import | Must NOT import |
|---|---|---|---|---|
| **model** | `internal/model` | Plain domain structs & constants (Transaction, Slot, Result). No behaviour that needs I/O. | std lib only | anything in `internal/` |
| **common** | `internal/common/...` | Cross-cutting helpers: config, logging, http helpers, errors. | std lib, `model` | `delivery`, `usecase`, `repository` |
| **repository** | `internal/repository/<thing>` | Talks to the outside world: DBs, the server API, the coil over USB, the on-disk journal. | `model`, `common` | `usecase`, `delivery` |
| **usecase** | `internal/usecase/<story>` | Business rules / orchestration. Grouped by user story. | `model`, `common`, repository **interfaces** | `delivery`, any concrete driver |
| **delivery** | `internal/delivery/<transport>` | Web API surface: HTTP, gRPC. Decodes requests, calls a usecase, encodes responses. | `model`, `common`, usecase **interfaces** | `repository` drivers |
| **cmd** | `cmd/<binary>` | `main`: parse flags, build concrete drivers, inject them, start the server. | everything | — |

> If you ever need an outward import to make something compile, the type you're
> reaching for belongs in `model` (or the interface is on the wrong side). Fix
> the boundary, don't add the import.

---

## 2. The port/adapter pattern (your "abstract class + inherit")

Go has no abstract classes or inheritance. The equivalent is:

- **abstract class with required methods** → a Go **`interface`** (the *port*).
- **a subclass that implements it** → a **struct** in a subpackage (the *adapter*).
- **singleton object of the class** → one exported struct with a constructor
  `New(...) *T`, created **once** in `cmd/main` and passed down.

The interface lives at the **root of the thing it describes**; each concrete
implementation lives in a **subpackage named after the technology**. This is the
layout you asked for:

```
internal/repository/user/
├── repository.go          # the port: `type Repository interface { ... }` + shared types
├── mysql/
│   └── mysql.go           # type MySQL struct{} ; func New(db *sql.DB) *MySQL
│                          # implements user.Repository
└── mariadb/
    └── mariadb.go         # type MariaDB struct{} ; func New(...) *MariaDB
                           # implements user.Repository
```

`usecase` imports **`repository/user`** (the port) and never
`repository/user/mysql` (the adapter). Only `cmd/main` imports the adapter, to
construct it. Swapping MySQL → MariaDB is a one-line change in `main`, and in a
test you pass a mock of `user.Repository` instead.

The *same* shape applies to usecases: `delivery` imports the usecase port, and
`cmd/main` constructs the concrete usecase.

---

## 3. Repository layer — convention

A repository wraps **one external dependency** and exposes it as model types.

### 3.1 The port file: `repository/<thing>/<thing>.go`

```go
// Package coil drives the vending machine's motor board (one coil per vend).
package coil

import "vending-machine/internal/model"

//go:generate mockgen -source=coil.go -destination=mock/coil_mock.go -package=mock

// Repository turns exactly one coil per call. Implementations allow one vend
// at a time (BR-14). This is the port; drivers live in subpackages.
type Repository interface {
	Vend(slot int) model.VendResult
	Describe() string
	Close() error
}
```

Rules:

- The interface is **named for its role in the consumer**, not the tech — `Repository`, not `MySQLUserRepo`. Callers read `user.Repository`, `coil.Repository`.
- Keep interfaces **small** — only the methods a usecase actually calls. A fat interface is hard to mock and leaks implementation.
- Method inputs/outputs are **`model` types or std types**, never driver types (`*sql.Rows`, `*serial.Port`). That's what keeps the port swappable.
- Add a `//go:generate mockgen ...` line so `go generate ./...` produces the mock.

### 3.2 The adapter: `repository/<thing>/<tech>/<tech>.go`

```go
package serial

import (
	"vending-machine/internal/model"
	"vending-machine/internal/repository/coil"
)

// Serial is the production driver over the CH340 board. It is a singleton:
// one is built in cmd/main and shared.
type Serial struct { /* port, slotmap, mutex... */ }

// New constructs the driver. Compile-time proof it satisfies the port:
var _ coil.Repository = (*Serial)(nil)

func New(port string, slots model.SlotMap) *Serial { ... }

func (s *Serial) Vend(slot int) model.VendResult { ... }
func (s *Serial) Describe() string               { ... }
func (s *Serial) Close() error                   { ... }
```

- Always add `var _ coil.Repository = (*Serial)(nil)` — it fails the build the
  moment an adapter drifts from the port.
- The adapter package imports the port package (inward), never the reverse.
- `sim` is just another adapter next to `serial` — same interface, no hardware.

---

## 4. Usecase layer — convention

A usecase holds **business rules** and orchestrates repositories. Group by
**user story / unified capability**, one package per story:

```
internal/usecase/
├── purchase/        # the money-to-motor story: create, pay, dispense, report
│   ├── purchase.go      # port: type UseCase interface + New() constructor
│   ├── create.go        # one file per sub-flow (handler of the story)
│   ├── pay.go
│   ├── report.go
│   └── purchase_test.go
├── catalog/         # loading & caching the catalog
└── operator/        # operator login / restock
```

### 4.1 The usecase port + constructor

```go
// Package purchase owns the money-to-motor step and the journal that makes it
// safe across restarts (PRD D12 / BR-14).
package purchase

import (
	"context"
	"vending-machine/internal/model"
	"vending-machine/internal/repository/coil"
	"vending-machine/internal/repository/journal"
	"vending-machine/internal/repository/server"
)

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	Create(ctx context.Context, slot int) (model.Transaction, error)
	Pay(ctx context.Context, txID string) (model.Transaction, error)
	Report(ctx context.Context, txID string) error
}

// Service is the singleton implementation. It depends only on repository
// *interfaces*, so every one is mockable in tests.
type Service struct {
	coil    coil.Repository
	journal journal.Repository
	server  server.Repository
	log     *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in cmd/main.
func New(c coil.Repository, j journal.Repository, s server.Repository, log *slog.Logger) *Service {
	return &Service{coil: c, journal: j, server: s, log: log}
}
```

Rules:

- A usecase's constructor takes **repository interfaces**, never concrete
  drivers. That single rule is what makes the whole layer unit-testable.
- A usecase **must not** import `net/http`, read `*http.Request`, or know about
  JSON wire shapes. It speaks `model` types and `error`. Decoding/encoding is
  delivery's job.
- Business errors are **`model`/`common` error values** (e.g. `model.ErrSoldOut`),
  so delivery can map them to a status code without string-matching.
- If one usecase needs another, depend on the **other usecase's interface**,
  not its struct.

---

## 5. Delivery layer — convention

Delivery is the **web surface**. One subpackage per transport. Handlers are
**thin**: decode → call usecase → encode. No business logic.

```
internal/delivery/http/
├── router.go        # the mux: every route in one place (your "find the HTTP API" map)
├── purchase.go      # handlers for the purchase story
├── catalog.go
├── request.go       # shared decode helpers
└── response.go      # shared writeJSON / writeError helpers
```

### 5.1 The router — the single source of "what endpoints exist"

```go
// Handler maps every HTTP route to a handler. This file is the answer to
// "which requests do we accept, with what method and path?".
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/transactions",            h.createTransaction)
	mux.HandleFunc("POST /api/transactions/{id}/pay",   h.payTransaction)
	mux.HandleFunc("GET  /api/catalog",                 h.getCatalog)
	// ...
	return h.middleware(mux)
}
```

### 5.2 A handler — thin

```go
// createTransaction: POST /api/transactions  {"slot": <int>}
func (h *Handler) createTransaction(w http.ResponseWriter, r *http.Request) {
	var req struct{ Slot int `json:"slot"` }   // the accepted request shape lives next to the handler
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Permintaan tidak terbaca.")
		return
	}
	tx, err := h.purchase.Create(r.Context(), req.Slot) // all logic is behind this call
	if err != nil {
		writeError(w, statusFor(err), codeFor(err), msgFor(err))
		return
	}
	writeJSON(w, http.StatusOK, tx)
}
```

Rules:

- The **request/response DTO structs live in the delivery file**, next to the
  handler. That is where someone looks to answer "what parameters does this
  endpoint accept?".
- `Handler` holds usecase **interfaces**, injected via `New`. So a delivery test
  passes a mock usecase and asserts on status codes / JSON without any real
  repository.
- Map errors to HTTP in one helper (`statusFor`/`codeFor`); never leak a raw
  `err.Error()` string as the contract.
- Static assets, middleware, auth header forwarding — all delivery concerns,
  stay here.

---

## 6. common & model — convention

- **`internal/model`** — pure data. `Transaction`, `Slot`, `SlotMap`,
  `VendResult`, the status/reason constants, and sentinel errors
  (`var ErrSoldOut = errors.New(...)`). No I/O, no internal imports. Everything
  else may import it freely; it keeps layers from importing each other just to
  share a type.
- **`internal/common`** — cross-cutting utilities with a subpackage each:
  `common/config`, `common/logx`, `common/httpx` (shared JSON/error writers if
  you prefer them central). A `common` package must not import `usecase`,
  `delivery`, or a concrete `repository` driver — if it wants to, it belongs in a
  layer, not in common.

---

## 7. Dependency injection & the singleton rule

Every package exposes **one exported implementation struct** and a `New(...)`
constructor that returns `*T`. There is no global state and no `init()` wiring.
The **only** place real implementations are constructed is `cmd/vending-machine/main.go`
— the composition root:

```go
// cmd/vending-machine/main.go  (the only file that imports concrete drivers)
coilDriver := serial.New(cfg.SerialPort, cfg.Slots)      // or sim.New(...)
serverRepo := server.New(cfg.ServerURL, cfg.MachineKey)
journalRepo := journal.New(filepath.Join(cfg.DataDir, "journal"))

purchaseUC := purchase.New(coilDriver, serverRepo, journalRepo, log)
catalogUC  := catalog.New(serverRepo, log)

httpHandler := http.New(purchaseUC, catalogUC, log)      // delivery gets usecase ports
server.ListenAndServe(cfg.Listen, httpHandler.Routes())
```

Everything below `main` receives its dependencies as interface parameters. That
is the "singleton" — constructed once, shared by reference, swappable in tests.

Keep wiring readable: if `main` grows unwieldy, extract a `wire()` function (or
adopt `google/wire`) — but the principle stays: **one place builds the graph.**

---

## 8. Testing & mocking — convention

The whole point of the layering is that **each layer is tested in isolation with
the next layer mocked.**

- **Interfaces are mocked, structs are not.** Because every boundary is an
  interface, every layer has something to inject.
- **Generate mocks** with `go.uber.org/mock` (`mockgen`). Put the
  `//go:generate` directive on the port file; mocks land in a `mock/`
  subpackage: `repository/coil/mock`, `usecase/purchase/mock`. Run
  `go generate ./...`.
- Hand-written fakes are acceptable for simple ports — you already do this well
  (`scriptedBoard`, `fakeServer` in `agent_test.go`). Keep them in `_test.go`.
- **What each layer's tests prove:**
  - *repository* — against the real dependency where feasible (a temp dir for
    `journal`, an `httptest.Server` for `server`, the `sim` driver for `coil`).
  - *usecase* — pure logic with **mock repositories**; assert orchestration
    (e.g. "coil turned exactly once", "journal written before vend"). No HTTP,
    no network.
  - *delivery* — with a **mock usecase** and `httptest`; assert status codes,
    JSON bodies, and request decoding. No repositories.
- Target: usecase and delivery packages at **high coverage** (they're pure once
  mocked); repository drivers covered by their integration-style tests.

```go
func TestPurchase_Create_turnsCoilOnce(t *testing.T) {
	coil := mock.NewMockRepository(ctrl)     // mock the port
	coil.EXPECT().Vend(3).Return(model.VendResult{OK: true}).Times(1)
	uc := purchase.New(coil, journalMock, serverMock, log)

	_, err := uc.Pay(ctx, "tx_123")

	require.NoError(t, err)   // mockgen verifies Vend was called exactly once
}
```

---

## 9. Naming & file conventions

- **Package name = directory name**, lowercase, no underscores: `purchase`,
  `coil`, `mysql`.
- **Port file = package name**: `coil/coil.go`, `purchase/purchase.go`,
  `user/repository.go`. Interface inside is `Repository` or `UseCase`.
- **Adapter file = tech name**: `mysql/mysql.go`, `serial/serial.go`.
- One **sub-flow per file** inside a usecase/delivery package (`create.go`,
  `pay.go`, `report.go`) — mirrors the user story's steps and keeps files small.
- **Constructor is always `New`**; returns `*T` and (optionally) `error`.
- Assert conformance with `var _ Port = (*Impl)(nil)` in every adapter.
- Mocks live in a `mock/` subpackage of the port, package name `mock`.

---

## 10. Where things go — quick decision guide

| You're writing… | It goes in… |
|---|---|
| A new HTTP endpoint | `delivery/http/<story>.go` + route in `router.go` |
| The accepted request parameters for an endpoint | a DTO struct next to that handler |
| "When X happens, do Y then Z" business logic | `usecase/<story>/` |
| Code that reads/writes a DB, the server, a device, a file | `repository/<thing>/<tech>/` |
| The interface that logic depends on | the **port** at `repository/<thing>/<thing>.go` or `usecase/<story>/<story>.go` |
| A plain struct shared by layers | `model/` |
| Config parsing, logging, generic helpers | `common/<x>/` |
| Constructing real drivers and starting the server | `cmd/vending-machine/main.go` |

---

## 11. Target layout for this repo

```
internal/
├── model/                      # Transaction, Slot, SlotMap, VendResult, errors, status constants
├── common/
│   ├── config/                 # ← internal/config
│   ├── logx/                   # ← internal/logx
│   └── httpx/                  # shared JSON/error response helpers (optional)
├── repository/
│   ├── coil/                   # ← internal/board  (port: coil.Repository)
│   │   ├── coil.go
│   │   ├── serial/             # the CH340 driver
│   │   └── sim/                # the simulator
│   ├── server/                 # ← internal/upstream  (port: server.Repository)
│   │   ├── server.go
│   │   └── http/               # the real HTTP client
│   └── journal/                # ← internal/journal  (port: journal.Repository)
│       ├── journal.go
│       └── file/               # the on-disk implementation
├── usecase/
│   ├── purchase/               # ← the vend/pay/report logic now inside internal/agent
│   ├── catalog/                # ← catalog cache logic
│   └── operator/               # ← operator login / restock
└── delivery/
    └── http/                   # ← the http.Handler now inside internal/agent
        ├── router.go
        ├── purchase.go
        ├── catalog.go
        └── operator.go
cmd/vending-machine/main.go     # composition root (already is one — keep it)
internal/winsvc/                # platform glue; invoked from cmd, stays as-is
web/                            # embedded kiosk assets, served by delivery/http
```

### Migration note

`internal/agent` is today a well-built *monolith of two layers*: HTTP handlers
(**delivery**) plus purchase/catalog orchestration (**usecase**), over three
repositories it already treats through interfaces (`board.Board` is a port!).
Migration is mechanical, not a rewrite:

1. Extract `model` first (pull the shared structs/constants out of `board`,
   `agent`, `upstream`). Nothing depends inward on anything yet — safe.
2. Rename `board`→`repository/coil`, `upstream`→`repository/server`,
   `journal`→`repository/journal`; introduce the port interface at each root and
   move the concrete type into a `serial/`/`http/`/`file/` subpackage.
3. Split `agent/*.go`: handler halves → `delivery/http`, orchestration halves →
   `usecase/<story>`. The existing `Options`-based constructor already injects
   dependencies — reuse that wiring.
4. Keep `agent_test.go`'s fakes; they become the usecase layer's mocks.

Do it one slice at a time (start with `catalog` — it's read-only), keeping the
build green between steps.
