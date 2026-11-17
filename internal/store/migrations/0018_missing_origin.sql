-- Schema version 18: why missing content is searched. 'missing' (the
-- manager's wanted/missing list, under the missing_search permission) or
-- 'pack' (an episode a cleared partial pack did not contain: its first
-- targeted search runs under the partial_pack permission, SPEC §8).
ALTER TABLE missing_content ADD COLUMN origin TEXT NOT NULL DEFAULT 'missing';
ALTER TABLE missing_content ADD COLUMN origin_intent INTEGER;
