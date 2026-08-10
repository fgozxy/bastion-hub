# Container Restic Backups

This is a minimal replacement for large tarball backups through NodePanel. It
backs up each Docker container as its own restic snapshot, so snapshots can be
listed and restored per container.

## Model

Each configured container gets one snapshot per run. Every snapshot has tags:

```text
type:docker-container
node:<NODE_NAME>
container:<container-name>
project:<compose-project>
service:<compose-service>
```

The snapshot includes the container's persistent Docker mount sources, plus a
metadata directory containing `docker inspect`, the generated container config,
Compose files when Docker labels expose them, and `manifest.json`.

## Install On A Node

```bash
install -d -m 0700 /root/container-restic
install -m 0700 /root/docker/nodepanel/deploy/container-restic/container-restic.sh /root/container-restic/container-restic.sh
/root/container-restic/container-restic.sh init
/root/container-restic/container-restic.sh discover
```

Then edit `/root/container-restic/restic.env` and set `RESTIC_REPOSITORY` plus
the backend credentials. For S3-compatible storage, restic expects standard AWS
environment variables in that file.

Initialize the repository once:

```bash
source /root/container-restic/restic.env
restic init
```

Run one backup manually:

```bash
/root/container-restic/container-restic.sh backup nodepanel
```

Run all enabled containers:

```bash
/root/container-restic/container-restic.sh backup-all
```

List snapshots for one container:

```bash
/root/container-restic/container-restic.sh snapshots nodepanel
```

Restore one container snapshot into a staging directory:

```bash
/root/container-restic/container-restic.sh restore nodepanel latest
```

The restore command intentionally restores to `/root/container-restic/restores/`
only. Review the files and metadata before copying data back over live Docker
mounts.

## Enable Timer

```bash
install -m 0644 /root/docker/nodepanel/deploy/container-restic/systemd/container-restic.service /etc/systemd/system/container-restic.service
install -m 0644 /root/docker/nodepanel/deploy/container-restic/systemd/container-restic.timer /etc/systemd/system/container-restic.timer
systemctl daemon-reload
systemctl enable --now container-restic.timer
```

Do not enable the timer until `restic init` and a manual backup have succeeded.

## Container Overrides

Per-container configs live in `/root/container-restic/containers.d/*.env`.
Useful fields:

```bash
ENABLED=1
EXTRA_PATHS="/path/one
/path/two"
EXCLUDE_PATTERNS="cache
