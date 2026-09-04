# mavor — voice-to-text utility for Wayland

# Default model used by the daemon when none is configured.
default_model := "base.en"
# Model used by integration tests (small, downloads quickly, deterministic enough).
test_model := "tiny.en"
# Cache dir for whisper models (matches the `models` subcommand).
model_dir := env_var_or_default("XDG_CACHE_HOME", env_var("HOME") + "/.cache") + "/mavor/models"
# Build stamp baked into `mavor version`. Several clones of this repo install to
# the same ~/.local/bin/mavor, so the commit is how you tell them apart.
commit := `git describe --always --dirty --match 'v[0-9]*' 2>/dev/null || echo unknown`
build_time := `date -u +%Y-%m-%dT%H:%M:%SZ`
ldflags := "-X main.Commit=" + commit + " -X main.BuildTime=" + build_time

# Show available recipes.
default:
    @just --list

# Quality gate (local development) — formats, lints, and runs tests.
check: format lint test

# Quality gate (CI and pre-commit) — read-only validation.
check-ci: lint-ci test

# Format all Go source files in-place.
format:
    go fmt ./...

# Run static analysis and linter checks.
lint:
    go vet ./...
    staticcheck ./...

# Verify formatting and static analysis without mutating files.
lint-ci:
    test -z "$(gofmt -l cmd internal test)" || (echo "gofmt would change:" && gofmt -l cmd internal test && exit 1)
    go vet ./...
    staticcheck ./...

# Unit tests only (fast, no Wayland required).
test *args:
    go test ./... {{args}}

# Tidy modules, ensure test model is present, and run doctor check.
setup:
    go mod tidy
    @just _ensure-model {{test_model}}
    @just doctor || true

# End of session check.
done: check-ci
    @echo "Quality gate passed. Ready to commit."

# Run environment and dependency verification.
doctor:
    @go run ./cmd/mavor doctor

# Build the static, pure-Go binary into ./bin/mavor.
build: (_ensure-model default_model)
    @mkdir -p bin
    go mod download
    CGO_ENABLED=0 go build -ldflags '{{ldflags}}' -v -o bin/mavor ./cmd/mavor

# Build with the in-process sherpa-onnx engines linked in (needs cgo).
build-sherpa: (_ensure-model default_model)
    @mkdir -p bin
    go build -tags sherpa -ldflags '{{ldflags}}' -v -o bin/mavor ./cmd/mavor
    @echo "Built mavor with sherpa-onnx (cgo) at bin/mavor"

# Install the binary to ~/.local/bin/mavor.
install: build
    @mkdir -p "${HOME}/.local/bin"
    install -m 0755 bin/mavor "${HOME}/.local/bin/mavor"
    @echo "Installed mavor to ${HOME}/.local/bin/mavor"

# Deploy binary and install/restart systemd user service.
deploy: install
    @${HOME}/.local/bin/mavor service install --start || bin/mavor service install --start
    @echo "Deployed mavor binary and restarted systemd user service."

# Run the daemon against the user's actual Wayland session, with verbose logging.
dev:
    go run ./cmd/mavor daemon -v

# Integration tests — spawns headless sway, drives the daemon end-to-end.
# Skipped when the Wayland/audio harness can't come up; failures are real.
test-int *args:
    go test -tags=integration ./test/integration/... {{args}}

# End-to-end smoke test with real whisper transcription.
test-e2e: (_ensure-model test_model)
    go test -tags=e2e ./...

# Run the automated UI Storybook test and generate pixel-accurate HTML report with real headless screenshots.
storybook:
    go test -tags=integration -run TestUIStorybookReport ./test/integration/... -v
    @echo ""
    @echo "UI Storybook Report: test/reports/ui-storybook.html"

# Cut a release: verify, tag, push. release.yml takes it from there.
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    # This recipe exists so `oss-release` takes its delegated path. Without it
    # the script falls back to tagging HEAD *and* running `gh release create`,
    # and since release.yml also triggers on the tag push, both race to create
    # the same release. Whichever loses fails with "a release with the same tag
    # name already exists" — which is how v0.1.0 published carrying no assets.
    #
    # Nothing generated has to ride in the tag: mavor is pure Go with no
    # embedded bundle, so the tag is plain HEAD and goreleaser builds from it.
    if [ -n "$(git status --porcelain)" ]; then
        echo "working tree is dirty — commit or stash first" >&2
        exit 1
    fi
    if git rev-parse -q --verify "refs/tags/v{{version}}" >/dev/null; then
        echo "tag v{{version}} already exists — pick another version" >&2
        exit 1
    fi
    just check-ci
    git tag "v{{version}}"
    git push origin "v{{version}}"
    @echo "pushed v{{version}} — release.yml takes it from here"

# Download a whisper model into the cache (no-op if already present).
_ensure-model name:
    @if [ ! -f "{{model_dir}}/ggml-{{name}}.bin" ]; then \
        echo "downloading ggml-{{name}}.bin into {{model_dir}}"; \
        mkdir -p "{{model_dir}}"; \
        curl -fSL --output "{{model_dir}}/ggml-{{name}}.bin" \
            "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-{{name}}.bin"; \
    fi
