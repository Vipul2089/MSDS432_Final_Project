-- Storage price per tier, used by the storage and cost dashboard (RPT-04).
-- Approximate Google Cloud list prices for us-central1, November 2026.
-- Check https://cloud.google.com/pricing before quoting them.
CREATE TABLE tier_prices (
    tier             varchar(4)    PRIMARY KEY CHECK (tier IN ('HOT', 'WARM', 'COLD')),
    usd_per_gb_month numeric(8,4)  NOT NULL CHECK (usd_per_gb_month >= 0),
    product          text          NOT NULL
);

INSERT INTO tier_prices (tier, usd_per_gb_month, product) VALUES
    ('HOT',  0.1700, 'SSD persistent disk (pd-ssd)'),
    ('WARM', 0.0400, 'Standard persistent disk (pd-standard)'),
    ('COLD', 0.0100, 'Cloud Storage Nearline');
