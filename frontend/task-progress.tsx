import { useEffect, useState } from "react";
import { formatBytes, progressView } from "./progress";
import type { SyncProgress } from "./progress";

type ProgressTask = {
  name: string;
  status: string;
  paused: number;
  progress?: SyncProgress;
};

export function TaskProgress({ task, disconnected, fetchedAt, translate: tr }: {
  task: ProgressTask;
  disconnected: boolean;
  fetchedAt: number;
  translate: (key: string) => string;
}) {
  const p = task.progress;
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 2000);
    return () => clearInterval(timer);
  }, []);
  if (task.status !== "running") return null;
  const stale = disconnected || (p && progressView(p, false, now, fetchedAt).stale);
  const phase = task.paused ? "pausing" : p ? `progress_${p.phase}` : "running";
  const transferring = p?.phase === "transferring";
  const checking = p?.phase === "checking";
  const showCounters = p?.measured && (transferring || checking || p.phase === "comparing");
  return <div className="cloud-progress">
    <div className="cloud-progress-heading" role="status">
      {!task.paused && !stale && <span className="waiting-spinner" aria-hidden="true" />}
      <span>{tr(phase)}</span>
    </div>
    {showCounters && <dl className="cloud-review">
      {(transferring || checking) && <>
        <dt>{tr(checking ? "progressCheckedBytes" : "progressBytes")}</dt><dd>{formatBytes(p.bytes)}</dd>
        {transferring && <><dt>{tr("progressFiles")}</dt><dd>{p.transfers.toLocaleString()}</dd></>}
        {!task.paused && !stale && <><dt>{tr("progressSpeed")}</dt><dd>{formatBytes(p.speed)}{tr("perSecond")}</dd></>}
      </>}
      <dt>{tr("progressChecks")}</dt><dd>{p.checks.toLocaleString()}</dd>
    </dl>}
    {stale
      ? <p className="muted">{tr("progressStale")}{p && <> · {new Date(p.observedAt * 1000).toLocaleTimeString()}</>}</p>
      : <p className="muted">{tr(showCounters ? "progressStageHelp" : "progressPhaseHelp")}</p>}
  </div>;
}
