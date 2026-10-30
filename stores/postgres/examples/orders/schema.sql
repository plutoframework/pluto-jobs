-- Apply with the outbox schema through application migration tooling.
CREATE TABLE orders (
    tenant_id text NOT NULL,
    id text NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
