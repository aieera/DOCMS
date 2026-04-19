# Mutation testing

Wave 13.5. go-mutesting runs against three security-critical
packages and fails CI if the mutation score drops below **70%**.

## What mutation testing actually catches

Regular coverage tells you which lines ran. Mutation testing
tells you which lines *matter*. go-mutesting flips one operator
at a time (`==` → `!=`, `<` → `<=`, boolean negations, arithmetic
swaps) and re-runs the package's tests. A well-tested package
catches each mutant with at least one test failure; a weakly-
tested package has mutants that pass every test — meaning the
logic has no actual verification.

## Scope

| Package | Why it's in scope |
|---|---|
| `services/auth/internal/service/...` | Login, MFA, session management. Silent regressions here are CVE-shaped. |
| `services/policy/internal/opa/...` | OPA evaluation. A flipped `allow` / `deny` is a data breach. |
| `services/storage/internal/service/...` | Envelope encryption (DEK unwrap, KEK derivation). A flipped `!=` in a tag compare is game over. |

The rest of the monorepo is *not* mutation-tested. It's
expensive (each package takes 5–20 min), and the value drops
sharply once you leave the security surface.

## Running locally

```bash
bash scripts/mutesting/run.sh
# or target one package:
go install github.com/avito-tech/go-mutesting/cmd/go-mutesting@latest
go-mutesting --exec-timeout-in-seconds=60 ./services/auth/internal/service/...
```

The threshold is 70% by default, override with
`THRESHOLD=80 bash scripts/mutesting/run.sh`.

## CI behaviour

The `mutation` job in `.github/workflows/ci.yml` runs:

- On every push to `main` and any `release/*` branch.
- On a PR that carries the `mutation-test` label (manual opt-in).

It doesn't run on every PR because it's slow (30–60 min) and most
changes don't touch the three packages.

## When it fails

Two possible responses:

1. **Add tests.** The go-mutesting report names each surviving
   mutant and its source location. Follow each one back to the
   weakly-tested line, write a test that distinguishes the
   pre- and post-mutation behaviour.
2. **Update the threshold with reviewer approval.** If the score
   drops because a new branch is hard to test in isolation
   (e.g. timing-dependent), document the reason in the PR and
   bump `THRESHOLD` in the CI job. Needs a security reviewer on
   the PR.

Silent merges with a lowered threshold and no explanation get
reverted.

## Baseline (to be published after first run)

Spec §13.5 DoD: "Baseline score published; scorecard panel in
observability dashboard." The baseline belongs in
`docs/security/mutation-baseline-YYYY-MM-DD.md` once the first
green run lands on `main` — an operator activity, not a
pre-merge artifact.
