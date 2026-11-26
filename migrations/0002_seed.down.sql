DELETE FROM tier_policies WHERE policy_name IN ('default', 'demo');
DELETE FROM log_sources WHERE source_id IN (1, 2);
