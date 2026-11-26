INSERT INTO log_sources (source_id, source_name, format) VALUES
    (1, 'nslkdd', 'csv'),
    (2, 'cloudtrail', 'json');

-- Policy values from LLD section 7.2. "demo" moves data in hours instead of days.
INSERT INTO tier_policies
    (policy_name, bucket_size, hot_after, warm_after, retain_for, late_window, purge_grace, is_active)
VALUES
    ('default', '1 day',  '7 days', '30 days', '365 days', '2 hours',   '1 hour',    true),
    ('demo',    '1 hour', '1 hour', '3 hours', '24 hours', '2 minutes', '5 minutes', false);
