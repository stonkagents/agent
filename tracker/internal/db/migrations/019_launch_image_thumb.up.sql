-- 019_launch_image_thumb: a small (≤ 128 px) pinned copy of the launch artwork so the portal
-- can list tokens without decoding every 512 px master. NULL for launches recorded before
-- the portal produced thumbnails; readers fall back to image_url.
ALTER TABLE token_launches ADD COLUMN IF NOT EXISTS image_thumb_url TEXT;
