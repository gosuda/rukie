#!/usr/bin/env bash
set -euo pipefail

test "$(uname -m)" = x86_64
test "$(id -u)" -ne 0 || { echo 'The sandbox probe requires a non-root runner' >&2; exit 1; }
sudo -n apt-get update
sudo -n apt-get install -y --no-install-recommends \
  build-essential pkg-config libseccomp-dev libsystemd-dev libbtrfs-dev \
  libgpgme-dev libassuan-dev libdevmapper-dev libglib2.0-dev libsqlite3-dev \
  libapparmor-dev podman uidmap catatonit

podman_revision=464c15fec06f0074c916f9eff87c4806869c2833
podman_source="$RUNNER_TEMP/podman-source"
git init "$podman_source"
git -C "$podman_source" remote add origin https://github.com/podman-container-tools/podman.git
git -C "$podman_source" fetch --no-tags --depth=1 origin "$podman_revision"
git -C "$podman_source" checkout --detach FETCH_HEAD
test "$(git -C "$podman_source" rev-parse HEAD)" = "$podman_revision"
make -C "$podman_source" bin/podman
sudo -n install -m 0755 "$podman_source/bin/podman" /usr/bin/podman
test "$(/usr/bin/podman --version)" = 'podman version 6.1.4'

curl --fail --location --retry 2 --max-time 120 \
  https://github.com/opencontainers/runc/releases/download/v1.5.3/runc.amd64 \
  -o "$RUNNER_TEMP/runc.amd64"
printf '%s  %s\n' 50dcc0b257d724fe07f3852c2dda52340223f038227c7572644eec870741223e "$RUNNER_TEMP/runc.amd64" | sha256sum -c -
sudo -n install -D -m 0755 "$RUNNER_TEMP/runc.amd64" /usr/local/libexec/rukie-ci/runc

runner_user=$(id -un)
runner_gid=$(id -g)
grep -q "^$runner_user:" /etc/subuid || printf '%s:165536:65536\n' "$runner_user" | sudo -n tee -a /etc/subuid
grep -q "^$runner_user:" /etc/subgid || printf '%s:165536:65536\n' "$runner_user" | sudo -n tee -a /etc/subgid
passwd_gid=$(awk -F: -v u="$runner_user" '$1==u {print $4}' /etc/passwd)
if [ "$passwd_gid" != "$runner_gid" ]; then
  sudo -n sed -i -E "s/^($runner_user:[^:]*:[0-9]+:)[0-9]+:/\1$runner_gid:/" /etc/passwd
fi
mkdir -p "$HOME/.config/containers" "$RUNNER_TEMP/podman-run" "$RUNNER_TEMP/podman-root"
cat > "$HOME/.config/containers/storage.conf" <<EOF
[storage]
driver = "vfs"
runroot = "$RUNNER_TEMP/podman-run"
graphroot = "$RUNNER_TEMP/podman-root"
EOF
cat > "$HOME/.config/containers/containers.conf" <<'EOF'
[engine]
runtime = "rukie-ci-runc"
[engine.runtimes]
rukie-ci-runc = ["/usr/local/libexec/rukie-ci/runc"]
EOF
podman --storage-driver vfs version --format json
