-- 029_peer_profile_not_null: tracker hardening after the dev seeding run.
-- 1. peers.country / region / masked_peer_id were nullable; a NULL made every peer lookup
--    that scans them into plain strings fail (auto hide was silently off for such reporters).
--    Backfill to '' and make them NOT NULL DEFAULT '' like display_name and city.
-- 2. board_reports.resolution_note records why a report was resolved without a platform
--    peer ("auto hidden" when the target auto hid after enough reports).

UPDATE peers SET country = '' WHERE country IS NULL;
UPDATE peers SET region = '' WHERE region IS NULL;
UPDATE peers SET masked_peer_id = '' WHERE masked_peer_id IS NULL;

ALTER TABLE peers ALTER COLUMN country SET DEFAULT '';
ALTER TABLE peers ALTER COLUMN country SET NOT NULL;
ALTER TABLE peers ALTER COLUMN region SET DEFAULT '';
ALTER TABLE peers ALTER COLUMN region SET NOT NULL;
ALTER TABLE peers ALTER COLUMN masked_peer_id SET DEFAULT '';
ALTER TABLE peers ALTER COLUMN masked_peer_id SET NOT NULL;

ALTER TABLE board_reports ADD COLUMN IF NOT EXISTS resolution_note TEXT NOT NULL DEFAULT '';
