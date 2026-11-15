# Codex integration review

All eight workers were launched through Herdr in distinct sibling Git worktrees. Root-owned contracts frozen at a8384da, compatible amendments CR001 HTTPS proxy peers, CR002 merchant URL validator, CR003 QQ-hosted alias domain allowlist. No worker changed root dependencies or shared domain models. Each accepted module was independently tested with package go test -count=1, go vet and go test -race; scope checked using git diff against its baseline.

Merge order: T06 storage; T01 Microsoft; T02 QQ/IMAP; T03 pipeline; T04 parser; T05 lifecycle/reminder/assets; T07 Telegram; T08 composition. After every merge root ran go build ./... and go test ./...; follow-up merge command outputs are under evidence/. Rebase conflicts were limited to append-only LESSONS.md; entries from both sides were retained and deduplicated by rule heading, differing entry content would stop automatic reconciliation for manual inspection. Worker reports retain pre-rebase commit IDs; final integrated commits are recorded in Git merge history.

## Review findings resolved

- Microsoft: atomic account+credential creation, same-address reauthorization without history loss, strict request-origin redirects, full Retry-After, actual Graph folder schema/localized roles, child folder recursion, large body streaming and attachment metadata paging. Official folder API checked, not inferred from mocks.
- QQ/IMAP: correct MIME headers for BODY[part], complete text without silent limits, raw fallback with diagnostics on decode failure, connection-wide timeouts including TLS, UID arithmetic overflow, one-time secure setup and existing-account reuse, case-sensitive mailbox identity except INBOX.
- Pipeline: consistent delivery kinds, canonical source IDs, diagnostics under provider identity, review errors never formal pending tasks, invoice duplicates preserve manual status, disabled-account race, 410/UIDVALIDITY rebuild boundaries, dynamic folder discovery, damaged-MIME review.
- Parser: exact original RFCHOST fixture, full Recurring Discount type, missing mandatory candidate detection, signed amounts/percentage validation, strict matching HTTPS invoice URL and integer overflow checks.
- Storage: independent memory stores, fixed-width date-time ordering, safe backup exclusive creation/alias rejection, full internal scans beyond100 invoices/assets, latest billing periods stored as a pair.
- Telegram: UNKNOWN ambiguous API response vs known failures, per-part receipts, full escaped Unicode content, real merchant URL button, async manual sync, pre-send reminder status recheck after rate limiting, transient repository reads retry rather than drop, triggering source email retained, folder IDs accessible to users.
- Deployment: supported pinned Alpine/Go/Caddy image tags verified against manifests and official lifecycle/releases; no unsupported3.21 runtime.

## Root cross-module regressions

Real SQLite tests exercise atomic page rollback (email/invoice/asset/outbox/cursor), replay after database reopen, duplicate invoice payment state/event idempotence, asset renewal period/history, historic mail suppression with review mail preservation, damaged MIME review and persistent diagnostics. A deterministic test reproduced folder refresh overwriting a user toggle during remote discovery; root fixed it by re-reading preferences and updating metadata inside one transaction with network I/O outside. Standard and race tests passed after correction.

Merchant URL trust now belongs to registered parser policies through optional core.InvoiceURLValidator. Telegram has no production vendor mapping; it checks generic HTTPS hygiene and delegates trust. Future parsers can enable safe payment links without Telegram edits.

Live Microsoft/QQ/Telegram credentials have not been used. Mock API/TLS and container verification are distinguished from external live acceptance in final-report.md.
