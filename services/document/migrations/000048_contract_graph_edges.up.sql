-- ADR 0099 — contract intelligence graph.
--
-- One edge table, tenant-scoped via RLS. The four edge types from §18 F4
-- are encoded as the three directional types below (the playbook's
-- "references-by" is just `references` traversed in reverse — see the
-- API handler's recursive CTE which walks both directions).

CREATE TABLE IF NOT EXISTS contract_graph_edges (
    tenant_id    uuid        NOT NULL REFERENCES organizations(id),
    id           uuid        NOT NULL DEFAULT gen_random_uuid(),
    src_document uuid        NOT NULL,
    dst_document uuid        NOT NULL,
    edge_type    text        NOT NULL,
    confidence   real        NOT NULL DEFAULT 1.0,                      -- 1.0 = manual; <1.0 = extractor-suggested
    metadata     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_by   uuid,                                                  -- nullable: extractor-created edges have no user
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT contract_graph_edges_self_loop_check
        CHECK (src_document <> dst_document),
    CONSTRAINT contract_graph_edges_type_check
        CHECK (edge_type IN ('amends', 'supersedes', 'references'))
);

-- Both directions of traversal need their own index. Without these the
-- recursive-CTE walker degrades to seq-scan once the graph passes a
-- few thousand edges.
CREATE INDEX IF NOT EXISTS idx_contract_edges_src
    ON contract_graph_edges (tenant_id, src_document);
CREATE INDEX IF NOT EXISTS idx_contract_edges_dst
    ON contract_graph_edges (tenant_id, dst_document);

-- Unique (src, dst, type) so the extractor can re-run idempotently
-- via UPSERT without creating duplicate edges.
CREATE UNIQUE INDEX IF NOT EXISTS uq_contract_edges_triplet
    ON contract_graph_edges (tenant_id, src_document, dst_document, edge_type);

ALTER TABLE contract_graph_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract_graph_edges FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_graph_edges_tenant_isolation ON contract_graph_edges
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));
