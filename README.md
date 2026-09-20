# PaNasMs Cloud Sync module

Prototype cloud synchronization for PaNasMs. Current manifest version: **0.1.5**.
Requires core `>=0.2.1,<0.3.0`, module API 1 and ARM64 Linux.

## Supported workflows

- Multiple connections for each supported provider: **Google Drive** and **Dropbox**.
- Upload, download and two-way synchronization tasks, pause/retry and task history.
- Per-user SQLite state and rclone workers; provider authorization is required.
- Local filesystem notifications and cloud change cursors reduce repeated local
  scans while idle. This avoids unnecessary disk activity but does not guarantee
  that other services or an active transfer will let a disk sleep.

Upload/download modes copy changes without propagating deletions and retain
replaced destination versions. Initial two-way synchronization requires one
folder to be empty; rclone deletion limits remain enabled. Interrupted two-way
history is not silently reset. OneDrive, Synology Drive and QuickConnect support
are not implemented.

## Connect an account

Install rclone on a computer with a browser. Run the local helper for the desired
provider, authorize the account, then import its output through **Cloud Sync →
Add connection**:

```sh
python3 tools/authorize.py drive --output drive-account.json
python3 tools/authorize.py dropbox --output dropbox-account.json
```

Run one command per connection; use distinct output files for additional accounts.
These files contain OAuth tokens. Keep them private and do not commit them.
The helper uses rclone's authorization flow, so a custom OAuth application is not
required for this prototype. Runtime OS dependencies, including rclone, are
specified in the signed manifest and resolved during module installation.

## Development

The frontend uses React/TypeScript and host-provided UI contracts. The server uses
Go and the pinned [module SDK](https://github.com/PaNasMs/module-sdk). Python helpers
are included where the module requires them. Do not bundle another copy of the
host React/router/query runtime.

Use ARM64 Linux, Node.js 24, Go 1.26 or newer, Python 3, a C compiler and
`libpam0g-dev`. The release workflow pins Go 1.27.1. From this repository:

```sh
sh scripts/build.sh
```

This installs locked npm dependencies, builds the UI, runs PAM-enabled Go tests,
builds the server, checks Python syntax/translation keys and available Python
tests, then writes `dist/<id>-<version>-arm64.unsigned.zip`. This is an unsigned
build payload and cannot be installed directly. The script labels output ARM64;
build on ARM64 rather than treating it as a cross-compilation command.

## Install and release

Install the signed version from the PaNasMs **Modules** catalog, or upload a signed
`.panasms` archive from the [registry](https://github.com/PaNasMs/module-registry).

For a new release, update `manifest.json`, `package.json` and the npm lockfile
consistently, commit, then push the matching `vX.Y.Z` tag. The workflow also builds
branches/PRs, but only a version tag publishes a source release. Its unsigned
payload is imported and signed by the registry, which publishes the installable
archive and updates the catalog. Signing keys are not stored in this repository.
Publish a new version instead of replacing an existing release.

## Documentation and license

Public documentation is maintained in English. Original code uses
[PolyForm Noncommercial 1.0.0](LICENSE); see [NOTICE](NOTICE) for third-party scope.
