# mavor — voice-to-text utility for Wayland

# Default model used by the daemon when none is configured.
default_model := "base.en"
# Model used by integration tests (small, downloads quickly, deterministic enough).
test_model := "tiny.en"
# Cache dir for whisper models (matches the `models` subcommand).
model_dir := env_var_or_default("XDG_CACHE_HOME", env_var("HOME") + "/.cache") + "/mavor/models"
# Where the GPU benchmark's whisper.cpp build lives. It is out of tree because
# it is a build artifact of another project, not of this one, and it is under
# the cache so a `git clean` cannot cost an hour of compiling.
bench_gpu_dir := env_var_or_default("XDG_CACHE_HOME", env_var("HOME") + "/.cache") + "/mavor-bench/whisper-vulkan"
bench_gpu_bin := bench_gpu_dir + "/whisper.cpp/build-vulkan/bin"

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

# Benchmark every installed model: speed, peak memory, accuracy, on every
# backend this machine can run. Writes docs/reports/model-benchmarks.md and
# test/reports/benchmarks/latest.json.
#
# Picks up the Vulkan whisper build automatically if `just bench-gpu-build` has
# been run; without it the report says the GPU column was skipped and why.
#
# Benchmark every installed model: speed, memory, accuracy, CPU and GPU.
bench *args: build
    #!/usr/bin/env bash
    set -euo pipefail
    gpu_flag=()
    if [ -x "{{bench_gpu_bin}}/whisper-cli" ]; then
        gpu_flag=(-whisper-gpu "{{bench_gpu_bin}}/whisper-cli")
    else
        echo "no Vulkan whisper build at {{bench_gpu_bin}} — run 'just bench-gpu-build' for the GPU column" >&2
    fi
    go run ./cmd/mavor-bench "${gpu_flag[@]}" {{args}}

# The same sweep with the in-process sherpa-onnx engines linked in. Needs cgo,
# so it cannot cross-compile and is a separate recipe rather than the default.
#
# Benchmark every model including the sherpa engines (needs cgo).
bench-sherpa *args: build
    #!/usr/bin/env bash
    set -euo pipefail
    gpu_flag=()
    if [ -x "{{bench_gpu_bin}}/whisper-cli" ]; then
        gpu_flag=(-whisper-gpu "{{bench_gpu_bin}}/whisper-cli")
    fi
    CGO_ENABLED=1 go run -tags sherpa ./cmd/mavor-bench "${gpu_flag[@]}" {{args}}

# Skips what is already cached; stops if the disk drops below 6 GB free.
#
# Download every model in the catalog, so `just bench` has something to measure.
bench-models: build
    #!/usr/bin/env bash
    set -uo pipefail
    for m in $(./bin/mavor models list --json | grep -o '"name": "[^"]*"' | cut -d'"' -f4); do
        avail=$(df --output=avail -BG "{{model_dir}}" | tail -1 | tr -dc 0-9)
        if [ "$avail" -lt 6 ]; then
            echo "only ${avail}G free — stopping before $m" >&2
            exit 1
        fi
        ./bin/mavor models pull "$m" || echo "pull failed: $m" >&2
    done

# Build the Vulkan-enabled whisper.cpp that the GPU column needs.
#
# The stock package is CPU-only on most distros — nixpkgs' whisper-cpp brings
# up nothing but a CPU backend — so measuring the GPU path means building
# upstream ourselves. Pinned to the same version as the packaged build, so the
# CPU and GPU rows compare the same upstream code.
#
# Needs: cmake, ninja, glslc (shaderc), the Vulkan headers and loader, the
# SPIR-V headers, and a Vulkan driver for your GPU.
#
# Build the Vulkan whisper.cpp that the GPU benchmark column needs.
bench-gpu-build version="v1.9.2":
    #!/usr/bin/env bash
    set -euo pipefail
    src="{{bench_gpu_dir}}/whisper.cpp"
    mkdir -p "{{bench_gpu_dir}}"
    if [ ! -d "$src/.git" ]; then
        git clone --depth 1 --branch {{version}} https://github.com/ggml-org/whisper.cpp "$src"
    fi
    # ggml-vulkan find_package()s SPIRV-Headers and then includes them by
    # path, so the include directory has to be passed as well as the prefix.
    prefix_args=()
    if command -v nix-build >/dev/null 2>&1; then
        spirv=$(ls -d /nix/store/*-spirv-headers-* 2>/dev/null | grep -v '\.drv$' | head -1 || true)
        if [ -n "$spirv" ]; then
            prefix_args=(-DCMAKE_PREFIX_PATH="$spirv" -DCMAKE_CXX_FLAGS="-I$spirv/include")
        fi
    fi
    cmake -S "$src" -B "$src/build-vulkan" -G Ninja \
        -DCMAKE_BUILD_TYPE=Release -DGGML_VULKAN=ON -DWHISPER_BUILD_TESTS=OFF \
        "${prefix_args[@]}"
    cmake --build "$src/build-vulkan" -j "$(nproc)"
    echo "built {{bench_gpu_bin}}/whisper-cli"
    echo "verify it sees your GPU with: LD_LIBRARY_PATH={{bench_gpu_bin}} {{bench_gpu_bin}}/whisper-cli"

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
    echo "pushed v{{version}} — release.yml takes it from here"

# Download a whisper model into the cache (no-op if already present).
_ensure-model name:
    @if [ ! -f "{{model_dir}}/ggml-{{name}}.bin" ]; then \
        echo "downloading ggml-{{name}}.bin into {{model_dir}}"; \
        mkdir -p "{{model_dir}}"; \
        curl -fSL --output "{{model_dir}}/ggml-{{name}}.bin" \
            "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-{{name}}.bin"; \
    fi
