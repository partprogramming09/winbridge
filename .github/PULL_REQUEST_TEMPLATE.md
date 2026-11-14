## What and why

<!-- One or two sentences: what changes and why. -->

## Checklist

- [ ] The strict profile is not widened: no new tool, job, port or firewall rule, and nothing that restarts `sshd`.
- [ ] Tests for new logic were written first and pass: `uv run --with pytest pytest -q`,
      `(cd cli && go vet ./... && go test -count=1 ./...)`, `(cd agent && go test ./...)`,
      `pwsh -NoProfile -File tools/test-bootstrap.ps1`.
- [ ] No keys, real IP addresses, user names or e-mail addresses in code, tests or docs (use `100.64.1.x` and `alice`).
- [ ] `.ps1` files are ASCII and work in Windows PowerShell 5.1 and 7.
- [ ] Docs and `CHANGELOG.md` are updated when behaviour changes.
- [ ] The commit title follows Conventional Commits.
