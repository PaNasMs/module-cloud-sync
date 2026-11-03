export type SyncProgress = {
  phase: string;
  observedAt: number;
  ageSeconds: number;
  measured: boolean;
  bytes: number;
  totalBytes: number;
  checks: number;
  totalChecks: number;
  transfers: number;
  totalTransfers: number;
  speed: number;
  eta: number | null;
};

export function progressView(p: SyncProgress, disconnected: boolean, now: number, fetchedAt: number) {
  const stale = disconnected || (p.measured && p.ageSeconds + Math.max(0, now - fetchedAt) / 1000 > 10);
  const checking = p.phase === "checking" || p.phase === "comparing";
  const done = checking ? p.checks : p.bytes;
  const total = checking ? p.totalChecks : p.totalBytes;
  const percent = (checking || p.phase === "transferring") && !stale && p.measured && total > 0
    ? Math.min(99, Math.max(0, Math.floor(done / total * 100)))
    : undefined;
  const eta = !stale && p.measured && p.phase === "transferring" && p.speed > 0 &&
    p.totalBytes > p.bytes && p.eta !== null && p.eta > 0
    ? p.eta : undefined;
  return { stale, percent, eta };
}

export function formatBytes(bytes: number) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const unit = Math.min(units.length - 1, Math.floor(Math.log(Math.max(1, bytes)) / Math.log(1024)));
  return `${(bytes / 1024 ** unit).toLocaleString(undefined, { maximumFractionDigits: 1 })} ${units[unit]}`;
}
