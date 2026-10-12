#!/usr/bin/env bash
# O-13 — migration safety gate for the hot tables.
#
# Fails when a migration added in the change range takes a long, blocking
# lock on a large table:
#
#   1. ALTER TABLE ... ADD a FOREIGN KEY / REFERENCES / CHECK constraint
#      without NOT VALID, where the altered table is hot or the FK
#      references a hot table. Adding the constraint validates every row
#      while holding a lock that blocks writes. Safe pattern:
#        migration N:   ALTER TABLE t ADD CONSTRAINT c ... NOT VALID;
#        migration N+1: ALTER TABLE t VALIDATE CONSTRAINT c;
#      Column constraints (ADD COLUMN x ... REFERENCES/CHECK) cannot be NOT
#      VALID, so add the column bare and the constraint separately.
#      VALIDATE CONSTRAINT must be in a later migration than the ADD:
#      golang-migrate runs a file as one transaction, so a VALIDATE in the
#      same file scans the table while the ADD's lock is still held (flagged).
#   2. A non-CONCURRENT index build on a hot table: CREATE [UNIQUE] INDEX
#      without CONCURRENTLY, or ADD UNIQUE / PRIMARY KEY / EXCLUDE without
#      USING INDEX. golang-migrate sends a file as one multi-statement query
#      (an implicit transaction), so CREATE INDEX CONCURRENTLY must be the
#      ONLY statement in its file. On the partitioned audit_events parent use
#      CREATE INDEX ... ON ONLY audit_events, then CONCURRENTLY per partition
#      and ALTER INDEX ... ATTACH PARTITION. ON ONLY is exempt only on the
#      partitioned audit_events parent; on any other hot table it blocks.
#   3. CREATE INDEX CONCURRENTLY in a file with any other statement (it
#      would fail at apply time: cannot run inside a transaction block).
#
# Hot tables: documents, document_versions, content_blobs, outbox, and
# audit_events including its partitions (audit_events_*).
#
# Usage:
#   scripts/check-migrations.sh [--base REF]   # migrations added/changed in REF...HEAD
#                                             # (default: $BASE_REF, else origin/main)
#   scripts/check-migrations.sh --files F...  # check the given .up.sql files
#   scripts/check-migrations.sh --self-test   # run the inline fixture suite
#
# Applied migrations are immutable; this gate exists for the next one.

set -euo pipefail

HOT_RE='^(DOCUMENTS|DOCUMENT_VERSIONS|CONTENT_BLOBS|OUTBOX|AUDIT_EVENTS|AUDIT_EVENTS_[A-Z0-9_]+)$'

# analyze FILE — prints one line per violation; exit status is not used.
analyze() {
    awk -v file="$1" -v hot_re="$HOT_RE" '
    # Strip -- comments line by line, join the file, then drop /* */ blocks.
    {
        line = $0
        sub(/--.*$/, "", line)
        sql = sql " " line
    }
    END {
        while ((s = index(sql, "/*")) > 0) {
            rest = substr(sql, s + 2)
            e = index(rest, "*/")
            if (e == 0) { sql = substr(sql, 1, s - 1); break }
            sql = substr(sql, 1, s - 1) " " substr(rest, e + 2)
        }
        sql = toupper(sql)
        gsub(/"/, "", sql)
        gsub(/[\t\r\n]+/, " ", sql)
        gsub(/ +/, " ", sql)

        n = split(sql, stmts, ";")
        # Tables this file creates are empty, so locks on them are cheap.
        # Only a plain CREATE TABLE counts: with IF NOT EXISTS the table may
        # already exist (and be large) on a migrated database.
        for (i = 1; i <= n; i++) {
            st = stmts[i]
            gsub(/^ +/, "", st)
            if (match(st, /^CREATE (UNLOGGED )?TABLE [^ (]+/) && st !~ /^CREATE (UNLOGGED )?TABLE IF NOT EXISTS /) {
                split(substr(st, RSTART, RLENGTH), cw, " ")
                created[tbl(cw[length(cw)])] = 1
            }
        }
        real = 0; concurrent = 0
        for (i = 1; i <= n; i++) {
            st = stmts[i]
            gsub(/^ +| +$/, "", st)
            if (st == "") continue
            real++
            if (st ~ /CREATE (UNIQUE )?INDEX CONCURRENTLY/) concurrent = 1
            check_index(st)
            check_alter(st)
        }
        # golang-migrate runs the whole file in one transaction, so the
        # SHARE ROW EXCLUSIVE lock of the ADD is still held during the VALIDATE scan.
        for (t in validated)
            if (t in constrained)
                report("VALIDATE CONSTRAINT in the same migration that adds a constraint locking hot table " tolower(constrained[t]) ": validate in a later migration", validated[t])
        if (concurrent && real > 1)
            report("CREATE INDEX CONCURRENTLY must be the only statement in its migration file (golang-migrate runs a multi-statement file as one transaction)", "")
    }

    function report(msg, st) {
        if (length(st) > 160) st = substr(st, 1, 157) "..."
        printf "%s: %s%s\n", file, msg, (st == "" ? "" : "\n    > " st)
    }

    # Normalises a table reference token: drops schema, trailing "(" etc.
    function tbl(tok) {
        sub(/^PUBLIC\./, "", tok)
        sub(/[^A-Z0-9_%].*$/, "", tok)
        return tok
    }

    function is_hot(t) { return t ~ hot_re && !(t in created) }

    # Table referenced by the first REFERENCES in s that targets a hot table.
    function hot_ref(s,    rest, p, t) {
        rest = s
        while ((p = match(rest, /REFERENCES [^ (]+/)) > 0) {
            t = tbl(substr(rest, RSTART + 11, RLENGTH - 11))
            if (is_hot(t)) return t
            rest = substr(rest, RSTART + RLENGTH)
        }
        return ""
    }

    function check_index(st,    rest, p, t, w, only) {
        if (match(st, /CREATE (UNIQUE )?INDEX /) == 0) return
        if (st ~ /CREATE (UNIQUE )?INDEX CONCURRENTLY/) return
        rest = substr(st, RSTART + RLENGTH)
        if ((p = index(rest, " ON ")) == 0 && substr(rest, 1, 3) != "ON ") return
        rest = (p == 0) ? substr(rest, 4) : substr(rest, p + 4)
        only = (rest ~ /^ONLY /)
        sub(/^ONLY /, "", rest)
        split(rest, w, " ")
        t = tbl(w[1])
        # ON ONLY is metadata-only on a partitioned parent (audit_events);
        # on a plain table it is an ordinary, blocking index build.
        if (only && t == "AUDIT_EVENTS") return
        if (is_hot(t))
            report("non-concurrent index on hot table " tolower(t) " (use CREATE INDEX CONCURRENTLY, alone in its file)", st)
    }

    # Splits the action list of an ALTER TABLE on top-level commas and checks
    # every ADD clause on its own, so one NOT VALID cannot cover a sibling.
    function check_alter(st,    p, rest, w, k, target, hot, acts, depth, c, cur, nc, j, cl, ref) {
        if ((p = match(st, /ALTER TABLE /)) == 0) return
        rest = substr(st, RSTART + RLENGTH)
        sub(/^IF EXISTS /, "", rest)
        sub(/^ONLY /, "", rest)
        split(rest, w, " ")
        target = tbl(w[1])
        hot = is_hot(target)
        rest = substr(rest, length(w[1]) + 2)

        nc = 0; depth = 0; cur = ""
        for (j = 1; j <= length(rest); j++) {
            c = substr(rest, j, 1)
            if (c == "(") depth++
            else if (c == ")") depth--
            if (c == "," && depth == 0) { acts[++nc] = cur; cur = ""; continue }
            cur = cur c
        }
        acts[++nc] = cur

        for (k = 1; k <= nc; k++) {
            cl = acts[k]
            gsub(/^ +| +$/, "", cl)
            if (cl ~ /^VALIDATE CONSTRAINT /) { validated[target] = st; continue }
            if (cl !~ /^ADD /) continue
            # Remember which hot table this ADD locks (the altered table, or
            # the one an FK references) for the same-file VALIDATE rule.
            if (cl ~ /^ADD (CONSTRAINT |FOREIGN KEY|CHECK|UNIQUE|PRIMARY KEY|EXCLUDE)/ || cl ~ /(REFERENCES |CHECK ?\()/) {
                ref = hot ? target : hot_ref(cl)
                if (ref != "") constrained[target] = ref
            }
            if (cl ~ /(FOREIGN KEY|REFERENCES |CHECK ?\()/ && cl !~ /NOT VALID/) {
                ref = hot_ref(cl)
                if (cl ~ /^ADD (COLUMN )?(IF NOT EXISTS )?[A-Z0-9_]+ / && cl !~ /^ADD CONSTRAINT / && cl !~ /^ADD (FOREIGN KEY|CHECK)/) {
                    if (hot || ref != "")
                        report("column constraint on " (hot ? "hot table " tolower(target) : "a reference to hot table " tolower(ref)) " cannot be NOT VALID: add the column bare, then ADD CONSTRAINT ... NOT VALID and VALIDATE in a later migration", st)
                } else if (hot) {
                    report("FOREIGN KEY/CHECK added to hot table " tolower(target) " without NOT VALID", st)
                } else if (ref != "") {
                    report("FOREIGN KEY referencing hot table " tolower(ref) " added without NOT VALID", st)
                }
            }
            if (hot && cl ~ /^ADD (CONSTRAINT [^ ]+ )?(UNIQUE|PRIMARY KEY|EXCLUDE)/ && cl !~ /USING INDEX/)
                report("UNIQUE/PRIMARY KEY/EXCLUDE on hot table " tolower(target) " builds a non-concurrent index (build it CONCURRENTLY, then ADD CONSTRAINT ... USING INDEX)", st)
        }
    }
    ' "$1"
}

check_files() {
    local f out bad=0
    for f in "$@"; do
        [ -f "$f" ] || continue
        out="$(analyze "$f")"
        if [ -n "$out" ]; then
            printf '%s\n' "$out"
            bad=1
        fi
    done
    return "$bad"
}

changed_migrations() {
    local base="$1"
    git diff --name-only --diff-filter=ACMR "${base}...HEAD" -- 'services/*/migrations/*.up.sql'
}

# ---------------------------------------------------------------------------
# Self-test: fixtures named pass_* must produce no violation, fail_* at least
# one. The last case exercises the git-range wiring in a throwaway repo.
# ---------------------------------------------------------------------------
self_test() {
    local dir rc=0 f out
    dir="$(mktemp -d)"
    # shellcheck disable=SC2064 # expand now: $dir is local
    trap "rm -rf '$dir'" EXIT

    fixture() { cat > "$dir/$1.up.sql"; }

    fixture fail_fk_documents <<'SQL'
ALTER TABLE documents ADD CONSTRAINT documents_owner_fk FOREIGN KEY (tenant_id, owner_id) REFERENCES users (tenant_id, id);
SQL
    fixture fail_check_outbox <<'SQL'
ALTER TABLE outbox
    ADD CONSTRAINT outbox_subject_check CHECK (subject <> '');
SQL
    fixture fail_quoted_schema_mixed_case <<'SQL'
alter table if exists "public"."content_blobs" add constraint cb_size_check check (size_bytes >= 0);
SQL
    fixture fail_fk_referencing_hot <<'SQL'
ALTER TABLE document_comments
    ADD CONSTRAINT document_comments_version_fk
    FOREIGN KEY (tenant_id, version_id) REFERENCES document_versions (tenant_id, id);
SQL
    fixture fail_column_references <<'SQL'
ALTER TABLE content_blobs ADD COLUMN parent_id uuid REFERENCES content_blobs (id);
SQL
    fixture fail_second_clause_valid <<'SQL'
ALTER TABLE documents
    ADD CONSTRAINT a_check CHECK (size > 0) NOT VALID,
    ADD CONSTRAINT b_check CHECK (version > 0);
SQL
    fixture fail_index_documents <<'SQL'
CREATE INDEX IF NOT EXISTS idx_documents_title ON documents (tenant_id, title);
SQL
    fixture fail_unique_index_partition <<'SQL'
CREATE UNIQUE INDEX idx_ae_2026_10_id ON audit_events_2026_10 USING btree (tenant_id, id);
SQL
    fixture fail_index_audit_parent <<'SQL'
CREATE INDEX idx_audit_events_kind ON audit_events (tenant_id, kind);
SQL
    fixture fail_unique_constraint <<'SQL'
ALTER TABLE document_versions ADD CONSTRAINT dv_number_uniq UNIQUE (tenant_id, document_id, version_number);
SQL
    fixture fail_concurrently_with_others <<'SQL'
CREATE INDEX CONCURRENTLY idx_documents_a ON documents (a);
CREATE INDEX CONCURRENTLY idx_documents_b ON documents (b);
SQL
    fixture fail_dynamic_sql <<'SQL'
DO $$
BEGIN
    EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id) ON DELETE CASCADE', t, c);
END $$;
SQL

    fixture fail_on_only_non_partitioned <<'SQL'
-- ON ONLY is a metadata-only build only on a partitioned parent; documents
-- is a plain table, so this is an ordinary blocking index build.
CREATE INDEX idx_documents_title ON ONLY documents (tenant_id, title);
SQL
    fixture fail_validate_same_file <<'SQL'
-- golang-migrate runs the file as one transaction: the ADD's lock is still
-- held while VALIDATE scans the table.
ALTER TABLE documents ADD CONSTRAINT documents_size_check CHECK (size_bytes >= 0) NOT VALID;
ALTER TABLE documents VALIDATE CONSTRAINT documents_size_check;
SQL
    fixture fail_validate_same_file_fk_into_hot <<'SQL'
ALTER TABLE clause_matches ADD CONSTRAINT cm_version_fk
    FOREIGN KEY (tenant_id, version_id) REFERENCES document_versions (tenant_id, id) NOT VALID;
ALTER TABLE clause_matches VALIDATE CONSTRAINT cm_version_fk;
SQL
    fixture pass_fk_not_valid <<'SQL'
-- Two-step FK: NOT VALID here, VALIDATE CONSTRAINT in the next migration.
ALTER TABLE documents
    ADD CONSTRAINT documents_owner_fk FOREIGN KEY (tenant_id, owner_id)
    REFERENCES users (tenant_id, id) NOT VALID;
SQL
    fixture pass_validate <<'SQL'
ALTER TABLE documents VALIDATE CONSTRAINT documents_owner_fk;
SQL
    fixture pass_check_not_valid <<'SQL'
ALTER TABLE outbox ADD CONSTRAINT outbox_subject_check CHECK (subject <> '') NOT VALID;
SQL
    fixture pass_concurrently_alone <<'SQL'
-- golang-migrate: alone in its file, so it runs outside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_documents_title ON documents (tenant_id, title);
SQL
    fixture pass_on_only_partitioned <<'SQL'
CREATE INDEX IF NOT EXISTS idx_audit_events_kind ON ONLY audit_events (tenant_id, kind);
SQL
    fixture pass_cold_tables <<'SQL'
CREATE INDEX idx_documents_archive_x ON documents_archive (x);
CREATE INDEX idx_outbox_dlq_subject ON outbox_dlq (subject);
ALTER TABLE document_tags ADD CONSTRAINT dt_tag_fk FOREIGN KEY (tag_id) REFERENCES tags (id);
ALTER TABLE document_tags ADD CONSTRAINT dt_len CHECK (length(label) < 64);
SQL
    fixture pass_new_table_fk <<'SQL'
CREATE TABLE document_pins (
    tenant_id   uuid NOT NULL,
    document_id uuid NOT NULL REFERENCES documents (id),
    CHECK (tenant_id IS NOT NULL),
    PRIMARY KEY (tenant_id, document_id)
);
CREATE INDEX idx_document_pins_doc ON document_pins (document_id);
SQL
    fixture pass_new_hot_table <<'SQL'
-- A new service's outbox: the table is created here, so it is empty.
CREATE TABLE outbox (
    tenant_id uuid NOT NULL,
    id        uuid NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX idx_outbox_unpublished ON outbox (created_at);
ALTER TABLE outbox ADD CONSTRAINT outbox_subject_check CHECK (subject <> '');
SQL
    fixture fail_if_not_exists_table_is_not_new <<'SQL'
-- IF NOT EXISTS: on a migrated database this is a no-op, the table is the
-- existing (large) one, so the index below still locks it.
CREATE TABLE IF NOT EXISTS outbox (tenant_id uuid NOT NULL, id uuid NOT NULL);
CREATE INDEX idx_outbox_unpublished ON outbox (created_at);
SQL
    fixture pass_plain_column_and_comments <<'SQL'
-- ALTER TABLE documents ADD CONSTRAINT x CHECK (a > 0);   (comment only)
/* CREATE INDEX idx ON documents (a); */
ALTER TABLE documents ADD COLUMN IF NOT EXISTS summary text;
ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_old_check;
SQL

    for f in "$dir"/*.up.sql; do
        out="$(analyze "$f")"
        case "$(basename "$f")" in
            fail_*)
                if [ -z "$out" ]; then
                    echo "SELF-TEST FAIL: expected a violation in $(basename "$f")"; rc=1
                else
                    echo "ok   (rejects) $(basename "$f")"
                fi ;;
            pass_*)
                if [ -n "$out" ]; then
                    echo "SELF-TEST FAIL: unexpected violation in $(basename "$f"):"; printf '%s\n' "$out"; rc=1
                else
                    echo "ok   (accepts) $(basename "$f")"
                fi ;;
        esac
    done

    # Range wiring: only migrations added after BASE are checked.
    local repo="$dir/repo" script
    script="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
    mkdir -p "$repo/services/document/migrations"
    cp "$dir/fail_index_documents.up.sql" "$repo/services/document/migrations/000001_old.up.sql"
    if (
        cd "$repo"
        git init -q -b main
        git -c user.email=t@t -c user.name=t add -A
        git -c user.email=t@t -c user.name=t commit -qm base
        git branch base
        cp "$dir/pass_fk_not_valid.up.sql" services/document/migrations/000002_ok.up.sql
        git -c user.email=t@t -c user.name=t add -A
        git -c user.email=t@t -c user.name=t commit -qm ok
        "$script" --base base >/dev/null
    ); then
        echo "ok   (range) pre-existing violation outside the range is ignored"
    else
        echo "SELF-TEST FAIL: range mode flagged a migration outside BASE...HEAD"; rc=1
    fi
    if (
        cd "$repo"
        cp "$dir/fail_fk_documents.up.sql" services/document/migrations/000003_bad.up.sql
        git -c user.email=t@t -c user.name=t add -A
        git -c user.email=t@t -c user.name=t commit -qm bad
        ! "$script" --base base >/dev/null
    ); then
        echo "ok   (range) violation added in BASE...HEAD fails the gate"
    else
        echo "SELF-TEST FAIL: range mode missed a violation added in BASE...HEAD"; rc=1
    fi

    # An unresolvable base (e.g. github.event.before after a force-push, or
    # the all-zero SHA of a new branch) must fail, not pass vacuously.
    if ( cd "$repo" && ! "$script" --base 0000000000000000000000000000000000000000 >/dev/null 2>&1 ); then
        echo "ok   (range) unresolvable base fails the gate"
    else
        echo "SELF-TEST FAIL: range mode passed with an unresolvable base"; rc=1
    fi

    if [ "$rc" -eq 0 ]; then echo "check-migrations self-test: PASS"; else echo "check-migrations self-test: FAIL"; fi
    return "$rc"
}

main() {
    local base="${BASE_REF:-origin/main}"
    case "${1:-}" in
        --self-test) self_test; return ;;
        --files) shift; check_files "$@"; return ;;
        --base) base="${2:?--base needs a ref}" ;;
        "") ;;
        -h|--help) sed -n '2,40p' "$0"; return 0 ;;
        *) echo "unknown argument: $1" >&2; return 2 ;;
    esac

    if ! git rev-parse --verify --quiet "${base}^{commit}" >/dev/null; then
        echo "check-migrations: FAIL — base '${base}' does not resolve to a commit (shallow clone, force-push or new branch?); refusing to pass without a range" >&2
        return 2
    fi
    local files=()
    mapfile -t files < <(changed_migrations "$base")
    if [ "${#files[@]}" -eq 0 ]; then
        echo "check-migrations: no migrations added in ${base}...HEAD"
        return 0
    fi
    echo "check-migrations: checking ${#files[@]} migration(s) added in ${base}...HEAD"
    if ! check_files "${files[@]}"; then
        echo "check-migrations: FAIL — unsafe lock on a hot table (see scripts/check-migrations.sh header for the safe patterns)"
        return 1
    fi
    echo "check-migrations: OK"
}

main "$@"
