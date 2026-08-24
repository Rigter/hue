# Contributing to hue

Thanks for your interest in improving `hue`.

## Getting started

- Use Go 1.22 or newer.
- Fork the repository and create a branch from the default branch.
- Keep the library dependency-free unless a change has a clear, discussed need.

## Before opening a pull request

Run the full local check suite from the repository root:

```bash
gofmt -w hue.go hue_test.go example/main.go tools/gendemo/main.go
go test -race ./...
go vet ./...
go generate ./...
git diff --check
```

`go generate ./...` updates the generated palette tables and swatches in the
README. Include those changes whenever an extraction change affects the demo.

## Pull requests

- Keep pull requests focused and explain the problem they solve.
- Add or update tests for behavior changes and bug fixes.
- Preserve the public API and the JSON field names in `ColorInfo` and `Result`.
- Update the README when usage, options, output, or supported image behavior
  changes.

## Commit messages

Use Conventional Commits with a concise scope where useful, for example:

```text
fix: choose text color by WCAG contrast ratio
docs: clarify transparent-pixel handling
feat(example): add a command-line demonstration
```

## Reporting bugs and requesting features

Please open a GitHub issue with a minimal reproducible example, the Go version,
and the image format involved. Do not use public issues to report security
vulnerabilities; see [SECURITY.md](./SECURITY.md).
