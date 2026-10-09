# PaNasMs Cloud Sync module

Cloud Sync is the [PaNasMs](https://github.com/PaNasMs/panasms) module that keeps
NAS folders in sync with Google Drive and Dropbox folders. PaNasMs is a browser
panel for managing a NAS on Debian-based Linux. Each user can connect several
accounts per provider and run several independent folder pairs per account.
Project website: <https://panasms.github.io/>.

The current version is 0.1.24. It requires PaNasMs core `>=0.2.15,<0.3.0` and
module API 1, and is published for ARM64 and AMD64.

## Install

Open **Modules** in the PaNasMs panel and install Cloud Sync from the catalog.
The module manager picks the package for your architecture. Signed packages and
the catalog are in the [module registry](https://panasms.github.io/module-registry/);
an administrator can also upload a signed `.panasms` archive from there. The
manifest declares the system `rclone` package (1.60 or newer) as a dependency.

Cloud Sync is currently available to panel administrators only.

## Connect an account

### Google Drive

1. An administrator enters the NAS-wide Google OAuth client in the panel Settings
   once. The Google Drive API must be enabled in that Google Cloud project.
2. Link your Google account under **My profile**, **Linked accounts**. Linking
   for panel sign-in does not grant file access on its own.
3. In Cloud Sync, choose **Add connection**, select the account and authorize
   Drive access.

Google apps in the External Testing publishing status get Drive grants that
expire, usually after seven days. Production consent and app verification are
configured separately in Google Cloud.

### Dropbox

1. Set up the NAS Dropbox app with the
   [Dropbox setup guide](https://panasms.github.io/docs/setup/dropbox/). In the
   Dropbox App Console, enable `account_info.read`, `files.metadata.read`,
   `files.content.read` and `files.content.write`.
2. Link your Dropbox account under **My profile**, **Linked accounts**.
3. In Cloud Sync, choose **Add connection**, select Dropbox and approve offline
   file access.

A Dropbox app in Development status may need extra users enabled in the App
Console. Dropbox credentials imported by older versions need a reconnect. The
module removes the old credentials only after the new grant succeeds.

The provider permission covers the whole account. The task configuration limits
which folder Cloud Sync touches. Refresh tokens and OAuth client secrets stay
encrypted in the core; the module receives only short-lived access tokens.

## Create a sync task

The setup wizard has three steps. You select or connect an account, choose the
cloud folder, the NAS folder and the direction, then review and start. Adding a
task to an existing account skips the first step. Connecting an account does not
start a transfer.

The NAS folder picker starts from your home folder and the mounted data volumes,
loads branches on demand, hides duplicate mount aliases and disables unsupported
destinations. It can create a subfolder in a writable folder with your Linux
permissions and umask. To sync the whole Google Drive, select **My Drive**
explicitly.

Directions:

- **NAS to cloud** and **cloud to NAS** copy changes one way and do not delete
  anything at the destination. Replaced destination files are kept under
  `.panasms-cloud-versions`.
- **Two-way** propagates changes, including deletions, in both directions. One of
  the two folders must be empty for the first run. A large batch of deletions
  stops the task for review instead of being applied. Conflicting versions are
  kept.

Folder requirements:

- The NAS folder must be under `/home`, `/srv`, `/mnt` or `/media` on a supported
  Linux filesystem, writable by its owner, with no symbolic links in the path.
  Network mounts and FAT or NTFS volumes are not supported.
- Folders of different tasks must not overlap.
- Each user can have up to 32 tasks. A task supports up to 100,000 listing
  entries and 100,000 watched directories.

## Day-to-day use

The sidebar lists sync tasks with their provider. The selected task shows both
folders, the direction, the state, account actions and history on one page, and
its ID is kept in the `sync` URL parameter. Connections without tasks are listed
separately so you can add a task or remove them.

You can pause and resume a task, run it now, remove it and view its history.
Removing a task or a connection leaves the synchronized files in place. Each
user's worker runs one synchronization at a time; new tasks wait for the current
transfer. You can add accounts, browse cloud folders and create tasks while a
transfer runs, but you must pause transfers before reconnecting or removing an
existing account.

Local changes are picked up through inotify, including new subdirectories. Cloud
changes are polled with a change cursor and backoff, so an unchanged account does
not trigger repeated scans of the local tree. Registering watches and real changes
still read the disk, and other NAS services and running transfers can keep HDDs
from sleeping.

The UI is available in English, Russian and Ukrainian.

## Safety and recovery

### Permission checks

Each transfer rechecks access, normally every 30 seconds. Revoked access, an
error from the permission service, cancellation or a lost volume stops rclone and
reaps the process. A token the provider has already issued can stay valid at the
provider until it expires. Cloud access is rechecked while all tasks are paused,
too. A successful check clears old account errors without advancing the change
cursor of paused tasks. Failed checks back off exponentially up to 30 minutes.

### Long-running transfers

Each run uses a private rclone configuration and a loopback token relay. rclone's
[token URL override](https://rclone.org/drive/#drive-token-url) asks the core
broker for fresh access tokens with a random per-run capability, so transfers do
not restart when an access token rotates. A revoked grant still stops the
transfer.

### Missing volumes

When a task's local volume disappears, the task stops. Cloud Sync checks it every
30 seconds and resumes it only when the saved mount identity matches and the
original folder is accessible. The identity is the filesystem UUID plus the mount
point, so a disk the kernel renames between boots (for example from `/dev/sda1` to
`/dev/sdb1`) is still the same volume. Identities recorded by device name before
0.1.18 are upgraded automatically. If the device was already renamed, the task is
accepted only when its folder still holds the task's access marker. Filesystems
for which udev publishes no UUID keep the device-based identity. A different
volume is never accepted as a replacement, and Cloud Sync never creates folders to
stand in for a missing volume. Pausing a task manually cancels automatic recovery.

Temporary connection failures retry with a persisted exponential backoff from 1
to 30 minutes. Authorization retries never bypass a revoked permission; the owner
has to grant access again.

### Access marker

Two-way tasks create a small hidden `.panasms-cloud-access-<task-id>` file in both
folders. It lets the first run start from an empty folder and handle single-file
changes with the rclone 1.60 package from the distribution, and it enables
`--check-access` on later runs. Do not delete the marker. If it disappears, the
task stops instead of recreating it or propagating deletions.

### Two-way history recovery

An interrupted two-way run needs review; it is never reset silently to an initial
synchronization. When a two-way task loses its history, the next scheduled
recovery treats the cloud as the reference:

1. The module verifies the cloud access marker and the original local mount.
   Older initialized tasks without a marker get one first. If a task that had a
   marker now has a missing or invalid one, recovery stops for review. Network and
   authorization failures are never read as an empty cloud.
2. If the first synchronization was interrupted, the cloud has no marker yet. The
   module first uploads local files the cloud lacks, without replacing any cloud
   file.
3. The cloud state is copied to the local folder. Replaced and local-only files
   move to `.panasms-cloud-versions/recovery-<timestamp>-<id>` and are not
   uploaded. These backups are excluded from synchronization and kept for manual
   review. Recovery needs enough free local space and write permission.
4. Content checks and a dry-run resync build fresh history in a separate
   directory. The cloud listing must not change during this step. Snapshots
   compare file paths, sizes, times and hashes, and folder paths without folder
   times, because Dropbox keeps no folder times and reports the listing time.
5. The previous history is kept outside the active work directory, and normal
   two-way synchronization resumes. The cloud takes priority only during recovery.

Do not edit either folder during recovery. Pausing a task manually cancels
scheduled retries, and tasks that were paused before an upgrade stay paused. The
task details show the recovery state and the next retry time. A full quota,
filesystem permissions, a missing access marker and safety-limit stops still need
user action.

### Diagnostics

When rclone fails, its output is saved with credentials and URLs redacted as
`<account-id>.last-error.log` (mode 0600) in the user's private state directory.
The UI reports history recovery only when rclone actually requires it; network and
authorization errors keep their own messages.

## How it works

The runtime is Go, with SQLite and an external rclone process. One executable
serves the module API and starts a worker per Linux user with that user's UID,
GID and supplementary groups. Filesystem work never runs with the parent's root
privileges. New files and directories use the system `UMASK` from
`/etc/login.defs` (`022` if unset), ownership stays with the user, and parent
setgid bits and default ACLs still apply.

Core access goes through an inherited private socket pair. The parent fixes the
owner and rechecks access, and workers cannot open the core's protected socket.
OAuth consent uses the shared UI and the
[external-grant contract](https://github.com/PaNasMs/panasms/blob/main/documentation/external-grants.md).
Temporary provider configurations live in `/run/panasms-cloud-sync/<uid>`.
SQLite databases and bisync state live in `/var/lib/panasms-cloud-sync/<uid>`.
Upgrades migrate per-user databases, task history and cursors in place. Stop the
module and back up that state before installing an older version.

## Development

The UI is React and TypeScript built with Vite against the host-provided UI
contracts. The server is Go, built on the pinned
[module SDK](https://github.com/PaNasMs/module-sdk).

| Path | Contents |
| --- | --- |
| `frontend/` | Cloud Sync UI and `locales/` (`en`, `ru`, `uk`) |
| `cmd/server/` | Module service and core grant relay |
| `internal/syncer/` | Tasks, workers, rclone transfers, recovery and SQLite store |
| `tools/authorize.go` | Command-line helper that authorizes a Dropbox account and writes the result to a JSON file |
| `scripts/` | `build.sh`, translation check and payload packaging |

You need Linux on the target architecture (ARM64 or AMD64), Node.js 24, Go 1.26
or newer, a C compiler, `libpam0g-dev`, Python 3 for the build scripts and
`rclone` for the transfer tests. CI uses Go 1.27.1 and installs `rclone`. To build:

```sh
sh scripts/build.sh
```

The script runs `npm ci`, builds the UI, runs `go test -tags pam ./...`, builds
`dist/bin/server`, checks that `ru` and `uk` have the same translation keys as
`en` (also available as `npm test`) and writes
`dist/cloud-sync-<version>-<arch>.unsigned.zip`. The architecture comes from
`go env GOARCH`, and packaging fails if the server binary does not match it, so
build on the architecture you are packaging for. The unsigned payload cannot be
installed directly.

The Go tests cover SQLite migration, refused consent, cursor failures, process
cleanup, volume changes, interrupted bisync, filesystem notifications, worker RPC
and setup while a transfer runs. When `rclone` is installed, they also run real
one-way and two-way transfers between isolated local directories. The live
Dropbox test runs only when `PANASMS_TEST_DROPBOX_CONFIG` is set. On x86
development machines you can also run `go test -race ./...`; ThreadSanitizer may
not support the address layout of the NAS kernel. Real provider consent and
transfers are separate acceptance checks.

## Release

Update the version in `manifest.json`, `package.json` and `package-lock.json`
together, commit, then push a matching `vX.Y.Z` tag for a tested release. The
[build workflow](.github/workflows/build.yml) builds both architectures on every
push to `main` and on pull requests. Only a version tag publishes a GitHub release
with the two unsigned payloads. The module registry signs and publishes them.
Never replace an existing signed version.

## Not supported yet

OneDrive, Synology Drive and QuickConnect.

## License

Original code is licensed under
[PolyForm Noncommercial 1.0.0](LICENSE). See [NOTICE](NOTICE) for third-party
components. The Go SDK and SQLite dependencies keep their own licenses, and rclone
is a separately installed dependency.
