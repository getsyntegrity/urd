-- Old offsets belong to Unscoped only. Never infer a tenant from a name.
ALTER TABLE offsets_store ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE offsets_store DROP CONSTRAINT IF EXISTS offsets_store_pkey;
ALTER TABLE offsets_store ADD PRIMARY KEY (tenant_id, projection_name, shard_number);
CREATE INDEX IF NOT EXISTS idx_offsets_store_scope_name ON offsets_store (tenant_id, projection_name);
