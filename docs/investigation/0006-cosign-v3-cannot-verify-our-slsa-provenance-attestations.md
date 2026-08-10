---
id: INV-0006
title: "cosign v3 cannot verify our SLSA provenance attestations"
status: Concluded
author: Donald Gifford
created: 2026-08-10
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0006: cosign v3 cannot verify our SLSA provenance attestations

**Status:** Concluded
**Author:** Donald Gifford
**Date:** 2026-08-10

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [F1 — The documented command fails on every published version](#f1--the-documented-command-fails-on-every-published-version)
  - [F2 — The provenance exists, is correct, and is signed by the right identity](#f2--the-provenance-exists-is-correct-and-is-signed-by-the-right-identity)
  - [F3 — Two attachment schemes coexist on the same digest](#f3--two-attachment-schemes-coexist-on-the-same-digest)
  - [F4 — cosign v2 verifies the same artifact with the same command](#f4--cosign-v2-verifies-the-same-artifact-with-the-same-command)
  - [F5 — GHCR does not serve the OCI referrers API](#f5--ghcr-does-not-serve-the-oci-referrers-api)
  - [F6 — Signatures are unaffected](#f6--signatures-are-unaffected)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [Open questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Question

`charts/docz-api/README.md` documents two verification commands for every
published chart and image: a cosign signature check and a SLSA provenance
check. The provenance one fails. **Is our SLSA provenance actually broken, or
is the documented command wrong?**

## Hypothesis

Initial suspicion (recorded before investigating, and **wrong** — see F2/F3):
the `SLSA provenance` jobs report success but the provenance is not being
attached to the digest that `cosign verify-attestation` reads, so the
attestation is effectively missing.

## Context

Found while verifying the freshly published chart `0.3.1` after IMPL-0005 /
PR #15. The signature check passed; the provenance check failed with a
certificate-identity mismatch. Nothing in the recent work touches the publish
pipeline, so the first job was to establish whether this was a regression or a
pre-existing condition.

This matters because the verification commands are **consumer-facing**: they
are in the chart README that operators (and the docz-site) follow, and the
README's "Both are signed with cosign keyless and ship SLSA Level 3 provenance
attestations" claim is a supply-chain promise.

**Triggered by:** post-merge verification of PR #15 (chart 0.3.1); publish
pipeline established in IMPL-0004 Phase 5.

## Approach

1. Run the README's two commands against chart `0.3.1`.
2. Repeat across older published versions to separate regression from
   pre-existing condition.
3. Inspect the registry directly (tag list, manifests, annotations) to find
   where the provenance actually lives.
4. Decode the attestation's signing certificate to read its true SAN and
   Fulcio OID extensions.
5. Re-run the failing command under an older cosign to isolate the variable.

## Environment

| Component | Version / Value |
|-----------|----------------|
| cosign (local, via `mise`) | `v3.1.1` (`mise.toml` pins `cosign = "latest"`) |
| cosign (comparison run) | `v2.6.1` (`mise x aqua:sigstore/cosign@2.6.1`) |
| SLSA generator | `slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@v2.1.0` |
| Signing step | `sigstore/cosign-installer` (pinned SHA) in `.github/workflows/ghcr.yml` |
| Registry | GHCR (`ghcr.io`) |
| Chart under test | `ghcr.io/donaldgifford/charts/docz-api:0.3.1`, digest `sha256:09015dab…7ba8c` |

## Findings

### F1 — The documented command fails on every published version

Not a regression. The signature check passes everywhere; the provenance check
fails everywhere, including versions published well before the current work:

| Chart version | `cosign verify` | `cosign verify-attestation --type slsaprovenance` |
|---------------|-----------------|---------------------------------------------------|
| 0.2.2 | OK | **FAIL** |
| 0.3.0 | OK | **FAIL** |
| 0.3.1 | OK | **FAIL** |

The error is a certificate-identity mismatch, not a missing-attestation error:

```text
Error: no matching attestations: failed to verify certificate identity:
no matching CertificateIdentity found, last error: expected SAN value to match
regex "^https://github.com/slsa-framework/slsa-github-generator/.+", got
"https://github.com/donaldgifford/docz-api/.github/workflows/ghcr.yml@refs/heads/main"
```

The image (`ghcr.io/donaldgifford/docz-api:0.5.0`) behaves identically, so this
is a property of the pipeline, not of the chart path.

### F2 — The provenance exists, is correct, and is signed by the right identity

This **refutes the hypothesis**. Fetching the legacy attestation tag
`sha256-<digest>.att` directly from the registry shows a real SLSA predicate:

```text
layer: application/vnd.dsse.envelope.v1+json
  predicateType = https://slsa.dev/provenance/v0.2
```

Decoding that layer's `dev.sigstore.cosign/certificate` annotation gives a SAN
that **matches the README's regex exactly**:

```text
X509v3 Subject Alternative Name: critical
  URI:https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@refs/tags/v2.1.0
```

with Fulcio extensions confirming the provenance is bound to this repo and
commit: `1.3.6.1.4.1.57264.1.5 = donaldgifford/docz-api`,
`…1.3 = 7c8cc85…` (the PR #15 merge commit),
`…1.18 = …/.github/workflows/release.yml@refs/heads/main`.

So the generator did its job. The artifact is real, correct, and correctly
signed.

### F3 — Two attachment schemes coexist on the same digest

The tag listing for the chart repository shows **two** attachments per digest —
and no `.sig` tag at all:

```text
0.3.1
sha256-09015dab…7ba8c        ← OCI image index, artifactType application/vnd.oci.empty.v1+json
sha256-09015dab…7ba8c.att    ← legacy cosign attestation tag (holds the SLSA provenance)
```

The bare `sha256-<digest>` tag is **cosign v3's referrers-fallback
attachment**; the `.att` tag is the **legacy (cosign v2) attestation tag**.

The split has a simple cause: the two halves of the pipeline run different
cosign majors. `ghcr.yml`'s own signing step uses current cosign (v3) and
writes the new-format attachment; the SLSA generator pins `v2.1.0`, which
bundles cosign v2 and writes the legacy `.att` tag.

cosign v3 reads the new-format attachment, finds only the **signature** there
(signed by `ghcr.yml`, hence the confusing SAN in the F1 error), and never
falls back to the legacy `.att` tag. The provenance is invisible to it.

### F4 — cosign v2 verifies the same artifact with the same command

Decisive. The README's command, unchanged, against the same published chart:

```console
$ mise x aqua:sigstore/cosign@2.6.1 -- cosign verify-attestation \
    --type slsaprovenance \
    --certificate-identity-regexp '^https://github.com/slsa-framework/slsa-github-generator/.+' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
    ghcr.io/donaldgifford/charts/docz-api:0.3.1
  - Existence of the claims in the transparency log was verified offline
  - The code-signing certificate was verified using trusted certificate authority certificates
Certificate subject: https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@refs/tags/v2.1.0
```

The **only** variable is the cosign major version.

### F5 — GHCR does not serve the OCI referrers API

```console
$ curl .../v2/donaldgifford/charts/docz-api/referrers/sha256:09015dab…
{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}
```

This is why cosign v3 uses the `sha256-<digest>` fallback-tag scheme here
rather than true referrers, and it rules out "just wait for registry support"
as a fix.

### F6 — Signatures are unaffected

`cosign verify` passes on every version with both majors. Only the
**attestation** path is affected. The supply-chain gap is therefore narrow: the
artifacts are signed and the provenance exists — consumers just cannot verify
the provenance with the tool version they will install today.

## Conclusion

**Answer: the documented command is wrong for current cosign; the provenance
itself is fine.**

Nothing is broken in the publish pipeline, and no artifact needs republishing.
The SLSA provenance is generated, correctly scoped to this repo and commit,
signed by the expected `slsa-github-generator` identity, and attached to the
right digest — in the legacy `.att` tag. cosign **v3** (what `mise.toml`'s
`cosign = "latest"` resolves to today, and what a consumer following the README
will install) does not read that tag, so the README's provenance command fails
for everyone.

It is a **documentation and tooling-alignment defect**, not a supply-chain
defect. Severity is low but it is consumer-visible: an operator following our
own README concludes our provenance is broken.

## Recommendation

Fix the README so the documented command works with the cosign a consumer will
actually have, and add a regression check so this cannot silently rot again.
See OQ-1 for the mechanism and OQ-3 for the check.

Do **not** republish anything: the existing attestations are valid and
verifiable, and republishing would not change the attachment scheme (the
generator pins its own cosign).

## Open questions

**OQ-1 — How do we make the documented verification work?**

- **(a) Document the cosign v2 requirement for the provenance command, and
  file an upstream-tracking note.** Add a line to the chart README's SLSA
  section saying the provenance attestation is stored in the legacy attachment
  scheme and requires `cosign v2.x` (with the exact `mise x` invocation from
  F4), leaving the signature command as-is for any version. Smallest change,
  honest about reality, no pipeline churn. **← recommendation**
- (b) Bump the SLSA generator to a release whose bundled cosign writes the
  v3-compatible attachment, so a single modern command verifies both. Correct
  long-term shape, but gated on the generator shipping it — needs verification
  that such a release exists before committing to it.
- (c) Re-attest in `ghcr.yml` after the generator runs: download the
  provenance and re-`cosign attest` it with the workflow's own cosign v3 so it
  lands in the new-format attachment. Makes the modern command work now, but
  the re-attestation is signed by `ghcr.yml`, not the generator — it weakens
  the SLSA identity story and the README regex would have to change anyway.
- (d) Leave it; drop the provenance command from the README and keep only the
  signature check.

**OQ-2 — Do we pin cosign in `mise.toml` instead of `latest`?**

- **(a) Leave `cosign = "latest"`.** The local pin drives signing and ad-hoc
  checks, not consumer behaviour, and Renovate keeps it current. Pinning to v2
  to make one doc command work would freeze us on an old major. **←
  recommendation**
- (b) Pin to a v2.x line until OQ-1(b) lands, so `just`-driven local
  verification matches the documented command.
- (c) Pin to a specific v3.x for reproducibility and document the v2 fallback
  separately.

**OQ-3 — Should CI verify its own published artifacts?**

- **(a) Add a post-publish verification step to `ghcr.yml`** that runs both
  documented commands against the just-published digest and fails the job on
  mismatch. This defect survived every release because nothing ever executed
  the README's commands. Cheap, and it turns the README into a tested claim.
  **← recommendation**
- (b) Add it as a scheduled workflow against the latest published version
  instead, keeping the release path fast.
- (c) Skip — treat verification as a consumer concern.

**OQ-4 — Scope of the fix.**

- **(a) Chart README + image docs together**, since `ghcr.io/donaldgifford/
  docz-api` has the identical split and the same README section covers both.
  **← recommendation**
- (b) Chart only for now; handle the image when someone asks.

## References

- `charts/docz-api/README.md.gotmpl` — the "Verifying the chart" section
  containing both commands (lines ~178–200).
- `.github/workflows/ghcr.yml` — `chart` / `image` publish jobs and the two
  `SLSA provenance` jobs (`generator_container_slsa3.yml@v2.1.0`).
- IMPL-0004 Phase 5 — where the publish + signing pipeline was consolidated.
- PR #15 — the chart 0.3.1 publish whose verification surfaced this.
- [OCI referrers API](https://github.com/opencontainers/distribution-spec/blob/main/spec.md#listing-referrers)
- [slsa-github-generator container generator](https://github.com/slsa-framework/slsa-github-generator/blob/main/internal/builders/container/README.md)
