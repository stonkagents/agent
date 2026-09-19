-- 019_launch_image_thumb: drop the thumbnail column.
ALTER TABLE token_launches DROP COLUMN IF EXISTS image_thumb_url;
