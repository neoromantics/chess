-- Down migration for 000004
-- We only drop the columns we know we added here.

ALTER TABLE users 
  DROP COLUMN IF EXISTS display_name,
  DROP COLUMN IF EXISTS avatar_url,
  DROP COLUMN IF EXISTS country,
  DROP COLUMN IF EXISTS is_premium,
  DROP COLUMN IF EXISTS elo,
  DROP COLUMN IF EXISTS bio,
  DROP COLUMN IF EXISTS last_login;

ALTER TABLE games
  DROP COLUMN IF EXISTS white_think_time,
  DROP COLUMN IF EXISTS black_think_time,
  DROP COLUMN IF EXISTS assessments;
