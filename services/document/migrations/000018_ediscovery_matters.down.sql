-- Reverse of 000018_ediscovery_matters.
--
-- ⚠ Production rollback caveat: ediscovery_exports rows are
-- chain-of-custody audit records. Dropping the table loses the
-- "what did we ship to outside counsel and when" trail. Snapshot
-- before running.

DROP TABLE IF EXISTS ediscovery_exports;
DROP TABLE IF EXISTS ediscovery_custodians;
DROP TABLE IF EXISTS ediscovery_matters;
