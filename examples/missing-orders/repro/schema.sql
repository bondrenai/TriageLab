-- The columns of orders that the finance sync reads. Customer details removed.
CREATE TABLE orders (
    id          INTEGER PRIMARY KEY,
    created_at  TEXT    NOT NULL,  -- UTC, microsecond precision
    channel     TEXT    NOT NULL,  -- portal, rep, or edi
    account     TEXT    NOT NULL,
    total_cents INTEGER NOT NULL
);

CREATE INDEX orders_created_at_idx ON orders (created_at);
