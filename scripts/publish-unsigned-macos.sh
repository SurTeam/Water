#!/usr/bin/env bash
set -euo pipefail

# Stage a locally built unsigned macOS app in a draft GitHub release and
# dispatch the signing-only workflow. This script never reads or uploads the
# signing certificate; the certificate stays in GitHub Actions secrets.

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

variant="${WATER_APP_VARIANT:-release}"
case "$variant" in
  release)
    app_name='Water'
    default_tag_prefix='v'
    default_publication='release'
    ;;
  dev)
    app_name='Water Dev'
    default_tag_prefix='dev-'
    default_publication='prerelease'
    ;;
  *)
    echo "error: WATER_APP_VARIANT must be dev or release" >&2
    exit 1
    ;;
esac

publication="${WATER_RELEASE_PUBLICATION:-$default_publication}"
case "$publication" in
  draft|none) ;;
  prerelease)
    [[ "$variant" == dev ]] || {
      echo "error: prerelease is reserved for the dev variant" >&2
      exit 1
    }
    ;;
  release)
    [[ "$variant" == release ]] || {
      echo "error: release is reserved for the release variant" >&2
      exit 1
    }
    ;;
  *)
    echo "error: WATER_RELEASE_PUBLICATION must be release, prerelease, draft, or none" >&2
    exit 1
    ;;
esac

version="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
[[ -n "$version" ]] || { echo "error: Cargo.toml version is empty" >&2; exit 1; }
asset="${app_name}-${version}-macOS-arm64.zip"
# GitHub normalizes spaces in release asset names to periods. Keep the local
# archive path unchanged, but pass the server-side name to the signing action.
release_asset="${asset// /.}"
asset_path="$root_dir/dist/$asset"
test -s "$asset_path" || {
  echo "error: missing unsigned archive $asset_path" >&2
  echo "hint: run CODESIGN_SKIP=1 WATER_APP_VARIANT=$variant bash scripts/build-macos-app.sh" >&2
  exit 1
}
unzip -tq "$asset_path" >/dev/null

if [[ -n "${WATER_RELEASE_TAG:-}" ]]; then
  release_tag="$WATER_RELEASE_TAG"
elif [[ "$variant" == release ]]; then
  release_tag="${default_tag_prefix}${version}"
else
  release_tag="${default_tag_prefix}$(date -u +%Y%m%d-%H%M)"
fi
[[ "$release_tag" =~ ^[A-Za-z0-9._-]+$ ]] || {
  echo "error: release tag contains unsupported characters" >&2
  exit 1
}

repository="${GITHUB_REPOSITORY:-$(gh repo view --json nameWithOwner --jq '.nameWithOwner')}"
[[ -n "$repository" ]] || { echo "error: could not determine GitHub repository" >&2; exit 1; }

if ! git ls-remote --exit-code --tags origin "refs/tags/$release_tag" >/dev/null 2>&1; then
  echo "error: remote tag $release_tag does not exist; create and push the tag before staging the app" >&2
  exit 1
fi

release_is_draft="$(gh release view "$release_tag" --repo "$repository" --json isDraft --jq '.isDraft' 2>/dev/null || true)"
if [[ -n "$release_is_draft" ]]; then
  [[ "$release_is_draft" == true ]] || {
    echo "error: release $release_tag already exists and is not a draft" >&2
    exit 1
  }
  gh release upload "$release_tag" "$asset_path" --repo "$repository" --clobber
else
  gh release create "$release_tag" "$asset_path" \
    --repo "$repository" \
    --draft \
    --title "Unsigned transport: $app_name $version" \
    --notes "Temporary unsigned archive. The signing workflow replaces this asset before publication."
fi

gh workflow run macos-signed.yml \
  --repo "$repository" \
  --ref main \
  -f "variant=$variant" \
  -f "publication=$publication" \
  -f "tag=$release_tag" \
  -f "source_release=$release_tag" \
  -f "source_asset=$release_asset"

echo "Staged unsigned $app_name.app as $repository#$release_tag"
echo "Dispatched signing workflow for $release_tag"
