#!/usr/bin/env bash
# Build clctl from this checkout and install it (default: ~/.local/bin/clctl), plus its
# tab completion for bash (and zsh/fish when installed), so completion always matches
# the binary. Completion asks clctl itself for commands and task IDs on every Tab.
# --alias NAME (repeatable), or CLCTL_ALIASES="cl ..." in the environment, also
# completes a shell alias of clctl such as `alias cl=clctl` (the alias itself goes in
# your shell profile).
# Usage: scripts/install.sh [install-dir] [--alias NAME]...
# To install a release without a checkout: go install github.com/JB0t/clctl@latest

set -euo pipefail

SRC_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${CLCTL_INSTALL_DIR:-$HOME/.local/bin}"
ALIAS_FLAGS=()
for alias in ${CLCTL_ALIASES:-}; do
	ALIAS_FLAGS+=(--alias "$alias")
done
while [ $# -gt 0 ]; do
	case "$1" in
	--alias)
		[ $# -ge 2 ] || { printf -- '--alias needs a name\n' >&2; exit 2; }
		ALIAS_FLAGS+=(--alias "$2")
		shift 2
		;;
	--alias=*)
		ALIAS_FLAGS+=(--alias "${1#--alias=}")
		shift
		;;
	-h | --help)
		sed -n '2,9p' "$0"
		exit 0
		;;
	*)
		INSTALL_DIR="$1"
		shift
		;;
	esac
done

if ! command -v go >/dev/null 2>&1; then
	printf 'go is required to build clctl.\n' >&2
	exit 1
fi

# The newest vX.Y.Z tag, as v1.2.0, or v1.2.0-3-gabc1234[-dirty] for later commits;
# dev-<commit> before the first release.
VERSION="$(git -C "$SRC_DIR" describe --tags --match 'v[0-9]*' --dirty 2>/dev/null || true)"
if [ -z "$VERSION" ]; then
	VERSION="dev-$(git -C "$SRC_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
fi

mkdir -p "$INSTALL_DIR"
cd "$SRC_DIR"
go build -trimpath -ldflags "-X github.com/JB0t/clctl/cmd.version=$VERSION" -o "$INSTALL_DIR/clctl" .

printf 'Installed clctl %s to %s\n' "$VERSION" "$INSTALL_DIR/clctl"

# Tab completion. bash-completion loads this file on the first Tab after "clctl" in any
# new shell, unless your shell profile registers its own completion for clctl.
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}"
BASH_COMPLETION="$DATA_DIR/bash-completion/completions/clctl"
mkdir -p "$(dirname "$BASH_COMPLETION")"
"$INSTALL_DIR/clctl" completion bash ${ALIAS_FLAGS[@]+"${ALIAS_FLAGS[@]}"} >"$BASH_COMPLETION"
printf 'Installed bash completion to %s\n' "$BASH_COMPLETION"
if command -v zsh >/dev/null 2>&1; then
	ZSH_COMPLETION="$DATA_DIR/zsh/site-functions/_clctl"
	mkdir -p "$(dirname "$ZSH_COMPLETION")"
	"$INSTALL_DIR/clctl" completion zsh ${ALIAS_FLAGS[@]+"${ALIAS_FLAGS[@]}"} >"$ZSH_COMPLETION"
	printf 'Installed zsh completion to %s (needs that folder in fpath before compinit)\n' "$ZSH_COMPLETION"
fi
if command -v fish >/dev/null 2>&1; then
	FISH_COMPLETION="${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions/clctl.fish"
	mkdir -p "$(dirname "$FISH_COMPLETION")"
	"$INSTALL_DIR/clctl" completion fish ${ALIAS_FLAGS[@]+"${ALIAS_FLAGS[@]}"} >"$FISH_COMPLETION"
	printf 'Installed fish completion to %s\n' "$FISH_COMPLETION"
fi
if [ ${#ALIAS_FLAGS[@]} -gt 0 ]; then
	printf 'Completion also registered for the alias(es):'
	printf ' %s' "${ALIAS_FLAGS[@]}" | sed 's/ --alias//g'
	printf ' (put e.g. "alias cl=clctl" in your shell profile)\n'
fi
printf 'Open a new shell for completion. If it still offers old commands, run: complete -p clctl\n'
printf '(anything but "complete -o default -F __start_clctl clctl" is a stale registration in your shell profile)\n'

