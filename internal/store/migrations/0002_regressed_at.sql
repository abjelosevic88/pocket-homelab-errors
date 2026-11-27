-- When an issue last came back after it was resolved, so a day can say
-- which issues came back on it (NULL = never, or before this column).
ALTER TABLE issues ADD COLUMN regressed_at TEXT;
