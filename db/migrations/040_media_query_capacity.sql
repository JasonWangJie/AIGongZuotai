-- Keep the original channel key, adaptive limit, cooldown, probe and version.
ALTER TABLE studio_media_channel_runtime
    DROP CONSTRAINT studio_media_channel_runtime_effective_limit_check,
    ADD CONSTRAINT studio_media_channel_runtime_effective_limit_check
    CHECK (effective_limit BETWEEN 1 AND CASE WHEN lane = 'query' THEN 1536 ELSE 32 END);
