-- Hourly event counts for the security and volume reports (RPT-02, RPT-03).
-- dlm writes them from OpenSearch; see LLD-001 §6.5.

-- Set when a sealed partition's hours have been rolled up for the last time.
-- A partition moves to COLD only after this, because COLD has no index to
-- aggregate.
ALTER TABLE data_partitions ADD COLUMN gold_final_at timestamptz;

CREATE SCHEMA obs_gold;

CREATE TABLE obs_gold.fact_events_hourly (
    hour_start  timestamptz NOT NULL,
    source      varchar(20) NOT NULL,
    category    varchar(20) NOT NULL,
    event_count bigint      NOT NULL,
    error_count bigint      NOT NULL, -- severity ERROR or CRITICAL
    bytes_total bigint      NOT NULL,
    PRIMARY KEY (hour_start, source, category)
);

-- Anomaly flag (FR-07, LLD-001 §6.5): an hour is flagged when its count is
-- at least three standard deviations from the previous 24 hours of the same
-- source and category. The score is null until there are 12 earlier hours.
-- Hours with no events have no row, so a source that goes silent is not
-- flagged.
CREATE VIEW obs_gold.events_hourly_scored AS
SELECT hour_start, source, category, event_count, error_count, bytes_total,
       round(mu, 1) AS baseline,
       CASE WHEN n >= 12 THEN round(((event_count - mu) / greatest(sigma, 1))::numeric, 2) END AS anomaly_score,
       (n >= 12 AND abs(event_count - mu) / greatest(sigma, 1) >= 3
                AND greatest(event_count, mu) >= 100) AS is_anomaly
  FROM (SELECT f.*,
               avg(event_count)         OVER w AS mu,
               stddev_samp(event_count) OVER w AS sigma,
               count(*)                 OVER w AS n
          FROM obs_gold.fact_events_hourly f
        WINDOW w AS (PARTITION BY source, category ORDER BY hour_start
                     ROWS BETWEEN 24 PRECEDING AND 1 PRECEDING)) scored;
