# WatchPot

[![CI](https://github.com/its-the-vibe/WatchPot/actions/workflows/ci.yaml/badge.svg)](https://github.com/its-the-vibe/WatchPot/actions/workflows/ci.yaml)

A watched pot never boils. A web app to show currently running CI/CD commands.

## Development

The project provides a `Makefile` for common tasks:

| Target | Description |
|--------|-------------|
| `make build` | Compile the binary (`watchpot`) |
| `make test` | Run all unit tests |
| `make lint` | Run `go vet` linting checks |

### Quick start

```bash
# Build
make build

# Run tests
make test

# Lint
make lint
```

## CI

A GitHub Actions workflow (`.github/workflows/ci.yaml`) runs `make build`, `make test`, and `make lint` on every push and pull request to the `main` branch.
