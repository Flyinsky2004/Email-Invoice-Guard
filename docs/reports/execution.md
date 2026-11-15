# Execution evidence

## Audit and baseline

2026-11-15: repository only contained original AGENT.md, no commits, no Go sources. Preserved in commit 551cad8. Frozen contract baseline a8384da includes domain models, all repository/service/provider interfaces, schema contract, architecture, dependency lock and exactly eight independent task specs. `go test ./internal/core`, `go build ./...`, `go vet ./...` passed at freeze.

CLI: Claude Code 2.1.325 via Herdr hc; default configured user model, no orchestrator model override. Native worktree conventions differ from user explicit task branch naming, so root created sibling worktrees outside Claude sandbox. Interactive agent terminals per user Herdr manual. acceptEdits with scoped Go/git tool permissions, no permission bypass flag. Initial direct/noninteractive login PATH lacked Node; interactive login shell plus explicit installed Node bin passed command -v go/git/python3/node, actual write calibration and versions. No global configs modified.

All eight worktrees passed actual `go build ./...` and `go test ./...` before launching. Worker mapping in worker-map.json. Launches through hc are real and remain watchable/steerable. Root owns final review/merges. Each initial implementation lane has30min budget; a review correction is separate.

## Verification protocol

1. Read result.txt, actual diff and commit, verify ownership including no core/mod changes.
2. Run worker package tests/vet/race independently and inspect security-critical code.
3. Return concrete failures to original worker (up to two review rounds), root fixes remaining defects.
4. Sequentially merge storage/providers/pipeline/parser/lifecycle/telegram; after each merge full build + applicable tests. T08 joins after real dependencies.
5. Run format check, vet, all tests/race, Linux CGO-free build, Docker build+runtime health, SQLite migration/reopen/backup, mock full delivery/payment/reminder/restart suite and dependency vulnerability scan. Record exact actual outputs and limitations, never infer completion from worker status.

External credentials not present and not required for automated suites; Microsoft/QQ/Telegram live validation pending user-provided configuration. Initial Docker daemon not running, CLI present; local runtime startup/build will be attempted during acceptance.

## Manual recurring reminders and bilingual Telegram (2026-11-15)

Incremental delivery on top of T01–T08. Contract freeze T09, then parallel T10 storage /
T11 domain / T12 Telegram, then serial T13 application integration, then T14 bilingual
Telegram text. Every merge ran `go build ./...`, `go vet ./...` and the full test suite on
`main`; the final `./scripts/verify.sh` (gofmt, vet, test, `-race`, CGO-free Linux build)
exited 0.

- T09: additive `internal/core` contract (manual plan/occurrence/dispatch/event types,
  standalone `ManualReminderRepository` and `ManualReminderService`, `ManualReminderConfig`,
  two appended `DeliveryPayload` fields), CR004 docs, schema contract, DAG, T10–T13 specs.
- T10: migration 0002, `core.ManualReminderRepository` on `*repo`, append-only outbox mirror.
  Orchestrator review required moving `notified_at`/`REMINDER_SENT` to delivery completion.
- T11: `internal/reminder/manual` recurrence, lifecycle, scheduler and service with a fixed
  clock. Orchestrator fixed two post-merge defects: reminder-time-aware first due date and
  skipping dates that already hold an occurrence on edit/resume.
- T12: wizard, commands, callbacks, manual rendering and additive `NewWithManual`. Review
  required a two-stage payment confirmation, removal of non-frozen type assertions, and the
  missing period/asset wizard steps. Orchestrator added the pre-send plan/occurrence recheck.
- T13: `MANUAL_*` config, manual scheduler goroutine, `NewWithManual` wiring, docs and an
  end-to-end create → tick → deliver → confirm → next month → restart test.
- T14: bilingual English/Simplified Chinese Telegram text, `/settings language en|zh-CN`,
  default `zh-CN`, 282 identical catalog keys per language, unchanged command names,
  callback data and exported formatter signatures.

Worker mapping: `docs/reports/worker-map.json` (T09–T14). All task worktrees and branches
were removed after merge. Live Telegram/Microsoft/QQ validation still requires user
credentials and is not claimed as tested.
