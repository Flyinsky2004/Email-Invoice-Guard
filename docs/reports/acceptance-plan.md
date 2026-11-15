# Acceptance matrix

Each row must be filled with actual test names/command evidence before completion. Mock services are mandatory, real credentials optional manual acceptance.

| Requirement | Verifier | Critical assertions |
|---|---|---|
| Contract boundaries | core tests + per-task diff ownership | provider-neutral types; optional money/date fields; safe IDs/URLs |
| Microsoft authorization | provider mock HTTP | PKCE S256; expiring one-use state; independent refresh; Outlook/Hotmail address; immutable delta; retry-after; foreign origin cannot steal bearer |
| QQ onboarding/IMAP | trusted TLS fake IMAP + HTTPS handler | auth-code only; actual certificate validation; folder/UIDVALIDITY UID identity; reconnect; no forwarded-header spoof |
| Bootstrap and all-mail | sync fake source + real store integration | old baseline silent; new/recent delivered; concurrent arriving mail retained; cursor atomic rollback; parsing failure delivered |
| Persistence | storage integration | WAL/FK/busy timeout; migration idempotence; credentials/session ciphertext; SQL inputs bound; backup/restore recent WAL |
| RFCHOST | exact fixture/variants | all specified fields 1140-456=684; Chinese promo; multiline; multi-item; credit separate balance; no float; conflicts review |
| Invoice lifecycle | service+real SQLite | duplicate invoice paid status retained; authorized CAS confirm twice; event one; undo restores; no keyword payment inference |
| Reminders | testable clock+real SQLite | timezone09; initial day suppressed; local-day unique even restart; status unknown distinct; status recheck prevents stale queued reminder |
| Assets | service+store | same provider/account/explicit hostname links history; missing/ambiguous untouched; old invoice cannot regress known period |
| Telegram | mock HTTP capture | allowed user+chat; confirm two stages; callbacks64bytes; HTML escaped; 4096UTF16; full long body; per-part receipts; unknown timeout retry |
| Application | actual composition+E2E | no runtime stub; signal shutdown; health; missing secrets fail safely; dynamic settings apply |
| Release | gofmt/vet/test/race/build/Docker | CGO-free Linux build; container builds and boots with test env; DB survives container restart |
| Dependencies | pinned modules + govulncheck | exact module versions and sums; inspect actual reachable findings |

External live manual checklist: Microsoft personal app redirect and consent, QQ app authorization and initial folder contents, Telegram bot/chat whitelist and actual longpoll traffic, external HTTPS certificate/reverse-proxy trust, volume backup plus separate key recovery. These are not labeled tested without live credentials.
