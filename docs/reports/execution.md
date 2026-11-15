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
