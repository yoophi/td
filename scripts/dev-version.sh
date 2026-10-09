#!/bin/sh
# Development versions carry an actual ancestor release, never a made-up release.
set -eu
root=${1:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)}
set --
for tag in $(git -C "$root" tag --merged HEAD | LC_ALL=C grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true); do
  set -- "$@" --match "$tag"
done
base=v0.0.0
if [ "$#" -gt 0 ]; then
  base=$(git -C "$root" describe --tags --abbrev=0 "$@" HEAD)
fi
branch=$(git -C "$root" branch --show-current)
[ -n "$branch" ] || branch=detached
branch=$(printf '%s' "$branch" | LC_ALL=C tr -cs 'A-Za-z0-9-' '-')
revision=$(git -C "$root" rev-parse --short HEAD)
dirty=
[ -z "$(git -C "$root" status --porcelain --untracked-files=normal)" ] || dirty=.dirty
printf '%s+devel.%s.%s%s\n' "$base" "$branch" "$revision" "$dirty"
