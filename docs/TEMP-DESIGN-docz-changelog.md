---
id: DESIGN-XXXX
title: "Changelog config block and ParseChangelog"
status: Draft
author: Donald Gifford
created: 2026-08-02
---

<!-- markdownlint-disable-file MD025 MD041 -->

<!-- TEMPORARY / PORTABLE: this doc lives in docz-api only as a handoff
     artifact (INV-0005). Recreate it in the docz repo with
     `docz create design "Changelog config block and ParseChangelog"`,
     paste the body, let docz assign the real number, then delete this
     file from docz-api. -->

# DESIGN XXXX: Changelog config block and ParseChangelog

**Status:** Draft **Author:** Donald Gifford **Date:** 2026-08-02

## Overview

docz gains first-class awareness of a repo's changelog: an opt-in `changelog:`
block in `.docz.yaml` naming the file, and a byte-based `ParseChangelog` that
parses the fleet-standard git-cliff / Keep-a-Changelog shape into structured
version sections. Consumers are docz-api (fetches, caches, and serves the
changelog per repo; later builds per-document backlinks on the parsed sections)
and, through it, the docz-site.

Requirements were derived in docz-api's INV-0005 ("Changelog as a first-class
docz artifact"); all of its open questions are resolved and this design encodes
the answers.

## Goals and Non-Goals

### Goals

- Add a `changelog:` config block: `enabled` (default **false**) and `file`
  (default **`CHANGELOG.md`**), with defaults surfaced via `DefaultConfig()` and
  per-key merge behavior identical to the rest of the config.
- Add `ParseChangelog([]byte)` — byte-based, no disk I/O (the `ParseFrontmatter`
  precedent) — parsing the fleet-standard shape into versions → groups → items.
- Keep the surface contract-pinnable: docz-api will freeze the config defaults
  and parser behavior in its `internal/doczcontract` (clause R6) on the pin
  bump.
- Ship both in **one additive minor release** — a second release costs a second
  fleet-wide pin dance for no benefit.

### Non-Goals

- No CLI subcommand (`docz changelog …`) — nothing in the CLI consumes the block
  yet; `docz config` printing the resolved block falls out for free.
- No changelog _generation_ — git-cliff owns that. docz only locates and parses.
- No wiki/mkdocs nav integration (possible follow-up).
- No commit→file mapping — that is docz-api "feature 2" (per-document backlinks)
  and depends on data outside the changelog file (GitHub compare API or PR
  joins), designed separately in docz-api.

## Background

- The fleet generates every changelog with **git-cliff** from a shared
  `cliff.toml`, **conventional commits**, and **SemVer** tags, so the input
  shape is uniform:

  ```markdown
  # Changelog

  <preamble prose>

  ## [unreleased]

  ### Bug Fixes

  - _(chart)_ Scope the main Service selector … ([#12](…))

  ## [0.4.2] - 2026-07-23

  ### Bug Fixes

  - _(ci)_ Drop stale goreleaser GPG signing … ([#10](…))
  ```

  Version headers are `## [semver] - YYYY-MM-DD` (plus `## [unreleased]`),
  groups are `### Title` from the shared cliff groups, and each bullet is one
  commit with optional `*(scope)*` and PR links. **No commit SHAs or file paths
  appear in the body** (that fact scoped feature 2 out).

- **Fleet file locations** (drives the `file` semantics): either `CHANGELOG.md`
  at the repo root, or `charts/<chart-name>/CHANGELOG.md` for helm-chart
  changelogs. Those are the only shapes in use; subpath support exists precisely
  for the chart case.

- **Rollout tolerance (verified in INV-0005 F2):** docz v1.0.0's yaml.v3-based
  `Load` silently ignores the unknown `changelog:` key, so repos can add the
  block before this ships with zero breakage. Preserve that property (see
  Testing).

- docz-api today already fetches a hardcoded root `CHANGELOG.md` and caches it
  raw; this design is what lets it become config-driven and eventually
  structured.

## Detailed Design

### Config

```go
// ChangelogConfig maps the changelog: block of .docz.yaml.
type ChangelogConfig struct {
    // Enabled opts the repo into changelog mapping. Default false.
    Enabled bool `yaml:"enabled"`
    // File is the changelog path relative to the repo root. Subpaths
    // are allowed (charts/<name>/CHANGELOG.md). Default "CHANGELOG.md".
    File string `yaml:"file"`
}
```

- `Config` gains `Changelog ChangelogConfig \`yaml:"changelog"\``.
- `DefaultConfig()` returns `{Enabled: false, File: "CHANGELOG.md"}`.
- **Partial-block merge:** `changelog: {enabled: true}` with no `file` must
  resolve `File` to the default — apply defaults before unmarshal (or normalize
  empty→default after), consistent with how the other blocks handle partial
  overrides. An empty `file:` value is the default, never "".
- **Validation** (in `Load`'s existing validation pass): `file` must be a clean
  relative path — reject absolute paths, `..` traversal, and trailing `/`;
  normalize a leading `./`. Consumers fetch this path out of a git tree, so
  cleanliness is the contract.

### Parser

Home: alongside `ParseFrontmatter` in the doc package (docz-api imports it as
`doczdoc` and gets both through one import), unless the package's cohesion
argues for a sibling package — implementer's call (OQ-A below).

```go
// Changelog is a parsed git-cliff / Keep-a-Changelog document.
type Changelog struct {
    // Preamble is the markdown before the first version heading
    // (title + prose), verbatim.
    Preamble string
    // Versions in document order (git-cliff emits newest first).
    Versions []ChangelogVersion
}

type ChangelogVersion struct {
    // Version is the bare version string: brackets stripped, a single
    // leading "v" trimmed ("0.4.2"), or "unreleased".
    Version    string
    Unreleased bool
    // Date is the raw "YYYY-MM-DD" from the heading; empty for
    // unreleased. Kept as a string — consumers parse if they need to.
    Date   string
    Groups []ChangelogGroup
}

type ChangelogGroup struct {
    Title string   // e.g. "Bug Fixes"
    Items []string // raw markdown bullet bodies, one per commit
}

// ErrNoVersions is returned when the input has no version headings —
// the file is not a changelog (or is empty). Callers decide skip vs
// fail, mirroring ErrNoFrontmatter.
var ErrNoVersions = errors.New(...)

func ParseChangelog(raw []byte) (*Changelog, error)
```

Parsing rules:

- A version heading is a line matching `## [<ver>]` or `## [<ver>] - <date>`; a
  `<ver>` of `unreleased` (case-insensitive) sets `Unreleased: true`.
- `### <title>` inside a version opens a group; bullet lines (`- …`, including
  continuation lines indented under the bullet) append to the open group's
  `Items` with the leading `-` marker and its following space stripped and the
  raw markdown preserved (scope markers, PR links untouched).
- Content inside a version but before any `###` heading is collected into a
  group with `Title: ""` (does not occur in the fleet shape, but the parser must
  not lose it).
- **Never panic on arbitrary input**; error only via `ErrNoVersions` (wrapped).
  Non-conforming markdown parses best-effort.
- **Stable version identity is the load-bearing contract**: the bare semver
  string is the key docz-api's feature 2 (backlinks) will join on. Normalization
  (bracket strip + `v` trim) must never change meaning.

## API / Interface Changes

All additive (one minor release, e.g. `v1.1.0`):

- `config`: `ChangelogConfig`, `Config.Changelog`, default values, validation.
- `doc` (or sibling): `Changelog`, `ChangelogVersion`, `ChangelogGroup`,
  `ErrNoVersions`, `ParseChangelog`.
- No changes to existing symbols; no breaking changes.

### Downstream contract (what docz-api pins as R6)

On its pin bump docz-api will add contract tests asserting:

- yaml keys `changelog.enabled` / `changelog.file`; defaults `false` /
  `"CHANGELOG.md"`; partial-block merge keeps the file default.
- Unknown-key tolerance of `Load` (the INV-0005 F2 rollout guarantee) stays
  intact.
- `ParseChangelog` over the fleet fixture: version order and identity, group
  titles, item counts, preamble capture; `ErrNoVersions` identity via
  `errors.Is` on a no-heading input.

Treat those as frozen once released — changes to any of them are breaking for
consumers regardless of Go-API compatibility.

## Data Model

None — docz holds no state. (docz-api maps the parsed shape onto its own store
when it builds structured serving; not this repo's concern.)

## Testing Strategy

Table-driven with golden fixtures:

- **Fleet-standard fixture** — a real git-cliff output (docz-api's
  `CHANGELOG.md` is representative): preamble + unreleased + several released
  versions, scopes, PR links.
- **Chart changelog fixture** — the `charts/<name>/CHANGELOG.md` shape (same
  format; exercises nothing special in the parser but pins the convention).
- **Edge fixtures** — empty input, prose-only input (both `ErrNoVersions`);
  unreleased-only; a version with no groups; bullets before any group heading;
  multi-line bullet continuation.
- **Config table** — absent block (defaults), partial block (`enabled` only),
  full block, invalid `file` values (absolute, `..`, trailing slash) rejected,
  unknown sibling keys still tolerated.

## Migration / Rollout Plan

1. Ship config + parser in one additive minor release.
2. Repos may add the `changelog:` block **before or after** the release (v1.0.0
   already tolerates it; the block is dormant until consumers act on it).
3. docz-api bumps its pin per its INV-0001 procedure, adds contract clause R6,
   then builds its feature 1 (config-driven fetch + raw serve endpoint) and
   later feature 2 (backlinks over parsed sections).
4. No migration inside docz — the block is opt-in and default-off.

## Open Questions

- **OQ-A — parser package home:** in the existing doc package next to
  `ParseFrontmatter` (single import for consumers — lean this way) or a sibling
  package?
- **OQ-B — `Date` type:** raw string (as specced — no tz/format opinions) or
  validated/parsed at parse time? String recommended; revisit only if a second
  consumer needs `time.Time`.
- **OQ-C — preamble fidelity:** verbatim capture (as specced) or trimmed?
  Verbatim recommended — consumers render it.

## References

- docz-api `INV-0005` — Changelog as a first-class docz artifact (the
  requirements source; all OQs answered `a`)
- docz-api `DESIGN-0003` / `IMPL-0003` — repo index endpoint (the
  repo-level-artifact consumption pattern feature 1 copies)
- docz-api `internal/doczcontract` — the contract-test harness that will pin
  this surface (clause R6)
- Keep a Changelog 1.1.0; git-cliff + the fleet-shared `cliff.toml`
