#!/usr/bin/env bash
set -euo pipefail

package_version="${VERSION:-3.0.0}"
repository_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="${repository_dir}/dist"

mkdir -p "${dist_dir}"

for arch in amd64 arm64; do
    echo "===> Packaging Windows (${arch})..."
    staging_dir="${dist_dir}/windows/staging_${arch}/shareto"
    rm -rf "${dist_dir}/windows/staging_${arch}"
    mkdir -p "${staging_dir}"

    bin_dir="${dist_dir}/shareto-windows-${arch}"
    bin_src="${bin_dir}/shareto.exe"

    if [ ! -f "${bin_src}" ]; then
        echo "Building windows/${arch} binary..."
        mkdir -p "${bin_dir}"
        CGO_ENABLED=0 GOOS=windows GOARCH="${arch}" GOTOOLCHAIN=local go build \
            -trimpath \
            -ldflags="-s -w -X main.Version=${package_version}" \
            -o "${bin_src}" "${repository_dir}/cmd/shareto"
    fi

    cp "${bin_src}" "${staging_dir}/shareto.exe"
    cp "${repository_dir}/packaging/install-windows.ps1" "${staging_dir}/install.ps1"
    cp "${repository_dir}/packaging/uninstall-windows.ps1" "${staging_dir}/uninstall.ps1"
    cp "${repository_dir}/README.md" "${staging_dir}/README.md"
    cp "${repository_dir}/docs/WINDOWS.md" "${staging_dir}/WINDOWS.md"
    cp "${repository_dir}/LICENSE" "${staging_dir}/LICENSE"

    zip_file="${dist_dir}/shareto_${package_version}_windows_${arch}.zip"
    rm -f "${zip_file}"
    (cd "${dist_dir}/windows/staging_${arch}" && zip -q -r -9 "${zip_file}" shareto)
    echo "Created: ${zip_file}"
    rm -rf "${dist_dir}/windows/staging_${arch}"
done

cd "${dist_dir}"
sha256sum "shareto_${package_version}_windows_amd64.zip" \
          "shareto_${package_version}_windows_arm64.zip" > sha256sums-windows.txt
echo "Checksums written to dist/sha256sums-windows.txt"
