#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${repo_root}/internal/goremote/payloads"
mkdir -p "${out_dir}"

variant="${WATER_APP_VARIANT:-dev}"
version="${WATER_APP_VERSION:-go-rewrite}"
identity_pkg="github.com/SurTeam/Water/internal/gobuild"
ldflags="-s -w -X ${identity_pkg}.Variant=${variant} -X ${identity_pkg}.Version=${version}"

targets=(
  "darwin arm64"
  "darwin amd64"
  "linux arm64"
  "linux amd64"
)

for target in "${targets[@]}"; do
  read -r goos goarch <<<"${target}"
  out="${out_dir}/water-server-${goos}-${goarch}"
  echo "building ${goos}/${goarch} -> ${out}"
  (
    cd "${repo_root}"
    CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
      go build -trimpath -ldflags="${ldflags}" -o "${out}" ./cmd/water-server
  )
done

echo "embedded Go server payloads ready in ${out_dir}"
