-- Data-format compatibility fence: v0.5 writes immutable model identities,
-- capability/parameter snapshots and raw-request idempotency hashes into the
-- existing JSON columns. Older binaries would discard these fields on save.
-- No table rewrite is needed. Recording version 5 makes older migration
-- loaders reject this database instead of silently erasing accepted metadata.
SELECT 1;
