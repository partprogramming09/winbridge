# Contributing

**Reader:** someone who wants to change WinBridge.

Thanks for your interest. Read [SECURITY.md](SECURITY.md) first: WinBridge controls a real desktop, so changes are judged
by what they could expose.

## Ground rules

- **The strict profile is not widened.** No new tool, job, port or firewall rule, and nothing that restarts `sshd`. A change
  that needs one is a design discussion first (open an issue), not a pull request.
- **No secrets or personal data** in code, tests or docs: no keys, real addresses, user names or e-mail addresses. Use
  `100.64.1.x` and `alice`. A test (`tests/test_no_personal_data.py`) fails when one appears.
- **Tests first for logic** (red, green, refactor); a bug starts with the test that reproduces it. Docs, text and config are
  edited by hand.
- **`.ps1` files are ASCII** and run in Windows PowerShell 5.1 and 7. Write accented text in tests with `[char]0x00F3`.
- **Conventional Commits**, small, in English (`feat:`, `fix:`, `docs:`, `test:`, `chore:`).

## Run the tests

```sh
uv run --with pytest pytest -q
(cd cli && go vet ./... && go test -count=1 ./...)
(cd agent && go test ./...)
pwsh -NoProfile -File tools/test-bootstrap.ps1
pwsh -NoProfile -File tools/check-ps1.ps1
```

`tools/test-jobs-runner.ps1` runs the job runner and only works on Windows (Windows PowerShell 5.1):
`powershell -NoProfile -ExecutionPolicy Bypass -File tools\test-jobs-runner.ps1`.

## Pull requests

Branch from `main`, keep the change small, fill in the template, and say which tests you ran. Reviews look first at what the
change could expose, then at correctness.

By contributing you agree that your work is released under the MIT licence. Everyone taking part follows the
[Code of Conduct](CODE_OF_CONDUCT.md).
