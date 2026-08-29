-- Adds the uniqueness get-or-create depends on: its non-transactional
-- read-then-insert let concurrent callers double-insert the same guild/channel.
-- These unique indexes enforce the invariant and serve the jsonb-equality lookup.

-- +goose Up
-- +goose StatementBegin

-- Collapse any duplicates created before the constraint existed, keeping the
-- lowest id and repointing children at it.
UPDATE "destination" d
SET instance_id = keep.min_id
FROM (
    SELECT MIN(id) AS min_id, platform_enum, instance_meta
    FROM "instance"
    GROUP BY platform_enum, instance_meta
) keep
WHERE d.instance_id <> keep.min_id
  AND EXISTS (
      SELECT 1 FROM "instance" i
      WHERE i.id = d.instance_id
        AND i.platform_enum = keep.platform_enum
        AND i.instance_meta = keep.instance_meta
  );

DELETE FROM "instance" i
WHERE i.id <> (
    SELECT MIN(j.id) FROM "instance" j
    WHERE j.platform_enum = i.platform_enum AND j.instance_meta = i.instance_meta
);

UPDATE "reminder" r
SET destination_id = keep.min_id
FROM (
    SELECT MIN(id) AS min_id, instance_id, destination_meta
    FROM "destination"
    GROUP BY instance_id, destination_meta
) keep
WHERE r.destination_id <> keep.min_id
  AND EXISTS (
      SELECT 1 FROM "destination" d
      WHERE d.id = r.destination_id
        AND d.instance_id = keep.instance_id
        AND d.destination_meta = keep.destination_meta
  );

DELETE FROM "destination" d
WHERE d.id <> (
    SELECT MIN(e.id) FROM "destination" e
    WHERE e.instance_id = d.instance_id AND e.destination_meta = d.destination_meta
);

CREATE UNIQUE INDEX uq_instance_platform_meta ON "instance" (platform_enum, instance_meta);
CREATE UNIQUE INDEX uq_destination_instance_meta ON "destination" (instance_id, destination_meta);

-- Superseded by the unique indexes above, which have the same leading column.
DROP INDEX IF EXISTS idx_instance_platform_enum;
DROP INDEX IF EXISTS idx_destination_instance;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

CREATE INDEX idx_instance_platform_enum ON "instance" (platform_enum);
CREATE INDEX idx_destination_instance ON "destination" (instance_id);

DROP INDEX IF EXISTS uq_destination_instance_meta;
DROP INDEX IF EXISTS uq_instance_platform_meta;

-- +goose StatementEnd
