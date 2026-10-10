# Sutra Mini

Reliable accounting push for job-work receipts. A receipt and its voucher are written in one Postgres transaction. A worker pushes pending vouchers to a mock accounting API that fails on purpose.

## Setup

Install Go 1.25 or newer and Node 20 or newer. pgx 5.11 needs Go 1.25. Create a Neon Postgres database and copy the direct connection string (connection pooling off). The host must not contain `-pooler`.

Windows:

```powershell
copy .env.example .env
```

Mac or Linux:

```bash
cp .env.example .env
```

Put the Neon connection string in `.env`. It must include `sslmode=require`:

```text
DATABASE_URL=postgresql://user:password@host/neondb?sslmode=require
MOCK_URL=http://127.0.0.1:8090
MOCK_FAIL_RATE=0.2
MOCK_HANG_MS=5000
MAX_ATTEMPTS=4
LEASE_MS=12000
```

`MOCK_FAIL_RATE=1` makes every accounting call fail, which is useful for the recording. Set it back to `0.2` afterwards. `MOCK_HANG_MS` should stay under 8000, because the worker gives up on a call after 8 seconds. `LEASE_MS` should stay above 8000 so a second worker does not take a voucher while the first call is still running.

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

Windows:

```powershell
curl.exe -X POST http://localhost:8080/jobwork/receipts -H "Content-Type: application/json" -H "Idempotency-Key: demo-1" -d "{\"issue_id\":1,\"qty\":2,\"received_on\":\"2026-10-10\"}"
```

Mac or Linux:

```bash
curl -X POST http://localhost:8080/jobwork/receipts \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: demo-1" \
  -d '{"issue_id":1,"qty":2,"received_on":"2026-10-10"}'
```

The header is optional. If you leave it out, the API creates a key for that request.

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

The receipt tests prove two things: ten concurrent receipts cannot pass the issued quantity, and eight requests with one idempotency key create one receipt and one voucher. The same key with a different quantity is rejected. A second worker that finds a voucher whose accounting reference was already stored marks it pushed with that original reference.

The overshoot test fails if the issue-row lock, the guarded quantity update, and the `received_qty <= qty` check are all removed. The idempotency test fails if the unique key and the second key check are both removed. Either layer on its own can still pass, so the tests are aimed at the whole guarantee.
