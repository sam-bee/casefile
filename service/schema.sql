CREATE TABLE IF NOT EXISTS projects (
    id BIGSERIAL PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO projects (slug, name, description)
VALUES ('local-research', 'Local research', 'Local research and examples')
ON CONFLICT (slug) DO NOTHING;

CREATE TABLE IF NOT EXISTS casenotes (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES projects(id),
    title TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'observation',
    status TEXT NOT NULL DEFAULT 'needs-work',
    tags TEXT[] NOT NULL DEFAULT '{}',
    fields JSONB NOT NULL DEFAULT '{}',
    author TEXT NOT NULL,
    source_revision TEXT NOT NULL DEFAULT '',
    version BIGINT NOT NULL DEFAULT 1,
    search_dirty BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT casenotes_summary_length_check CHECK (char_length(summary) <= 1400)
);

ALTER TABLE casenotes ADD COLUMN IF NOT EXISTS project_id BIGINT REFERENCES projects(id);
ALTER TABLE casenotes ADD COLUMN IF NOT EXISTS summary TEXT NOT NULL DEFAULT '';
ALTER TABLE casenotes ADD COLUMN IF NOT EXISTS fields JSONB NOT NULL DEFAULT '{}';
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'casenotes'::regclass AND conname = 'casenotes_summary_length_check'
    ) THEN
        ALTER TABLE casenotes ADD CONSTRAINT casenotes_summary_length_check
            CHECK (char_length(summary) <= 1400);
    END IF;
END $$;
UPDATE casenotes SET project_id = (SELECT id FROM projects WHERE slug = 'local-research'),
    search_dirty = TRUE WHERE project_id IS NULL;
ALTER TABLE casenotes ALTER COLUMN project_id SET NOT NULL;

CREATE TABLE IF NOT EXISTS casenote_comments (
    id BIGSERIAL PRIMARY KEY,
    casenote_id BIGINT NOT NULL REFERENCES casenotes(id) ON DELETE CASCADE,
    author TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS casenote_links (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES projects(id),
    from_casenote_id BIGINT NOT NULL REFERENCES casenotes(id) ON DELETE CASCADE,
    to_casenote_id BIGINT NOT NULL REFERENCES casenotes(id) ON DELETE CASCADE,
    relation TEXT NOT NULL,
    weight DOUBLE PRECISION NOT NULL CHECK (weight >= 0 AND weight <= 1),
    note TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT no_self_casenote_link CHECK (from_casenote_id <> to_casenote_id),
    CONSTRAINT unique_casenote_link UNIQUE (from_casenote_id, to_casenote_id, relation)
);

ALTER TABLE casenote_links ADD COLUMN IF NOT EXISTS project_id BIGINT REFERENCES projects(id);
UPDATE casenote_links AS l SET project_id = n.project_id FROM casenotes AS n
    WHERE l.from_casenote_id = n.id AND l.project_id IS NULL;
ALTER TABLE casenote_links ALTER COLUMN project_id SET NOT NULL;

CREATE TABLE IF NOT EXISTS casenote_activity (
    id BIGSERIAL PRIMARY KEY,
    casenote_id BIGINT NOT NULL REFERENCES casenotes(id) ON DELETE CASCADE,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    summary TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}',
    origin_key TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO casenote_activity (casenote_id, actor, action, summary, details, origin_key)
SELECT endpoint.casenote_id, 'casefile-migration (system/system)', 'link_weight_migrated',
    'Legacy link assigned neutral weight 0.5 because strength was not assessed',
    jsonb_build_object('link_id', l.id, 'weight', 0.5, 'migration_default', true),
    'link-weight-migration-' || l.id || '-' || endpoint.direction
FROM casenote_links AS l
CROSS JOIN LATERAL (VALUES (l.from_casenote_id, 'from'), (l.to_casenote_id, 'to'))
    AS endpoint(casenote_id, direction)
WHERE l.weight IS NULL ON CONFLICT (origin_key) DO NOTHING;

UPDATE casenotes SET version=version+1, search_dirty=TRUE, updated_at=now()
WHERE id IN (
    SELECT from_casenote_id FROM casenote_links WHERE weight IS NULL
    UNION SELECT to_casenote_id FROM casenote_links WHERE weight IS NULL
);
UPDATE casenote_links SET weight=0.5 WHERE weight IS NULL;
ALTER TABLE casenote_links ALTER COLUMN weight SET NOT NULL;

UPDATE casenotes SET author = author || ' (legacy/unknown)'
    WHERE author !~ '^[^()]+ \([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\)$';
UPDATE casenote_comments SET author = author || ' (legacy/unknown)'
    WHERE author !~ '^[^()]+ \([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\)$';
UPDATE casenote_links SET author = author || ' (legacy/unknown)'
    WHERE author !~ '^[^()]+ \([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\)$';

UPDATE casenote_activity SET origin_key = 'casenote-' || casenote_id
    WHERE action = 'created' AND origin_key IS NULL;
UPDATE casenote_activity SET origin_key = 'comment-' || (details->>'comment_id')
    WHERE action = 'commented' AND origin_key IS NULL AND details ? 'comment_id';
UPDATE casenote_activity SET origin_key = 'link-' || (details->>'link_id') ||
    CASE WHEN details->>'direction' = 'incoming' THEN '-to' ELSE '-from' END
    WHERE action = 'linked' AND origin_key IS NULL AND details ? 'link_id';

INSERT INTO casenote_activity (casenote_id, actor, action, summary, details, origin_key, created_at)
SELECT n.id, n.author, 'created', 'Casenote created before activity tracking',
    '{"backfilled":true}'::jsonb, 'casenote-' || n.id, n.created_at
FROM casenotes AS n WHERE true ON CONFLICT (origin_key) DO NOTHING;

INSERT INTO casenote_activity (casenote_id, actor, action, summary, details, origin_key, created_at)
SELECT c.casenote_id, c.author, 'commented', 'Comment added before activity tracking',
    jsonb_build_object('comment_id', c.id, 'backfilled', true), 'comment-' || c.id, c.created_at
FROM casenote_comments AS c WHERE true ON CONFLICT (origin_key) DO NOTHING;

INSERT INTO casenote_activity (casenote_id, actor, action, summary, details, origin_key, created_at)
SELECT l.from_casenote_id, l.author, 'linked', 'Linked casenote #' || l.to_casenote_id || ' (' || l.relation || ')',
    jsonb_build_object('link_id', l.id, 'other_casenote_id', l.to_casenote_id, 'relation', l.relation, 'weight', l.weight, 'backfilled', true),
    'link-' || l.id || '-from', l.created_at
FROM casenote_links AS l WHERE true ON CONFLICT (origin_key) DO NOTHING;

INSERT INTO casenote_activity (casenote_id, actor, action, summary, details, origin_key, created_at)
SELECT l.to_casenote_id, l.author, 'linked', 'Linked from casenote #' || l.from_casenote_id || ' (' || l.relation || ')',
    jsonb_build_object('link_id', l.id, 'other_casenote_id', l.from_casenote_id, 'relation', l.relation, 'weight', l.weight, 'backfilled', true),
    'link-' || l.id || '-to', l.created_at
FROM casenote_links AS l WHERE true ON CONFLICT (origin_key) DO NOTHING;

CREATE INDEX IF NOT EXISTS casenotes_tags_idx ON casenotes USING GIN (tags);
CREATE INDEX IF NOT EXISTS casenotes_updated_idx ON casenotes (updated_at DESC);
CREATE INDEX IF NOT EXISTS casenotes_project_updated_idx ON casenotes (project_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS casenotes_search_dirty_idx ON casenotes (id) WHERE search_dirty;
CREATE INDEX IF NOT EXISTS casenote_comments_idx ON casenote_comments (casenote_id, created_at);
CREATE INDEX IF NOT EXISTS casenote_links_from_idx ON casenote_links (from_casenote_id);
CREATE INDEX IF NOT EXISTS casenote_links_to_idx ON casenote_links (to_casenote_id);
CREATE INDEX IF NOT EXISTS casenote_links_project_idx ON casenote_links (project_id);
CREATE INDEX IF NOT EXISTS casenote_activity_note_idx ON casenote_activity (casenote_id, created_at DESC, id DESC);
