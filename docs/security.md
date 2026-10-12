# Security notes

A review of how Sebab handles secrets and what it trusts (PRD Phase 5 "security review of secret handling").
Each claim names the test that proves it.

## Threat model

Sebab runs as the desktop user and never needs root. It protects credentials (bearer tokens, API keys, basic auth
passwords, notification channel tokens) against accidental disclosure: screenshots of the bar, copied diagnoses
pasted into tickets, logs, shell history and process listings. It does not protect against other programs already
running as the same user (the Secret Service itself does not either once unlocked).

## Where secrets live

| Place | Protection | Proof |
|---|---|---|
| Secret Service keyring (preferred) | the keyring's own encryption and unlock policy | `internal/secret` TestKeyringPreferred |
| `~/.config/sebab/secrets.toml` (fallback) | created 0600 atomically; refused when group/other can read it; a warning on every store; `sebab doctor` flags it | TestFileFallbackWhenNoKeyring, TestDoctorNoKeyringOnArch |
| `env:NAME` references | read from the engine's environment | TestEnvReference |
| `config.toml` | holds only references (`secret = "name"`), written 0600 | TestPanelFlowWithHandoffFile |

## How secrets reach the engine

- CLI: only from stdin (`--secret-stdin`, `sebab secret set NAME < file`) or a handoff file. No flag accepts a
  value, so nothing lands in shell history or `/proc/<pid>/cmdline`.
- Noctalia panel: Noctalia gives child processes no stdin (verified in its source), so the panel writes the token
  into a one-shot handoff file that the engine created first: mode 0600 inside a 0700 directory under
  `$XDG_RUNTIME_DIR/sebab/handoff` (tmpfs). The engine refuses files outside that directory, symlinks, files not
  owned by the user and files readable by others (deleting the exposed file unread), then deletes the file after
  reading. Abandoned files are deleted when the panel closes and after 10 minutes by the next handoff.
  Proof: TestHandoffLifecycle, TestHandoffRejections, panel_test.luau ("token leaked into argv").

## Where secrets must never appear

Every event line is passed through a redactor that knows each resolved secret in its raw, JSON-escaped,
URL-encoded and base64 forms (and `user:password` for basic auth). Targets are rendered with sensitive query
parameters (`token`, `key`, `secret`, `sig`, `password`, ...) and userinfo replaced by `[REDACTED]`. Validation
errors mask URL queries and userinfo.

Automated canary tests grep for a known secret in: the event stream, desktop and frontend notifications, `sebab
report` Markdown, `sebab check` output, `monitor list`, `history`, every stdout/stderr of the CLI tests and the
written config (TestSecretNeverLeaksThroughStream, assertNoLeak in `internal/cli`, TestRedactorForms,
TestValidationErrorsDoNotEchoCredentials, channels TestFailuresReportedOnceAndScrubbed and
TestUnreachableChannelErrorIsScrubbed, where a Telegram token sits in the URL path).

Redirects: Go drops `Authorization` and cookies when a redirect leaves the host; Sebab also drops the custom auth
header (`auth.type = "header"`) and only follows redirects to the same host, its `www.` variant or
`allowed_redirect_hosts`.

## Engine surface

- Control socket `$XDG_RUNTIME_DIR/sebab/sebab.sock`: 0600 in a 0700 directory, and each connection's peer uid is
  checked with `SO_PEERCRED`. Commands: subscribe, check, reload, report, status. None of them returns a secret.
- The engine never executes shell commands. The Noctalia plugin uses argument arrays for every command except the
  long-running `runStream`, whose binary path is shell-quoted.
- External notification channels are opt-in and never receive monitor targets.
- `insecure_tls = true` is shown in every frontend and in reports.
- The systemd unit adds `NoNewPrivileges`, `MemoryDenyWriteExecute`, `RestrictAddressFamilies` and friends.

## Known limits

- Secrets are held in process memory while the engine runs (needed to send them).
- A handoff file abandoned by a crashed panel stays on tmpfs until the next handoff or logout (at most 10 minutes
  of exposure to the same user only).
- Response content shown by a failed assertion (an actual JSON or header value, truncated to 60 characters) comes
  from the server and is not redacted unless it matches a known secret.

## Builds

Release builds are static (`CGO_ENABLED=0`), use `-trimpath` and a fixed commit timestamp; two clean builds of the
same commit produce byte-identical binaries (checked locally). Release checksums are GPG-signed (`.goreleaser.yaml`).
