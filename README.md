# commentlint

A Go comment linter built on one rule:

> **A comment may only talk about the scope it is attached to.**

Package doc talks about the package. A function doc states that function's
contract. A body comment explains *that statement*. The moment a comment
reaches upward — describing the pipeline, the project, the architecture — it is
README content that has leaked into source, where nothing will ever prompt
anyone to update it.

Inside a function body the stronger rule applies: explain **why**, not what.
The code already says what it does.

## Levels

| Level | Legitimate subject |
|---|---|
| package | what this package is for |
| exported decl | the contract: inputs, outputs, invariants, edge behaviour |
| unexported decl | why this exists as a separate thing |
| field | what this field means, valid values |
| body | why this statement is like this and not the obvious form |

## Rules

| Rule | Tier | What it catches |
|---|---|---|
| `commented-code` | error | commented-out code |
| `restatement` | error | `// Merge merges...` — doc adds nothing beyond the name |
| `no-removal` | error | `Why(workaround)` with no removal condition |
| `untyped` | opt-in | body comment without a `Why:`/`Ref:` prefix (`-require-prefix`) |
| `too-long` | warning | length as a proxy for reaching upward |
| `scope-reach` | ranking | comment names identifiers not in scope here |

## Usage

    commentlint ./...                  # fail on findings
    commentlint -rank -top 40 ./...    # worst-first worklist, exit 0
    commentlint -v ./...               # per-rule counts and corpus stats
    commentlint -rules restatement,commented-code ./...
    commentlint -require-prefix ./...  # enforce Why:/Ref: in bodies

Tuning: `-max-body-lines` (3), `-max-decl-lines` (5), `-reach-ratio` (0.5).

## How scope-reach works

Two passes. The first builds an identifier vocabulary from every function in
the corpus and computes IDF per token, so "rare" means rare *in this repo*, not
in English. The second scores each comment: tokens that are unambiguously
code-shaped (camelCase, snake_case, ALLCAPS, backticked) and rare enough to
carry information are checked against the identifiers in scope at that
position. A high out-of-scope fraction means the comment is explaining
something that lives elsewhere.

Deliberate limits:

- **Bare lowercase English is never evidence.** On a large Go corpus nearly
  every common word ("show", "first", "back") also appears inside some
  identifier, so vocabulary membership alone proves nothing. Requiring
  code-shaped tokens cut false positives on a 4300-function corpus from 38% of
  all comments to 6.7%.
- **Below 60 functions, scope-reach stays silent.** IDF cannot separate rare
  identifiers from ordinary words on a small corpus.
- **Domain acronyms still trip it.** `ACL`, `API`, `JWT` are shared vocabulary,
  not scope reaches. A per-repo allowlist is the obvious fix.

Because of that last point scope-reach is a **ranking signal**, not a CI gate.
It orders a cleanup sweep; it should not fail a build.

## Deliberately not done

No embeddings or LLM scoring. A CI finding must be deterministic and
actionable from its message alone — "your comment scored 0.61" is neither.
