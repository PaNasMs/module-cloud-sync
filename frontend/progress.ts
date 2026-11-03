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
  // rclone totals grow during discovery; they do not establish a final denominator.
  return { stale };
}

export function formatBytes(bytes: number) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const unit = Math.min(units.length - 1, Math.floor(Math.log(Math.max(1, bytes)) / Math.log(1024)));
  return `${(bytes / 1024 ** unit).toLocaleString(undefined, { maximumFractionDigits: 1 })} ${units[unit]}`;
}
