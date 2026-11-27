#!/bin/sh
# Build a development Fennec.app from one commit and install it on this Mac.
#
#   scripts/dev-install-mac.sh [--force] [--build-only] [--tag FEN-<n>] [<commit>]
#
# <commit> defaults to origin/main. A ref (origin/main) is resolved after the
# build lock is taken, so a request that waited behind another build installs
# the newest main at that moment.
#
# Every build runs in one fixed worktree (FENNEC_DEV_BUILD_DIR, default
# ~/repos/runta-dev/fennec-build) checked out with `git checkout --detach`.
# Cargo path dependencies (desktop/vendor/*) fingerprint their absolute path
# and file mtimes, so a fresh worktree per build recompiles all of them,
# ghostty's zig build included; a fixed path rebuilds only what changed.
# Don't set CARGO_TARGET_DIR.
#
# Only one build runs at a time: the script holds an flock(2) lock (released by
# the kernel when the script dies, so it never goes stale) and waits for it.
# When the installed build already contains <commit>, it stops without
# rebuilding or restarting the app (--force rebuilds anyway). --build-only
# builds and checks the signature but leaves the installed app alone (for
# compile checks on the Mac); the bundle is in ~/repos/runta-dev/fennec-build/build.
#
# Steps: lock, fetch, checkout, build, codesign check, back up (keeping the
# newest 5 backups), install, open, verify. Stopping the app drops the cloud agents' ssh relay, so start this
# detached and poll the log; the last line is INSTALL_OK, SKIP or FAILED:
#
#   nohup scripts/dev-install-mac.sh --tag FEN-148 <sha> \
#     > /tmp/fennec-dev-install-FEN-148.log 2>&1 < /dev/null &
set -eu

main() {
  force=0
  build_only=0
  tag=dev
  while [ $# -gt 0 ]; do
    case "$1" in
      --force) force=1 ;;
      --build-only) build_only=1 ;;
      --tag) tag=$(printf '%s' "$2" | tr 'A-Z' 'a-z'); shift ;;
      -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
      -*) fail "unknown option $1" ;;
      *) break ;;
    esac
    shift
  done
  want=${1:-origin/main}

  export PATH="/opt/homebrew/bin:/usr/local/go/bin:$HOME/go/bin:$HOME/.cargo/bin:$HOME/.local/bin:$PATH"
  source_repo=${FENNEC_DEV_SOURCE_REPO:-$HOME/repos/runta-dev/fennec}
  build_dir=${FENNEC_DEV_BUILD_DIR:-$HOME/repos/runta-dev/fennec-build}
  state_dir=${FENNEC_DEV_STATE_DIR:-$HOME/.cache/fennec-dev-install}
  installed_app=/Applications/Fennec.app
  mkdir -p "$state_dir"
  started=$(date +%s)

  acquire_lock
  step "lock held (waited $(( $(date +%s) - started ))s)"

  git -C "$source_repo" fetch --quiet origin
  sha=$(git -C "$source_repo" rev-parse --verify "$want^{commit}") || fail "unknown commit $want"
  step "commit $(git -C "$source_repo" log -1 --format='%h %s' "$sha")"

  installed_sha=$(cat "$state_dir/installed-commit" 2>/dev/null || true)
  installed_id=$(cat "$state_dir/installed-build-id" 2>/dev/null || true)
  current_id=$(cat "$installed_app/Contents/Resources/control-build-id" 2>/dev/null || true)
  if [ "$force" = 0 ] && [ "$build_only" = 0 ] && [ -n "$installed_sha" ] && [ "$installed_id" = "$current_id" ]; then
    if git -C "$source_repo" merge-base --is-ancestor "$sha" "$installed_sha" 2>/dev/null; then
      echo "SKIP: the installed build ($installed_sha) already contains $sha"
      exit 0
    fi
    if ! git -C "$source_repo" merge-base --is-ancestor "$installed_sha" "$sha" 2>/dev/null; then
      fail "$sha does not contain the installed build $installed_sha; installing it would drop those changes (pass --force to do it anyway)"
    fi
  fi

  checkout_build_dir
  build
  check_signature
  if [ "$build_only" = 1 ]; then
    printf '%s %s %s build-only build=%ss\n' "$(date '+%F %T')" "$sha" "$tag" "$build_seconds" >> "$state_dir/history.log"
    echo "BUILD_ONLY_OK $sha $version (build ${build_seconds}s)"
    exit 0
  fi
  install_app
  verify
  printf '%s\n' "$sha" > "$state_dir/installed-commit"
  cat "$installed_app/Contents/Resources/control-build-id" > "$state_dir/installed-build-id"
  total=$(( $(date +%s) - started ))
  printf '%s %s %s build=%ss total=%ss\n' "$(date '+%F %T')" "$sha" "$tag" "$build_seconds" "$total" >> "$state_dir/history.log"
  echo "INSTALL_OK $sha $version (build ${build_seconds}s, total ${total}s)"
}

step() { printf '[%s] %s\n' "$(date +%T)" "$*"; }
fail() { echo "FAILED: $*" >&2; exit 1; }

acquire_lock() {
  lock="$state_dir/build.lock"
  # fd 9 stays open for the script's lifetime; perl takes flock(2) on the
  # shared open file description, so the lock outlives perl and is dropped
  # by the kernel when this script exits or is killed.
  exec 9>>"$lock"
  waited=0
  until perl -MFcntl=:flock -e 'open(my $f, ">&=", 9) or die "$!"; flock($f, LOCK_EX | LOCK_NB) or exit 1'; do
    if [ $((waited % 60)) = 0 ]; then
      step "waiting for another build: $(cat "$lock.owner" 2>/dev/null || echo unknown)"
    fi
    sleep 10
    waited=$((waited + 10))
    [ "$waited" -lt "${FENNEC_DEV_LOCK_TIMEOUT:-5400}" ] || fail "gave up waiting for the build lock after ${waited}s"
  done
  printf 'pid %s, %s, %s, since %s\n' "$$" "$tag" "$want" "$(date '+%F %T')" > "$lock.owner"
}

checkout_build_dir() {
  if [ ! -e "$build_dir/.git" ]; then
    step "creating the build worktree $build_dir"
    git -C "$source_repo" worktree prune
    git -C "$source_repo" worktree add --detach "$build_dir" "$sha"
    # Recompiling the credential helper changes its Keychain identity, so
    # carry the cached, signed helper over byte for byte.
    if [ -d "$source_repo/build/credential-helper-cache" ]; then
      mkdir -p "$build_dir/build"
      ditto "$source_repo/build/credential-helper-cache" "$build_dir/build/credential-helper-cache"
    fi
  fi
  # --force discards edits someone left in the build worktree. Only files that
  # differ between the two commits are rewritten, so unchanged sources keep
  # their mtimes and cargo keeps their fingerprints.
  git -C "$build_dir" checkout --quiet --detach --force "$sha"
  # build-macos.sh updates the submodules; that is a no-op when their
  # commits didn't change, so the vendored sources keep their mtimes too.
  step "checked out $sha in $build_dir"
}

build() {
  desktop=${FENNEC_BUILD_DESKTOP:-}
  if [ -z "$desktop" ]; then
    desktop=0
    [ -e "$installed_app/Contents/MacOS/fennec-desktop" ] && desktop=1
  fi
  step "building (FENNEC_BUILD_DESKTOP=$desktop)"
  build_started=$(date +%s)
  cd "$build_dir"
  # A leftover bundle must never pass for a fresh build.
  rm -rf build/Fennec.app
  go build -trimpath -o "$state_dir/fennec-daemon" . 9>&-
  FENNEC_BUILD_DESKTOP=$desktop FENNEC_DAEMON_BINARY="$state_dir/fennec-daemon" ./scripts/build-macos.sh 9>&-
  build_seconds=$(( $(date +%s) - build_started ))
  step "BUILD_OK in ${build_seconds}s"
}

check_signature() {
  app="$build_dir/build/Fennec.app"
  codesign --verify --deep --strict "$app" || fail "codesign --verify failed"
  codesign -dvv "$app" 2>&1 | grep -q '^Authority=Fennec Local Development$' ||
    fail "$app is not signed by Fennec Local Development"
  if [ "$desktop" = 1 ]; then
    [ -x "$app/Contents/MacOS/fennec-desktop" ] || fail "fennec-desktop missing from the bundle"
  fi
  version=$(defaults read "$app/Contents/Info.plist" CFBundleShortVersionString)
  step "signature ok, version $version"
}

install_app() {
  if [ -d "$installed_app" ]; then
    backup="/Applications/Fennec.before-$tag-$(date +%m%d).app"
    n=2
    while [ -e "$backup" ]; do
      backup="/Applications/Fennec.before-$tag-$(date +%m%d)-$n.app"
      n=$((n + 1))
    done
    ditto "$installed_app" "$backup"
    step "backed up to $backup"
    # Every install leaves a ~110 MB backup; keep only the newest few
    # (FENNEC_DEV_KEEP_BACKUPS, default 5). Older dev builds can be rebuilt.
    # Order by inode change time: ditto keeps the source bundle's mtime and
    # birth time, so those say when the app was built, not backed up.
    stat -f '%c %N' /Applications/Fennec.before-*.app 2>/dev/null | sort -rn |
      tail -n +$(( ${FENNEC_DEV_KEEP_BACKUPS:-5} + 1 )) | cut -d ' ' -f 2- |
      while IFS= read -r old; do rm -rf "$old"; done
  fi
  # Only the installed app's own processes: not the iOS Simulator's Fennec, not
  # herdr's hc. A menu-bar app updated in-app runs with extra arguments.
  pids=$(pgrep -f '^/Applications/Fennec.app/Contents/MacOS/(Fennec|fennec-desktop)( |$)' || true)
  if [ -n "$pids" ]; then
    step "stopping $(echo $pids)"
    kill -TERM $pids 2>/dev/null || true
    i=0
    while [ $i -lt 20 ] && pgrep -f '^/Applications/Fennec.app/Contents/MacOS/(Fennec|fennec-desktop)( |$)' >/dev/null; do
      sleep 1
      i=$((i + 1))
    done
  fi
  rm -rf "$installed_app"
  ditto "$app" "$installed_app"
  open "$installed_app" 9>&-
  step "installed and opened"
}

verify() {
  expected=$(cat "$installed_app/Contents/Resources/control-build-id")
  i=0
  until "$installed_app/Contents/MacOS/fen" daemon status 2>/dev/null | grep -q "$expected"; do
    [ $i -lt 90 ] || fail "daemon build_id did not become $expected"
    sleep 2
    i=$((i + 1))
  done
  pgrep -f '^/Applications/Fennec.app/Contents/MacOS/Fennec( |$)' >/dev/null || fail "Fennec is not running"
  step "daemon build_id matches, Fennec running"
  connector=$(cat "$HOME/.config/fennec/connector-status.json" 2>/dev/null | tr -d '\n' | cut -c1-200 || true)
  step "connector-status: $connector"
}

main "$@"
