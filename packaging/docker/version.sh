#!/usr/bin/env bash
set -euo pipefail

if [ "${REF_TYPE}" = tag ]; then
    image_version="${REF_NAME#v}"
else
    image_version="$(make -s print-version)"
fi
echo "value=${image_version}" >> "${GITHUB_OUTPUT}"
