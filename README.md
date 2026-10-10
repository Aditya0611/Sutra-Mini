# Sutra Mini

Reliable accounting push for job-work receipts. A receipt and its voucher are written in one Postgres transaction. A worker pushes pending vouchers to a mock accounting API that fails on purpose.

## Setup

Install Go 1.22 or newer and Node 20 or newer. Create a Neon Postgres database and copy the direct connection string (connection pooling off). The host must not contain `-pooler`.

```powershell
copy .env.example .env
```

Put the connection string in `.env`:

```text
DATABASE_URL=postgresql://...
MOCK_URL=http://127.0.0.1:8090
```

`.env` is gitignored. Apply the schema once, from the repo root:

```powershell
go run ./cmd/migrate
```

This creates 5 SKUs, 2 job-workers, and 4 open issues. If the tables already exist, the command leaves them alone.

## Run

Use four terminals, from the repo root except the web app:

```powershell
go run ./cmd/mockacct
```

```powershell
go run ./cmd/api
```

```powershell
go run ./cmd/worker
```

```powershell
cd web
npm install
npm run dev
```

Open http://localhost:3000/sync. The API is on port 8080 and the mock is on port 8090.

Record a receipt:

```powershell
curl -X POST http://localhost:8080/jobwork/receipts -H "Content-Type: application/json" -H "Idempotency-Key: demo-1" -d "{\"issue_id\":1,\"qty\":2,\"received_on\":\"2026-10-10\"}"
```

The first call returns 201. The same key again returns 200 and the same receipt. A receipt that would pass the issued quantity returns 409.

`GET /issues/1` returns issued, received, and outstanding. `GET /vouchers?status=pending` lists vouchers. `POST /vouchers/{id}/retry` queues a failed voucher again.

Voucher amount is quantity times 10000 paise (₹100 per piece). A SKU has no price in the brief.

## Worker

The worker claims a pending voucher with `FOR UPDATE SKIP LOCKED` and a 12 second lease, so two processes can run together. Each push has its own 8 second timeout, which is longer than the mock's 5 second hang, and other vouchers keep moving during that hang.

The mock answers `POST /api/vouchers` like this:

- the same `client_ref` returns HTTP 409 with the original `external_ref`
- otherwise 20% return HTTP 500
- 10% wait 5 seconds, then succeed
- the rest succeed immediately

A 500 is not remembered. A success is remembered before the 5 second wait, so a worker killed during the wait can post the same `client_ref` again and store the original reference.

A failed attempt is retried after 250ms, 500ms, 1s, and then the fourth failure marks the voucher `failed` with the reason. Retry on the sync page clears that and sets it back to `pending`.

## Tests

```powershell
go test ./...
```

The receipt tests prove two things: ten concurrent receipts cannot pass the issued quantity, and eight requests with one idempotency key create one receipt and one voucher.
