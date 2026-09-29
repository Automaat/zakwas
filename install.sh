#!/bin/bash
set -euo pipefail

repo=""
dir="${ZAKWAS_DIR:-$HOME/dotfiles}"
version="${ZAKWAS_VERSION:-latest}"
bin_dir="$HOME/.local/bin"
tmp=""
apply=1

info() { printf '\033[1;33m==> %s\033[0m\n' "$1"; }
die() { printf '\033[1;31mzakwas: %s\033[0m\n' "$1" >&2; exit 1; }

usage() {
    cat <<'USAGE'
Installs zakwas to ~/.local/bin, clones your config repo and applies it.
zakwas installs Homebrew and mise itself when the config uses them.

Usage: install.sh [--repo URL] [--dir DIR] [--version X.Y.Z] [--no-apply]

  --repo URL        config repo to clone (skipped when DIR already exists)
  --dir DIR         where the config lives (default: ~/dotfiles, $ZAKWAS_DIR)
  --version X.Y.Z   zakwas release to install (default: latest, $ZAKWAS_VERSION)
  --no-apply        install zakwas and clone, but don't run `zakwas apply`
USAGE
}

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --repo) repo="${2:?--repo needs a URL}"; shift 2 ;;
            --dir) dir="${2:?--dir needs a path}"; shift 2 ;;
            --version) version="${2:?--version needs a version}"; shift 2 ;;
            --no-apply) apply=0; shift ;;
            -h|--help) usage; exit 0 ;;
            *) usage >&2; exit 64 ;;
        esac
    done
}

install_zakwas() {
    if [ "$version" = latest ]; then
        version=$(curl -fsSL https://api.github.com/repos/Automaat/zakwas/releases/latest |
            sed -n 's/^ *"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p')
        [ -n "$version" ] || die "can't find the latest zakwas release"
    fi
    local arch archive base
    arch=$(uname -m)
    [ "$arch" = x86_64 ] && arch=amd64
    archive="zakwas_${version}_darwin_${arch}.tar.gz"
    base="https://github.com/Automaat/zakwas/releases/download/v$version"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    info "Installing zakwas $version to $bin_dir"
    curl -fsSL -o "$tmp/$archive" "$base/$archive"
    curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
    (cd "$tmp" && grep "  $archive\$" checksums.txt | shasum -a 256 -c -s) ||
        die "checksum mismatch for $archive"
    tar -xzf "$tmp/$archive" -C "$tmp" zakwas
    mkdir -p "$bin_dir"
    mv "$tmp/zakwas" "$bin_dir/zakwas"
}

main() {
    parse_args "$@"
    [ "$(uname -s)" = Darwin ] || die "zakwas only supports macOS"

    # Piped as `curl … | bash`, stdin is the script itself: prompts would
    # swallow script text. The script lives in main, called on the last
    # line, so bash has parsed all of it before stdin moves to the terminal.
    # CI has a /dev/tty node that can't be opened, hence the trial redirect.
    if [ ! -t 0 ] && { : </dev/tty; } 2>/dev/null; then
        exec </dev/tty
    fi

    # git ships with the Command Line Tools, and Homebrew needs them too.
    if ! xcode-select -p &>/dev/null; then
        info "Installing Xcode Command Line Tools (finish the dialog, then press any key)"
        xcode-select --install
        read -r -n 1 -s
    fi

    install_zakwas

    if [ ! -d "$dir" ]; then
        [ -n "$repo" ] || die "$dir doesn't exist; pass --repo to clone your config"
        info "Cloning $repo to $dir"
        mkdir -p "$(dirname "$dir")"
        git clone "$repo" "$dir"
    fi
    [ -f "$dir/zakwas.yaml" ] || die "no zakwas.yaml in $dir"

    if [ "$apply" = 1 ]; then
        info "Converging the machine"
        cd "$dir"
        "$bin_dir/zakwas" apply
        info "Done. Open a new terminal."
    else
        info "Ready. Run: cd $dir && $bin_dir/zakwas apply"
    fi
}

main "$@"; exit
