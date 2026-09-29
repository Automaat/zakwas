#!/bin/bash
set -euo pipefail

repo=""
dir="${ZAKWAS_DIR:-$HOME/dotfiles}"
version="${ZAKWAS_VERSION:-latest}"
apply=1

info() { printf '\033[1;33m==> %s\033[0m\n' "$1"; }
die() { printf '\033[1;31mzakwas: %s\033[0m\n' "$1" >&2; exit 1; }

usage() {
    cat <<'USAGE'
Installs Xcode CLI tools, Homebrew, mise and zakwas, clones your config repo
and applies it.

Usage: install.sh [--repo URL] [--dir DIR] [--version X.Y.Z] [--no-apply]

  --repo URL        config repo to clone (skipped when DIR already exists)
  --dir DIR         where the config lives (default: ~/dotfiles, $ZAKWAS_DIR)
  --version X.Y.Z   zakwas release to use (default: latest, $ZAKWAS_VERSION)
  --no-apply        install everything, but don't run `zakwas apply`
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

    if ! xcode-select -p &>/dev/null; then
        info "Installing Xcode Command Line Tools (finish the dialog, then press any key)"
        xcode-select --install
        read -r -n 1 -s
    fi

    if ! command -v brew &>/dev/null; then
        if [ ! -x /opt/homebrew/bin/brew ] && [ ! -x /usr/local/bin/brew ]; then
            info "Installing Homebrew"
            /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
        fi
        if [ -x /opt/homebrew/bin/brew ]; then
            eval "$(/opt/homebrew/bin/brew shellenv)"
        else
            eval "$(/usr/local/bin/brew shellenv)"
        fi
    fi

    if ! command -v mise &>/dev/null; then
        info "Installing mise"
        brew install mise
    fi

    # mise hides releases younger than its minimum release age from
    # "latest", so a fresh release would fail to resolve.
    if [ "$version" = latest ]; then
        version=$(curl -fsSL https://api.github.com/repos/Automaat/zakwas/releases/latest |
            sed -n 's/^ *"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p')
        [ -n "$version" ] || die "can't find the latest zakwas release"
    fi

    info "Installing zakwas $version"
    mise install "github:Automaat/zakwas@$version"

    if [ ! -d "$dir" ]; then
        [ -n "$repo" ] || die "$dir doesn't exist; pass --repo to clone your config"
        info "Cloning $repo to $dir"
        mkdir -p "$(dirname "$dir")"
        git clone "$repo" "$dir"
    fi
    [ -f "$dir/zakwas.yaml" ] || die "no zakwas.yaml in $dir"

    # mise refuses to run from a directory whose mise.toml isn't trusted.
    if [ -f "$dir/mise.toml" ]; then
        mise trust --yes "$dir/mise.toml"
    fi

    if [ "$apply" = 1 ]; then
        info "Converging the machine"
        cd "$dir"
        mise exec "github:Automaat/zakwas@$version" -- zakwas apply
        info "Done. Open a new terminal."
    else
        info "Ready. Run: cd $dir && mise exec github:Automaat/zakwas@$version -- zakwas apply"
    fi
}

main "$@"; exit
