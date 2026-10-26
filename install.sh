#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root (e.g. using sudo)." >&2
    exit 1
fi

echo "===> Setting up shareto APT repository..."

# Install prerequisite tools
apt-get update -y
apt-get install -y --no-install-recommends ca-certificates curl gnupg

# Add GPG keyring
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://huadaox.github.io/shareto/key.gpg | gpg --dearmor --yes -o /etc/apt/keyrings/shareto.gpg
chmod a+r /etc/apt/keyrings/shareto.gpg

# Add sources list
echo "deb [signed-by=/etc/apt/keyrings/shareto.gpg] https://huadaox.github.io/shareto stable main" | tee /etc/apt/sources.list.d/shareto.list > /dev/null

echo "===> Installing shareto..."
apt-get update -y
apt-get install -y shareto

echo ""
echo "Successfully installed shareto!"
echo "Run 'shareto init' to get started."
