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
| `duplication` | opt-in | the same fact explained in more than one comment |
| `doclink` | opt-in | a `[Bracketed.Reference]` that resolves to nothing |
| `param-contract` | opt-in | a precondition asserted about a primitively-typed parameter |
| `nil-contract` | opt-in | nil behaviour written as ad-hoc prose |

## Usage

    commentlint ./...                  # fail on findings
    commentlint -rank -top 40 ./...    # worst-first worklist, exit 0
    commentlint -v ./...               # per-rule counts and corpus stats
    commentlint -rules restatement,commented-code ./...
    commentlint -require-prefix ./...  # enforce Why:/Ref: in bodies
    commentlint -rules duplication ./...        # facts explained more than once
    commentlint -rules doclink ./...            # dead godoc links
    commentlint -rules param-contract ./...     # invariants that want a type
    commentlint -rules nil-contract ./...       # nil contracts to standardize

Tuning: `-max-body-lines` (3), `-max-decl-lines` (5), `-reach-ratio` (0.5),
`-dup-overlap` (0.30), `-dup-max-sites` (8), `-dup-same-name`.

## How duplication works

Every other rule judges a comment against its own scope. This one judges it
against the rest of the corpus, because the failure it targets is invisible
locally: each copy of a duplicated fact looks fine where it sits.

Comments are split into paragraphs, normalized (lowercased, doc links and
punctuation dropped, so a re-linked or re-punctuated copy still matches), and
reduced to 6-word shingles. A shingle index makes the comparison near-linear
rather than O(n²) on the ~10k comments a mid-sized repo carries. A paragraph
sharing ≥30% of its shingles with another comment is reported, and transitive
groups collapse into one finding listing every site.

Two things make it worth acting on:

- **It prints both texts.** You judge the finding from the output; you do not
  open two files to find out whether it is real. That is what lets a rule with
  imperfect precision still be useful.
- **The remedy is concrete.** "Hoist this to the type both cite" is an action.
  "This comment is too long" is not.

Ranking is by duplicated **volume** (shared shingle count), not by fraction.
Fraction promotes small twin helpers whose entire doc is two shared sentences;
volume promotes the cases where hoisting the fact deletes the most text.

Suppressed by default: identically-named declarations. Backends implementing a
shared interface legitimately repeat the contract — every `store.Store` saying
"returns store.ErrNotFound if the entity does not exist" is correct, not
copy-paste. Pass `-dup-same-name` to see them anyway.

## doclink — dead godoc links

Go degrades an unresolvable doc link **silently**. `go/doc/comment` keeps it as
plain text, so pkg.go.dev renders the literal characters `[Set.Enforce]`,
brackets and all. Verified against `go vet`, `staticcheck` and golangci-lint's
`godoclint` on a deliberately broken link: all three report zero. The failure is
invisible precisely because rendering treats it as fine.

The check inverts the stdlib's own resolver rather than reimplementing symbol
lookup. `comment.Parser` only emits a `DocLink` when `LookupSym` accepts the
reference; anything it rejects survives as `Plain`. So: parse once WITH a
resolver built from the package's declarations, then look for bracketed text
that came back plain. Scoping, receivers and package qualification stay the
stdlib's problem.

Three failure shapes, each with its own message:

- **missing symbol** — nothing declares it.
- **bare method** — `[Method]` where Go requires `[Recv.Method]`. The most
  common shape by far; the finding names the qualified form to use.
- **pluralized** — `[Option]s`. A link must END at the bracket, so the
  trailing "s" stops it rendering.

Not reported: builtins (`[any]`, `[string]`), lowercase single words (prose and
markdown-style placeholders, not package names), and references to packages the
file does not import. That last exclusion is deliberate — they are usually
cross-references to a package that *cannot* be imported without a cycle, e.g.
`[entitymanager.Manager]` named from the `acl` package it calls into. Naming the
collaborator is the point; the author is not claiming the link renders.

## Contract rules

A contract comment is an invariant the compiler is not checking. Writing it
down is the weakest available enforcement, but the right remedy splits in two,
so these are two rules rather than one.

### param-contract — the invariant wants a type

Fires when a doc asserts a precondition ("MUST already have passed
containedPath", "id pre-validated", "must already be escaped") about a
parameter whose type is a bare `string`/`int`/`[]byte`. Those types carry no
invariant, so the requirement is enforced by prose and code review alone.

The check is conservative by construction: the assertion must name a parameter
that actually exists on the function, and that parameter must have a type with
no room for the invariant. A precondition about an already-structured type is
not actionable — the type is there and the comment is explaining it. On a
9.7k-comment corpus this yields 5 findings, all true positives, concentrated in
security-adjacent code (a credentials path, an app-id lookup, an ACL-gated
export). Low volume is the point: each one is worth a type.

This is not a new idea imposed on a codebase — it is a codebase's own idea,
applied unevenly. The corpus this was built against already keeps
`principal.Principal`'s `roles`/`orgID` unexported so they can only enter
through a verifying constructor. `param-contract` finds the places that did not
get the treatment their neighbours did.

### nil-contract — the invariant cannot want a type

Go has no non-nullable pointer, so nil-ness is the one contract category a type
genuinely cannot fix; a wrapper here is worse than the comment. The remedy is a
convention instead:

    Nil: rejected — <why>
    Nil: accepted — <why>
    Nil: never returned

A tag machines can read, then free prose, because the *why* varies per call
site and is the part worth keeping. The corpus had eleven different phrasings
("non-nil", "nil when", "may be nil", "nil disables", ...) for what are really
three facts: nil is rejected, nil is accepted with a meaning, or nil is never
returned.

Bare `non-nil` is deliberately NOT matched. It is overwhelmingly used to draw
the nil-vs-empty-slice distinction, which describes decoding behaviour rather
than stating a caller contract; matching it produced 93 false positives.

Functions whose only result is `error` are skipped: "returns nil if the config
is valid" there is Go's universal error idiom, not a nil contract. This is a
small population (2 of 46 `returns nil if/when` comments in the corpus) but a
pure false-positive class. Everything else matched by that phrasing returns a
pointer, map or interface, where nil genuinely is a documented outcome.

The rule converges: a comment carrying a `Nil:` tag is skipped, so fixing one
removes it permanently rather than reformatting it forever.

### Why not length

`too-long` was the original rule for "this comment is explaining the system."
It does not work. On a 9.7k-comment corpus it fired 1221 times with a mode of
exactly one line over the threshold, and its top-ranked finding was a comment
whose length was the least interesting thing about it. Worse, the highest-value
comment in that corpus — 28 lines documenting a *verified* sandbox-escape
limitation, on a one-line function — is the single worst doc:body ratio in the
repo. Any length or ratio heuristic flags it, and is wrong every time.

Length is a symptom. What actually distinguishes a comment that should be cut
is that some of it is re-derivable from somewhere else. Measure that instead.

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

## Suppressing false positives

Every rule here is a heuristic over prose, so false positives are a permanent
fact rather than a bug to be fixed once. Two escape hatches, deliberately
different in blast radius.

**Inline** — preferred for a one-off judgement. It sits where the reader is,
travels with the code, and is reviewed in the diff that adds it:

```go
func storeCredentials(repoPath string) error { //commentlint:ignore param-contract  repoPath is contained by Clone
```

A bare `//commentlint:ignore` suppresses every rule on that declaration; naming
rules narrows it. Text after the rule list is a reason, encouraged and unparsed.
The directive goes on the declaration line, not inside the comment, so
suppressing a finding never edits the prose being judged.

**`.commentlint.yml`** — for policy that would otherwise be repeated at dozens
of sites. Searched for upward from the scan root:

```yaml
exclude:
  - "internal/legacy/**"     # ** matches any number of segments
  - testdata                 # a bare name matches any path segment
rules:
  too-long: false            # off everywhere
  nil-contract: true
ignore:
  nil-contract:
    - "internal/store/**"    # rule off for these paths only
allow-phrases:
  - "non-nil empty slice"    # a house idiom a rule keeps misreading
settings:
  max-body-lines: 6
  dup-overlap: 0.35
```

`-config <path>` points at a specific file; `-no-config` ignores any. An
explicit `-rules` flag always beats the file, so a developer can run one rule
without touching shared config.

For `duplication`, suppressing *either* site drops the pair — the finding names
two places, and silencing one is a statement that the pairing is not a defect.

## Deliberately not done

No embeddings or LLM scoring. A CI finding must be deterministic and
actionable from its message alone — "your comment scored 0.61" is neither.
