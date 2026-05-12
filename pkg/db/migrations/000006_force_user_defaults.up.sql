-- Migration 000006: Force defaults for users table
-- This migration ensures that even if columns existed but lacked defaults,
-- they are now correctly configured to prevent NOT NULL constraint violations.

ALTER TABLE users ALTER COLUMN created_at SET DEFAULT NOW();
ALTER TABLE users ALTER COLUMN last_login SET DEFAULT NOW();
ALTER TABLE users ALTER COLUMN rating SET DEFAULT 1500;
ALTER TABLE users ALTER COLUMN rd SET DEFAULT 350;
ALTER TABLE users ALTER COLUMN volatility SET DEFAULT 0.06;
ALTER TABLE users ALTER COLUMN elo SET DEFAULT 1200;
ALTER TABLE users ALTER COLUMN display_name SET DEFAULT '';
ALTER TABLE users ALTER COLUMN avatar_url SET DEFAULT '';
ALTER TABLE users ALTER COLUMN country SET DEFAULT '';
ALTER TABLE users ALTER COLUMN bio SET DEFAULT '';
ALTER TABLE users ALTER COLUMN is_premium SET DEFAULT FALSE;
