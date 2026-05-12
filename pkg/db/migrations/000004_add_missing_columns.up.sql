-- Migration 000004: Add missing columns and fix defaults
-- Ensure all required columns exist and have proper defaults for existing databases.

ALTER TABLE users 
  ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS avatar_url TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS country TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS is_premium BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS elo INTEGER NOT NULL DEFAULT 1200,
  ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS last_login TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Force defaults for existing columns to ensure they never violate NOT NULL constraints.
-- This is necessary for existing installations where columns might have been added
-- manually without defaults.
ALTER TABLE users ALTER COLUMN last_login SET DEFAULT NOW();
ALTER TABLE users ALTER COLUMN created_at SET DEFAULT NOW();

ALTER TABLE games
  ADD COLUMN IF NOT EXISTS white_think_time INTEGER NOT NULL DEFAULT 1000,
  ADD COLUMN IF NOT EXISTS black_think_time INTEGER NOT NULL DEFAULT 1000,
  ADD COLUMN IF NOT EXISTS assessments TEXT NOT NULL DEFAULT '[]';
