# PaNasMs Cloud Sync module

Synchronize local folders with multiple Google Drive and Dropbox accounts.

Standalone source repository. Requires PaNasMs core `>=0.2.1,<0.3.0`, module API 1.
Frontend: React / TypeScript. Server: Go with the versioned module SDK.
Python helpers run through the host management interface where applicable.

## Build and release

On ARM64 Linux install Node.js 24, Go 1.26+, Python 3, gcc and libpam0g-dev.
Run `sh scripts/build.sh`. The result is an **unsigned build payload** in dist.
The registry signs it separately; unsigned payloads cannot be installed directly.

Update manifest.json and package.json versions, commit and push a matching `vX.Y.Z`
tag. CI tests and builds the source and publishes an immutable GitHub Release.
PaNasMs/module-registry periodically imports official releases, signs installable
archives and publishes them in the catalog. No signing key is available here.
Never replace a published version; issue a new patch version instead.

Original code: PolyForm Noncommercial 1.0.0. See LICENSE and NOTICE.
