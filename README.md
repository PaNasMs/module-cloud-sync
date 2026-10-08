# PaNasMs Cloud Sync

Synchronize selected NAS, **Google Drive** and **Dropbox** folders with multiple
accounts. Version **0.1.23**, module API 1, ARM64 and AMD64 Linux, core **0.2.15+**.

## Connect and synchronize

For Google Drive, link your Google identity in **My profile → Connections**, then
select it in **Cloud Sync → Add connection** and authorize Drive access. Linking
for panel sign-in does not grant access to files. NAS-wide Google credentials
are configured once in Settings. Enable Google Drive API for that Google project.

The setup wizard has three steps: select or connect an account; choose a Drive
folder, a NAS folder and a direction on one screen; review and start. Adding a
task from an existing account skips the first step. Connecting an account alone
never starts a transfer. Each account can have several independent folder pairs.
You can add accounts, browse cloud folders and create tasks while another task
is synchronizing. New tasks wait for the current transfer; each user worker runs
one synchronization at a time. Reconnecting or removing an existing account
requires transfers to be paused first.

The main page uses the NAS settings layout. Its sidebar lists sync tasks; the
selected task shows both folders, direction, state, account actions and history
on one page without tabs. Selection is retained in the `sync` URL parameter.
Connections with no tasks remain available separately for setup or removal.

Both folder fields use visual navigation. The NAS picker has lazy expandable
branches, a Home shortcut resolved from the Linux account, and mounted data
volumes as entry points (not /home, /srv, /mnt and /media as a directory list).
Duplicate mount aliases are collapsed; unsupported destinations are disabled.
The NAS picker can create a subfolder
under the selected writable folder, using the Linux user's permissions and umask.
Select My Drive explicitly to synchronize its root. The Google permission is not
restricted to that folder; the task configuration limits the sync operation.

For Dropbox, configure the NAS client following the
[Dropbox setup guide](https://panasms.github.io/docs/setup/dropbox/). Enable
`account_info.read`, `files.metadata.read`, `files.content.read` and
`files.content.write` in the Dropbox App Console. Link your Dropbox account in
My profile, choose Dropbox in the connection wizard, then approve offline file
access. Linking identity alone does not authorize file access. Multiple linked
accounts are supported; Development apps may need additional users enabled.
Choose the existing remote folder and a local folder before starting. File
permissions apply to the account; the task limits which folder is synchronized.

Both providers keep refresh tokens and client credentials encrypted in core;
the module receives only temporary access tokens. Old imported Dropbox credentials
require reconnection and are removed only after the replacement grant succeeds.

Choose a direction:

- **Upload / download:** copy changes without propagating deletions. Replaced
  destination files are preserved under `.panasms-cloud-versions`.
- **Two-way:** propagate changes in both directions with rclone's deletion limit.
  One side must be empty for initial setup. When history is damaged, recovery
  treats the cloud as authoritative and backs up displaced local files first.
- Pause/resume, run now, remove tasks and inspect history. Removing a task or
  connection preserves synchronized files.

Folders must not overlap. Local folders must reside on a supported Linux
filesystem below `/home`, `/srv`, `/mnt` or `/media`, be writable by the owner,
and use no symlink path components. Network and FAT/NTFS mounts are unsupported.
OneDrive, Synology Drive and QuickConnect are future work.

## Implementation

The runtime backend is entirely Go, with SQLite and an external rclone process.
The same executable hosts the module API and launches workers with each Linux
user's UID, GID and supplementary groups. Filesystem work never inherits the
parent's root privileges. Existing per-user databases, task history and cursors
are migrated in place; stop the module and back up state before downgrading.

Core access is relayed through an inherited private socket pair. The parent fixes
the owner and rechecks access; unprivileged workers cannot open core's protected
socket. OAuth consent uses the shared UI and
[external-grant contract](https://github.com/PaNasMs/panasms/blob/main/documentation/external-grants.md).
Temporary provider configs live under `/run/panasms-cloud-sync/<uid>`. Persistent
SQLite and bisync state live under `/var/lib/panasms-cloud-sync/<uid>`.

Permissions are rechecked during transfers, normally every 30 seconds. Revocation,
permission-service errors, cancellation and volume loss stop and reap rclone.
One-way copies may restart after token rotation. Interrupted two-way operations
require review; they are never silently reset to initial synchronization.
Already-issued tokens can remain valid at the provider until their expiry.

Inotify observes local changes, including new subdirectories. Cloud cursors are
polled with backoff; unchanged accounts do not trigger repeated local tree scans.
Initial watch registration and actual changes still access local storage. Other
NAS services and active transfers can prevent HDD sleep.

Google External Testing applications have limited-lived Drive grants (typically
seven days); production consent/verification needs separate configuration.
Current limits are 32 tasks per user, 100,000 listing entries/directory watches,
and bounded cloud responses. Module access currently requires a NAS administrator.

## Build and validate

Use ARM64 Linux, Node.js 24, Go 1.26+, a C compiler, `libpam0g-dev`, Python 3
(for build/packaging scripts only), and rclone for transfer tests:

```sh
sh scripts/build.sh
```

Tests cover SQLite migration, consent refusal, cursor failures, process cleanup,
volume changes, interrupted bisync, filesystem notifications and worker RPC.
When rclone is available they also run real upload/download/two-way transfers
between isolated local test directories. Local x86 development can run
`go test -race ./...`; ThreadSanitizer may not support the NAS kernel's address
layout. Actual provider consent and transfers are separate acceptance checks.

The build writes an unsigned ARM64 ZIP. Install a signed `.panasms` bundle from
Modules or the registry. Set manifest/package/lockfile versions consistently and
push `vX.Y.Z` only for a tested release. GitHub builds the module, then the registry
signs and publishes it. Never overwrite an existing signed version.

## License

Original code: [PolyForm Noncommercial 1.0.0](LICENSE).
See [NOTICE](NOTICE) for third-party components. Go SDK and SQLite dependencies
retain their own licenses; rclone remains an independently installed dependency.

### Automatic recovery

Cloud access is rechecked even when all synchronization tasks are paused. Successful
checks clear obsolete account errors without advancing the cloud change cursor
for paused tasks. Failed checks use exponential backoff (up to 30 minutes).

Tasks stopped because their local volume disappeared are checked every 30 seconds.
They resume only when the saved mount identity matches and the original directory
is accessible. The identity is the filesystem UUID with its mount point, so a
disk renamed by the kernel between boots (for example `/dev/sda1` to `/dev/sdb1`)
remains the same volume. Identities recorded by device before 0.1.18 are upgraded
automatically; when the device was already renamed, the task is accepted only if
its folder still holds the task's own access marker. Filesystems for which udev
publishes no UUID keep the device-based identity.
A different volume never becomes an automatic replacement. Manual
pause cancels automatic recovery. Temporary connectivity failures retry with
persisted exponential backoff (1–30 minutes). Authorization retries never bypass
revoked permissions; the owner must grant access again when required.
Existing volume-unavailable errors are adopted by this recovery mechanism on the
first upgrade. No folders are created to substitute for a missing volume.

### Permissions of user files

New user files and directories use the system `UMASK` from `/etc/login.defs`
(`022` if unset), rather than the private service mask. Ownership remains with
the Linux user running the operation. Parent-directory setgid and default ACLs
still apply; existing files are not changed. Private module state and credentials
retain restrictive permissions. Terminal startup scripts can override the initial
shell mask.

## Long-running authorization

A transfer uses a private per-run rclone configuration and a loopback refresh
relay. rclone's [token URL override](https://rclone.org/drive/#drive-token-url)
requests fresh access tokens from the core broker using a random per-run capability.
Provider refresh tokens and OAuth client secrets remain in core. Google Drive and
Dropbox transfers no longer restart merely because an access token rotates.
Revoked grants still stop work. History recovery follows the cloud-authoritative
procedure described below, rather than an unrestricted resync.

## Supported architectures

Version 0.1.14 and newer publish separate native `arm64` and `amd64` packages. The module manager selects the compatible package automatically. CI tests both architectures on Ubuntu 24.04 runners before publishing a release. Package creation verifies the server ELF architecture against the manifest. Older ARM64-only releases remain unchanged.

## Empty folders and transfer diagnostics

From 0.1.15, new two-way tasks create a small hidden
`.panasms-cloud-access-<task-id>` file in the selected folder pair. This stable
marker supports empty-folder initialization and single-file changes with distro
rclone 1.60, and enables `--check-access` on subsequent runs. Do not delete the
marker: loss stops synchronization instead of recreating it or propagating
deletions. Existing tasks are not silently resynchronized or migrated.

Failed subprocess output is retained with credentials and URLs redacted in the
private per-user state directory as `<account-id>.last-error.log` (0600). The UI
only reports history recovery when rclone actually requires it; ordinary network
and authorization errors retain their specific messages. Original failed test
tasks can be replaced with a new task after preserving the folders, subject to
the same initial requirement that one side be empty.

## Cloud-authoritative history recovery

When a two-way task loses its history, the next scheduled recovery copies the
cloud state to the local folder. Replaced files and local-only files are moved
into `.panasms-cloud-versions/recovery-<timestamp>-<id>`; they are never uploaded
as part of recovery. Backups are excluded from synchronization and retained for
manual review. Sufficient local free space and write permissions are required.

The module verifies the cloud access marker and original local mount before
reconciliation. Legacy initialized tasks without a marker receive one first.
A missing or invalid marker on a task which already had one stops recovery for
review. Network and authorization failures cannot be treated as an empty cloud.

A task whose first synchronization was interrupted has no marker in the cloud yet.
It first uploads local files that the cloud lacks, without replacing any cloud
file, and then continues with the same cloud-authoritative reconciliation, which
replaces partially downloaded files and keeps the replaced copies in the backup.

Cloud snapshots compare file paths, sizes, times and hashes, and folder paths
without folder times: Dropbox keeps none and reports the time of the listing.

After reconciliation, content checks and a dry-run resync build a fresh history
in a separate directory. The cloud listing must remain unchanged during this
step. Previous history is retained outside the active work directory. Normal
two-way synchronization then resumes; cloud priority applies only to recovery.
Do not edit either folder during recovery. Manual pause cancels scheduled retries;
pre-existing manually paused tasks remain paused after upgrade.

The task details show recovery state and the next retry time. Quota, filesystem
permissions, missing access markers and safety-limit failures still need user
action; automatic recovery cannot repair these conditions.
