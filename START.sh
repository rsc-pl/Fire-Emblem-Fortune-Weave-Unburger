#!/usr/bin/env bash
# START.sh — build and launch the FEFW canonical corpus console.
#
# The launcher is intentionally dependency-free apart from the Go toolchain
# and coreutils. It resolves the project root from this script's location, so
# it may be invoked from any working directory. The built binary is installed
# under .local/bin (ignored by Git) and is only rebuilt when its build
# fingerprint changes.
set -u

FORMAT_VERSION="fefw-tui-launcher-v1"
BUILD_FLAGS="-trimpath -buildvcs=false"

SCRIPT_PATH="${BASH_SOURCE[0]}"
SCRIPT_DIR="$(cd -- "$(dirname -- "$SCRIPT_PATH")" >/dev/null 2>&1 && pwd -P)" || {
    printf 'error: cannot resolve the launcher directory\n' >&2
    exit 6
}
ROOT="$SCRIPT_DIR"
cd -- "$ROOT" || {
    printf 'error: cannot enter project root: %s\n' "$ROOT" >&2
    exit 6
}

BIN_DIR="$ROOT/.local/bin"
BIN="$BIN_DIR/fefw-tui"
STATE_DIR="$ROOT/.local/state/fefw-tui"
FP_FILE="$STATE_DIR/fingerprint"
LOCK_DIR="$STATE_DIR/build.lock"
GO_BIN="${FEFW_GO:-go}"
BUILD_TMP=""

usage() {
    cat <<'EOF'
FEFW canonical corpus console

usage: ./START.sh [--help] [--check] [--rebuild] [--] [TUI arguments...]

  --help       show this help
  --check      report whether the installed binary matches the current
               sources, module files, toolchain, target and build flags.
               Exit 0 = current, 1 = rebuild required, 3 = no Go toolchain.
  --rebuild    force a rebuild before launching
  --           stop launcher option parsing; remaining arguments go to the TUI

The binary is installed at .local/bin/fefw-tui. State and build locks live
under .local/state/fefw-tui/. Rebuilds are atomic and never replace a working
binary with a failed build. All TUI arguments after launcher options are
forwarded unchanged.

environment:
  FEFW_GO             Go executable to use (default: go)
  FEFW_BUILD_TAGS     extra Go build tags (participates in the fingerprint)
  NO_COLOR            passed through to the TUI

exit codes:
  0 success / current          3 Go toolchain unavailable
  1 check found stale binary   4 another build holds the lock
  2 usage error                5 build failed (previous binary preserved)
  6 launcher/fingerprint error  otherwise the TUI's exit status
EOF
}

die() {
    local code="$1"
    shift
    printf 'error: %s\n' "$*" >&2
    exit "$code"
}

toolchain_ok() {
    if ! command -v "$GO_BIN" >/dev/null 2>&1; then
        printf 'error: Go toolchain not found (tried: %s)\n' "$GO_BIN" >&2
        printf 'hint: install Go 1.21 or newer, or set FEFW_GO to the go binary\n' >&2
        return 1
    fi
    if ! "$GO_BIN" version >/dev/null 2>&1; then
        printf 'error: Go executable is present but not runnable: %s\n' "$GO_BIN" >&2
        return 1
    fi
    return 0
}

collect_sources() {
    shopt -s globstar nullglob
    local f
    for f in "$ROOT"/cmd/fefw-tui/**/*.go "$ROOT"/internal/**/*.go "$ROOT"/go.mod "$ROOT"/go.sum; do
        [[ -f "$f" ]] || continue
        printf '%s\n' "${f#"$ROOT"/}"
    done
}

# fingerprint prints a deterministic digest of everything that can change the
# installed TUI binary. It deliberately excludes translation JSON, generated
# reports and file modification times.
fingerprint() {
    local go_version go_env
    go_version="$("$GO_BIN" version 2>&1)" || return 1
    go_env="$("$GO_BIN" env GOVERSION GOOS GOARCH CGO_ENABLED 2>&1)" || return 1

    {
        printf 'format=%s\n' "$FORMAT_VERSION"
        printf 'go_version=%s\n' "$go_version"
        printf 'go_env=%s\n' "$go_env"
        printf 'goflags=%s\n' "${GOFLAGS:-}"
        printf 'build_tags=%s\n' "${FEFW_BUILD_TAGS:-}"
        printf 'build_flags=%s\n' "$BUILD_FLAGS"
        printf 'cgo=0\n'
        printf 'sources:\n'

        local rel sum
        while IFS= read -r rel; do
            [[ -n "$rel" ]] || continue
            sum="$(sha256sum -- "$ROOT/$rel" 2>/dev/null)" || return 1
            printf '%s %s\n' "${sum%% *}" "$rel"
        done < <(collect_sources | LC_ALL=C sort)
    } | sha256sum | {
        read -r digest _
        printf '%s\n' "$digest"
    }
}

lock_release() {
    rm -rf -- "$LOCK_DIR" 2>/dev/null || true
}

lock_acquire() {
    local waited=0 pid
    while ! mkdir -- "$LOCK_DIR" 2>/dev/null; do
        if [[ -f "$LOCK_DIR/pid" ]]; then
            pid="$(cat -- "$LOCK_DIR/pid" 2>/dev/null || true)"
            if [[ -z "$pid" ]] || ! kill -0 "$pid" 2>/dev/null; then
                # Stale lock from a crashed or killed build.
                rm -rf -- "$LOCK_DIR" 2>/dev/null || true
                continue
            fi
        elif ((waited >= 20)); then
            # The creator died between mkdir and writing its pid.
            rm -rf -- "$LOCK_DIR" 2>/dev/null || true
            continue
        fi
        waited=$((waited + 1))
        if ((waited > 300)); then
            printf 'error: another build is still holding %s\n' "$LOCK_DIR" >&2
            printf 'hint: wait for it, or remove the directory if its process is gone\n' >&2
            return 1
        fi
        sleep 0.1
    done
    printf '%s\n' "$$" >"$LOCK_DIR/pid"
    trap 'lock_release' EXIT
    trap 'cleanup_and_exit 129 HUP' HUP
    trap 'cleanup_and_exit 130 INT' INT
    trap 'cleanup_and_exit 143 TERM' TERM
}

cleanup_and_exit() {
    local code="$1"
    trap - EXIT HUP INT TERM
    if [[ -n "$BUILD_TMP" ]]; then
        rm -f -- "$BUILD_TMP" 2>/dev/null || true
    fi
    lock_release
    exit "$code"
}

is_current() {
    [[ -x "$BIN" && -f "$FP_FILE" ]] || return 1
    [[ "$(cat -- "$FP_FILE" 2>/dev/null || true)" == "$1" ]]
}

build_binary() {
    mkdir -p -- "$BIN_DIR" "$STATE_DIR" || die 6 "cannot create .local directories"

    lock_acquire || exit 4

    local want
    want="$(fingerprint)" || die 6 "cannot compute build fingerprint"
    if ((FORCE == 0)) && is_current "$want"; then
        lock_release
        trap - EXIT HUP INT TERM
        return 0
    fi

    # Only the lock holder may clean stale temp files.
    rm -f -- "$BIN_DIR"/.fefw-tui.tmp.* 2>/dev/null || true
    BUILD_TMP="$(mktemp "$BIN_DIR/.fefw-tui.tmp.XXXXXX")" || die 6 "cannot create build temp file"

    printf 'building fefw-tui (sources changed or forced) ...\n' >&2
    local -a tags=()
    if [[ -n "${FEFW_BUILD_TAGS:-}" ]]; then
        tags=(-tags "$FEFW_BUILD_TAGS")
    fi
    if ! CGO_ENABLED=0 "$GO_BIN" build $BUILD_FLAGS "${tags[@]}" -o "$BUILD_TMP" ./cmd/fefw-tui; then
        rm -f -- "$BUILD_TMP" 2>/dev/null || true
        BUILD_TMP=""
        printf 'error: build failed; the previous binary was left untouched\n' >&2
        exit 5
    fi
    chmod 0755 "$BUILD_TMP" || {
        rm -f -- "$BUILD_TMP" 2>/dev/null || true
        BUILD_TMP=""
        die 6 "cannot mark the new binary executable"
    }
    mv -f -- "$BUILD_TMP" "$BIN" || {
        rm -f -- "$BUILD_TMP" 2>/dev/null || true
        BUILD_TMP=""
        die 6 "cannot install the new binary"
    }
    BUILD_TMP=""

    printf '%s\n' "$want" >"$FP_FILE.tmp.$$" || die 6 "cannot write build fingerprint"
    mv -f -- "$FP_FILE.tmp.$$" "$FP_FILE" || die 6 "cannot install build fingerprint"

    lock_release
    trap - EXIT HUP INT TERM
    return 0
}

FORCE=0
CHECK=0
while (($# > 0)); do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        --check)
            CHECK=1
            shift
            ;;
        --rebuild)
            FORCE=1
            shift
            ;;
        --)
            shift
            break
            ;;
        *)
            break
            ;;
    esac
done

if ((CHECK == 1)) && (($# > 0)); then
    printf 'error: --check does not accept TUI arguments\n' >&2
    exit 2
fi

toolchain_ok || exit 3

if ((CHECK == 1)); then
    want="$(fingerprint)" || die 6 "cannot compute build fingerprint"
    if is_current "$want"; then
        printf 'current: %s\n' "$BIN"
        printf 'fingerprint: %s\n' "$want"
        exit 0
    fi
    if [[ -x "$BIN" ]]; then
        printf 'stale: sources/toolchain/build settings changed\n'
    else
        printf 'stale: binary is missing\n'
    fi
    printf 'run ./START.sh --rebuild to rebuild\n'
    exit 1
fi

if ((FORCE == 0)); then
    want="$(fingerprint)" || die 6 "cannot compute build fingerprint"
    if is_current "$want"; then
        exec "$BIN" "$@"
    fi
fi

build_binary
exec "$BIN" "$@"
