# PaNasMs Cloud Sync

Synchronize selected NAS and **Google Drive** folders with multiple accounts.
Version **0.1.6**, module API 1, ARM64 Linux. Google connections require a core
build exposing the external-grant API and `PaNasMsSDK.external` (September 22,
2026 or later); the prototype core version alone does not identify this feature.

## Connect and synchronize

For Google Drive, link your Google identity in **My profile → Connections**, then
select it in **Cloud Sync → Add connection** and authorize Drive access. Linking
for panel sign-in does not grant access to files. NAS-wide Google credentials
are configured once in Settings. Enable Google Drive API for that Google project.

The setup wizard has three steps: select or connect an account; choose a Drive
folder, a NAS folder and a direction on one screen; review and start. Adding a
task from an existing account skips the first step. Connecting an account alone
never starts a transfer. Each account can have several independent folder pairs.

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

Dropbox is hidden from connection options pending implementation and acceptance
testing. Existing legacy backend support and its developer helper are retained.
Google refresh tokens and client credentials stay in core; the module only
receives temporary access tokens.

Choose a direction:

- **Upload / download:** copy changes without propagating deletions. Replaced
  destination files are preserved under `.panasms-cloud-versions`.
- **Two-way:** propagate changes in both directions with rclone's deletion limit.
  One side must be empty for initial setup. After an interrupted initialization,
  automatic `--resync` is forbidden; preserve both sides and recover deliberately.
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
Temporary Google configs live under `/run/panasms-cloud-sync/<uid>`. Persistent
SQLite, Dropbox configs and bisync state live under `/var/lib/panasms-cloud-sync/<uid>`.

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
