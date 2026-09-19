# Contributing

Thanks for your interest in the StonkAgents agent. Bug reports, fixes and features are welcome.

## Before you start

- Check the open issues so the work is not already in progress.
- For anything larger than a bug fix, open an issue first and describe the change. It saves
  everyone time if the approach is agreed before the code is written.
- Security problems go to security@stonkagents.com, not to the issue tracker. See
  [SECURITY.md](SECURITY.md).

## Development setup

You need Go 1.26.4 or newer (the toolchain is pinned in `go.mod`). PostgreSQL 16 is only
needed for the tracker's database tests; Docker helps there
(`docker compose -f docker-compose.tracker.yml up -d`).

```bash
git clone https://github.com/stonkagents/agent.git
cd agent
go mod download
go build ./...
```

## Pull request flow

1. Fork the repository and create a branch from `main`.
2. Make the change in small, reviewable commits.
3. Run the checks below and make sure they pass.
4. Open a pull request against `main` and fill in the template. Link the issue it closes.
5. A maintainer reviews it. Expect questions; small follow up commits are fine, no need to
   squash until the review is done.

## Checks

```bash
gofmt -l ./cmd ./internal ./pkg ./tracker ./installer   # prints nothing when formatted
go vet ./...
go test -short ./...
```

`go test -short` skips the long peer to peer end to end suite; it still runs on CI. Tracker
tests that need PostgreSQL skip themselves unless `DATABASE_URL` is set. If you touch the
installer, the scripts under `scripts/` and `installer/` have to keep building on Windows and
macOS respectively; say in the pull request which platforms you tested on.

New behaviour comes with tests. The daemon and tracker are tested with the standard library
and testify; look at a neighbouring `_test.go` for the conventions.

## Commit messages

Use a short imperative subject with the area in front, for example
`tracker: reject bounty awards after the escrow is released` or
`installer: keep the data folder on a major upgrade`. Explain the why in the body when it is
not obvious from the diff. Reference issues with `Fixes #123`.

## Code style

- `gofmt` and `go vet` clean; `golangci-lint run ./...` uses the config in `.golangci.yml`.
- Keep files focused; if a file grows past a few hundred lines, split it.
- Validate input at the boundaries (HTTP handlers, CLI flags, files read from disk).
- No secrets, keys or environment files in the repository, ever. `.env*` files are ignored.

## License

By contributing you agree that your contributions are licensed under the Apache License 2.0,
the license of this repository.
