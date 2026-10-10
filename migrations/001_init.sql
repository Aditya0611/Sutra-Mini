-- Sutra Mini schema.
-- received_qty is the running total on the issue. The check constraint
-- rejects a receipt that would pass the issued quantity.
-- lease_until and next_attempt_at let two workers claim pending vouchers
-- without adding a status beyond pending, pushed, and failed.

CREATE TABLE skus (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code text NOT NULL UNIQUE,
    name text NOT NULL,
    image_url text NOT NULL
);

CREATE TABLE parties (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    phone text NOT NULL
);

CREATE TABLE jobwork_issues (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    party_id bigint NOT NULL REFERENCES parties (id),
    sku_id bigint NOT NULL REFERENCES skus (id),
    qty integer NOT NULL CHECK (qty > 0),
    received_qty integer NOT NULL DEFAULT 0 CHECK (received_qty >= 0),
    issued_on date NOT NULL,
    CONSTRAINT jobwork_issues_received_within_issued CHECK (received_qty <= qty)
);

CREATE TABLE jobwork_receipts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id bigint NOT NULL REFERENCES jobwork_issues (id),
    qty integer NOT NULL CHECK (qty > 0),
    received_on date NOT NULL,
    idempotency_key text NOT NULL UNIQUE
);

CREATE TABLE vouchers (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type text NOT NULL,
    source_id bigint NOT NULL REFERENCES jobwork_receipts (id),
    amount_paise bigint NOT NULL CHECK (amount_paise >= 0),
    status text NOT NULL CHECK (status IN ('pending', 'pushed', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error text,
    client_ref text NOT NULL UNIQUE,
    external_ref text,
    next_attempt_at timestamptz,
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (type, source_id)
);

CREATE INDEX vouchers_claimable_idx
    ON vouchers (created_at)
    WHERE status = 'pending';

CREATE INDEX jobwork_receipts_issue_id_idx
    ON jobwork_receipts (issue_id);

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER vouchers_set_updated_at
    BEFORE UPDATE ON vouchers
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- 5 SKUs, 2 job-workers, 4 open issues. received_qty starts at 0.
INSERT INTO skus (code, name, image_url) VALUES
    ('SKU-001', 'Red Banarasi saree', 'https://placehold.co/400x400?text=SKU-001'),
    ('SKU-002', 'Green anarkali', 'https://placehold.co/400x400?text=SKU-002'),
    ('SKU-003', 'Navy lehenga', 'https://placehold.co/400x400?text=SKU-003'),
    ('SKU-004', 'Ivory kurta set', 'https://placehold.co/400x400?text=SKU-004'),
    ('SKU-005', 'Maroon dupatta', 'https://placehold.co/400x400?text=SKU-005');

INSERT INTO parties (name, phone) VALUES
    ('Ramesh Embroidery', '9810000001'),
    ('Sita Stitching', '9810000002');

INSERT INTO jobwork_issues (party_id, sku_id, qty, issued_on) VALUES
    (1, 1, 10, DATE '2026-10-01'),
    (1, 2, 6, DATE '2026-10-03'),
    (2, 3, 4, DATE '2026-10-05'),
    (2, 5, 20, DATE '2026-10-07');
