-- Migration 000004: Add missing columns
-- In earlier versions of this project, columns were added directly to 
-- 000001_initial.up.sql instead of in a new migration. Existing databases 
-- will not have these columns because golang-migrate skips already-applied migrations.
-- This migration ensures all databases have the correct schema.

ALTER TABLE users 
  ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS avatar_url TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS country TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS is_premium BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS elo INTEGER NOT NULL DEFAULT 1200,
  ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS last_login TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE games
  ADD COLUMN IF NOT EXISTS white_think_time INTEGER NOT NULL DEFAULT 1000,
  ADD COLUMN IF NOT EXISTS black_think_time INTEGER NOT NULL DEFAULT 1000,
  ADD COLUMN IF NOT EXISTS assessments TEXT NOT NULL DEFAULT '[]';
