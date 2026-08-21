---
id: INV-0007
title: "Ingest failures are silent and block re-ingestion"
status: Concluded
author: Donald Gifford
created: 2026-08-21
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0007: Ingest failures are silent and block re-ingestion

**Status:** Concluded
**Author:** Donald Gifford
**Date:** 2026-08-21

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [F1 — asynq never logs the handler's error unless an ErrorHandler is set](#f1--asynq-never-logs-the-handlers-error-unless-an-errorhandler-is-set)
  - [F2 — Our worker's no-log-here comment codifies a false assumption](#f2--our-workers-no-log-here-comment-codifies-a-false-assumption)
  - [F3 — The error text lives only in Redis, on the task record](#f3--the-error-text-lives-only-in-redis-on-the-task-record)
  - [F4 — An exhausted task silently swallows every future ingest for that repo](#f4--an-exhausted-task-silently-swallows-every-future-ingest-for-that-repo)
  - [F5 — Nothing validates GitHub App credentials before first use](#f5--nothing-validates-github-app-credentials-before-first-use)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [Recovery runbook (as-is, no code changes)](#recovery-runbook-as-is-no-code-changes)
- [Open questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Question

While testing the EKS deployment, an ingest job died with a single log line —
`asynq: WARN: Retry exhausted for task id=ingest:<owner>/<name>` — and **no
error appeared anywhere**: not in the pod logs, not on the webhook deliveries
(GitHub showed every POST answered 202). Three questions:

1. Where did the actual error go?
2. Why doesn't anything check the outbound GitHub path (App credentials),
   when the inbound path (webhooks) is visibly healthy?
3. After retries exhaust, does anything recover the repo, or is it stuck?

## Hypothesis

Initial suspicion: the errors are logged somewhere we weren't looking
(a different stream or level), and recovery is automatic on the next push
because enqueue-coalescing treats a duplicate task id as success.

Both halves turned out **wrong**, the second one badly: the errors are
logged *nowhere*, and the coalescing behavior is precisely what *prevents*
recovery.

## Context

**Triggered by:** the first EKS test deployment (chart 0.4.0, Okta login via
Gateway API + ALB). After fixing the auth topology (`AUTH_REDIRECT_BASE`
must be the docz-**site** host, since docz-site is a BFF proxy for
`/auth/`+`/api/`), the first repo ingest failed all five attempts. The only
observable signal was the terminal asynq WARN line.

Phase 4 (CLAUDE.md) documents the intended failure model: "any ingest error
is returned so asynq retries with backoff (the content-hash gate makes
retries idempotent)". This investigation is about what happens *around* that
model — visibility of the error and life after the last retry.

## Approach

1. Read `internal/queue/worker.go` / `client.go` for what we log and what
   options we enqueue with.
2. Read asynq v0.26.0 source (module cache) for what the processor logs on
   handler failure, where the error string goes, and what the task-id
   uniqueness check actually tests.
3. Map the enqueue path's `ErrTaskIDConflict` handling against the archived
   state's lifetime.
4. Inventory what does/doesn't exercise GitHub App credentials outside a job.

## Environment

| Component | Version / Value |
|-----------|----------------|
| asynq | `v0.26.0` (read from the module cache) |
| queue name / task type / task id | `ingest` / `ingest:repo` / `ingest:<owner>/<name>` |
| enqueue options | `TaskID` + `ProcessIn(debounce)` + `MaxRetry(5)` + `Retention(24h)` |
| deployment | EKS, chart 0.4.0, image v0.5.1 |

## Findings

### F1 — asynq never logs the handler's error unless an ErrorHandler is set

`processor.handleFailedMessage` (asynq v0.26.0, `processor.go:335`) is the
only place a failed handler return lands:

- it calls `Config.ErrorHandler` **if one is set** (`processor.go:336-338`);
- otherwise a mid-flight failure schedules the retry with **no log at all**
  (`processor.go:347` → `retry`), and the final failure logs only
  `Retry exhausted for task id=...` (`processor.go:344`) — the id, never the
  error text.

Our `queue.NewWorker` sets no `ErrorHandler`. So an ingest that fails five
times produces exactly one WARN line containing zero diagnostic content.
The user-visible symptom — "webhooks all green, no errors anywhere, repo
never ingests" — is the designed-in output of this configuration.

### F2 — Our worker's no-log-here comment codifies a false assumption

`internal/queue/worker.go:106-107`, at the failure site in `handleIngest`:

> The span, the failure metric, and the returned error (which asynq logs
> with the repo-labeled context) carry the signal; don't also log here.

The parenthetical is **false** (F1): asynq does not log returned errors.
The failure site deliberately skips `slog` on the strength of behavior the
library doesn't have. The two signals that *do* exist need optional backends
the test cluster doesn't run:

- the span records the error — visible only with an OTLP collector + trace
  backend (`OTEL_EXPORTER_OTLP_ENDPOINT` unset ⇒ dropped);
- `docz_api_ingest_jobs_total{status="failure"}` increments — visible only
  with Prometheus scraping, and alerting only with the chart's
  `DoczAPIIngestFailures` rule deployed.

Logs were the one channel guaranteed to exist, and they're the one channel
the failure never reaches.

### F3 — The error text lives only in Redis, on the task record

`processor.retry` passes `e.Error()` to `broker.Retry` (`processor.go:360`),
which stores it on the task hash; archived tasks keep it. So the root cause
of any exhausted ingest is recoverable — just not from logs:

```bash
# via the asynq CLI (shows last error per task)
asynq task ls --queue=ingest --state=archived

# zero-install: the error string is embedded readably in the encoded msg
redis-cli HGET 'asynq:{ingest}:t:ingest:<owner>/<name>' msg | strings
```

### F4 — An exhausted task silently swallows every future ingest for that repo

Three verified facts compose into the trap:

1. `EnqueueIngest` uses the **fixed** id `ingest:<owner>/<name>` and maps
   `ErrTaskIDConflict`/`ErrDuplicateTask` to **nil** — coalesce-as-success
   (`internal/queue/client.go:87-96`).
2. asynq's enqueue uniqueness check is a bare `EXISTS` on the task key
   (`internal/rdb/rdb.go:98-108`, `enqueueCmd`), and that key exists in
   **every** state — including `archived`.
3. Archived tasks live **90 days** (`internal/rdb/rdb.go:841`,
   `archivedExpirationInDays`). The `Retention(24h)` we set applies only to
   *completed* tasks; it does not touch the archive.

Composed: once a repo's ingest exhausts its retries, every subsequent
webhook push, `repo_added`, or `-onboard` for that repo enqueues, hits the
archived task's id, is reported as success, and **does nothing — for up to
90 days**. Fixing the root cause (e.g. correcting a bad credential) does not
resurrect the repo; the operator must manually delete or re-run the archived
task. Nothing logs that the swallow happened.

Coalesce-as-success is the right semantics against a *pending/scheduled*
task (the pending job will cover the trigger). It is the wrong semantics
against a *dead* one (nothing will).

### F5 — Nothing validates GitHub App credentials before first use

Webhook deliveries being green proves only the **inbound** path: GitHub can
reach us and the HMAC matches. The App private key and app id are exercised
exclusively on the **outbound** path — JWT → installation token → contents
fetch — which runs only inside a worker job. Consequences:

- A wrong/mangled `private-key` or `app-id` (typical when moving to a new
  secret provider) surfaces as five silent job failures (F1) followed by a
  90-day swallow (F4) — never as a startup or readiness error.
- `/readyz` checks postgres/meilisearch/redis only. That scope is
  **deliberate and correct**: readiness gates request routing, and GitHub
  being down should not pull the read API out of rotation. The gap is not
  in readyz — it's that no *startup self-check* (e.g. authenticate the App
  JWT against `GET /app`) exists to fail loudly at deploy time.

## Conclusion

**Answer:** all three questions resolve against us, by construction.

1. The error goes to the Redis task record only (F1/F3); logs get a bare
   "Retry exhausted" because we never installed the `ErrorHandler` our own
   comment assumes exists (F2).
2. Nothing checks outbound GitHub because credentials are only exercised
   inside jobs and no boot-time self-check exists; webhook health is
   evidence about the wrong direction (F5).
3. Nothing recovers. Worse than nothing: the archived task blocks and
   silently discards all re-ingestion triggers for that repo for up to
   90 days (F4).

## Recommendation

Three code fixes, in value order — all small:

1. **Install `asynq.Config.ErrorHandler`** in `queue.NewWorker`, slog-ing
   every failed attempt (task id, repo, retry count, error). Correct the
   false comment in `handleIngest` at the same time. This restores the
   behavior the code already believes it has.
2. **Startup App-auth self-check**: mint the App JWT and call `GET /app`
   once during boot; log the authenticated app slug or fail loudly.
   Turns "5 silent failures + 90-day swallow" into a deploy-time error.
3. **Un-swallow the archived case**: on `ErrTaskIDConflict`, distinguish a
   live pending/scheduled task (coalesce, as today) from an archived one
   (delete-and-re-enqueue, or at minimum log loudly). An
   `Inspector.GetTaskInfo` state check on the conflict path is enough.

## Recovery runbook (as-is, no code changes)

1. Read the stored error: `asynq task ls --queue=ingest --state=archived`
   (CLI: `go install github.com/hibiken/asynq/tools/asynq@latest`, point it
   at the Valkey service — port-forward works).
2. Fix the root cause it names.
3. Clear the blocker — either re-run the archived task (safe: the payload
   carries no SHA; the worker refetches HEAD) or delete it and push again:
   `redis-cli DEL 'asynq:{ingest}:t:ingest:<owner>/<name>'`.

## Open questions

- **OQ-1** — root cause of the triggering EKS failure: not yet retrieved
  from the archived task record (runbook step 1). Ranked suspects, in
  order: mangled `private-key` PEM in the new external Secret; blocked
  egress to `api.github.com`; repo missing `.docz.yaml` (a *permanent*
  failure that retries can never fix); installation-id mismatch.
- **OQ-2** — should the boot self-check fail startup or warn-only?
  Fail-fast matches the OIDC-discovery precedent (a bad issuer fails the
  boot); warn-only keeps the read API serving when only ingest is broken.
- **OQ-3** — fix 3's mechanism: enqueue-side `GetTaskInfo`+delete, vs a
  worker-side `DeleteTask` after archiving, vs shortening the archive TTL.
  Enqueue-side is the least magical and keeps the decision at the choke
  point that owns the TaskID scheme.

## References

- `internal/queue/worker.go` — `handleIngest` (:82), the false comment
  (:106-107), no `ErrorHandler` in the `asynq.Config` (:54-59)
- `internal/queue/client.go` — enqueue options + conflict-as-success
  (:87-96), `queueName` (:17)
- asynq `v0.26.0`: `processor.go:335-349` (`handleFailedMessage`),
  `processor.go:360` (error string → broker), `internal/rdb/rdb.go:98-108`
  (`enqueueCmd` EXISTS check), `internal/rdb/rdb.go:840-841`
  (`maxArchiveSize`, `archivedExpirationInDays = 90`)
- CLAUDE.md — Phase 4 (queue architecture, retry model, coalescing design)
- INV-0006 — prior investigation; precedent for the Findings/Decision shape
