# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Project scaffold: Makefile targets, CI, lint configuration and community files.
- Empty terminal UI with the dashboard frame, help overlay (`?`), quit
  confirmation while work is running, light/dark themes and `NO_COLOR` support.
- Redacted crash reports written to the state directory.
- Secrets-leak test harness (`make test-secrets`).
