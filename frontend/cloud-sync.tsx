import { DialogContent, WaitingSurface } from "@panasms/ui";
import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import * as Dialog from "@radix-ui/react-dialog";
import {
  mdiCloudSyncOutline,
  mdiGoogleDrive,
  mdiDropbox,
  mdiPlus,
  mdiPause,
  mdiPlay,
  mdiSync,
  mdiDeleteOutline,
  mdiClose,
  mdiLinkVariant,
  mdiArrowRight,
  mdiArrowLeft,
  mdiSwapHorizontal,
  mdiFolderOutline,
  mdiDownload,
  mdiAlertCircleOutline,
} from "@mdi/js";
import { Button, Icon, Notice } from "@panasms/ui";
import { registerModule } from "@panasms/runtime";
import { request } from "@panasms/client";
import { useQueryValue } from "@panasms/navigation";
import {
  registerTranslations,
  registerServerMessages,
  translator,
} from "@panasms/i18n";
import messages from "./server-messages.json";
import en from "./locales/en.json";
import ru from "./locales/ru.json";
import uk from "./locales/uk.json";
import "./cloud-sync.css";
registerTranslations("cloud-sync", { en, ru, uk });
registerServerMessages("cloud-sync", messages);
const tr = translator("cloud-sync");
type Account = {
  id: string;
  provider: string;
  label: string;
  identity: string;
  error: string;
};
type Task = {
  id: string;
  account: string;
  name: string;
  local: string;
  remote: string;
  direction: string;
  paused: number;
  status: string;
  error: string;
  last_sync: number;
};
type State = {
  accounts: Account[];
  tasks: Task[];
  history: {
    id: number;
    task: string;
    at: number;
    kind: string;
    message: string;
  }[];
};
const empty: State = { accounts: [], tasks: [], history: [] };
const providerIcon = (id: string) =>
  id === "drive" ? mdiGoogleDrive : mdiDropbox;
async function api<T>(body?: unknown): Promise<T> {
  const result = await request<T & { error?: string }>(
    "module-api/cloud-sync/" + (body ? "action" : "state"),
    body ? "POST" : "GET",
    body,
  );
  if (result.error) throw Error(result.error);
  return result;
}
export function CloudSyncPage() {
  const query = useQuery({
    queryKey: ["cloud-sync"],
    queryFn: () => api<State>(),
    refetchInterval: 3000,
  });
  const state = query.data ?? empty;
  const [accountID, setAccountID] = useQueryValue("account");
  const [tab, setTab] = useQueryValue("tab", "tasks", ["tasks", "history"]);
  const account =
    state.accounts.find((a) => a.id === accountID) ?? state.accounts[0];
  const [dialog, setDialog] = useState<"account" | "task" | null>(null);
  const [reconnect, setReconnect] = useState<Account | null>(null);
  const [provider, setProvider] = useState("drive");
  const [label, setLabel] = useState("");
  const [authorization, setAuthorization] = useState<unknown>(null);
  const [authFilename, setAuthFilename] = useState("");
  const [name, setName] = useState("");
  const [local, setLocal] = useState("");
  const [remote, setRemote] = useState("");
  const [direction, setDirection] = useState("download");
  const [folders, setFolders] = useState<{ name: string; path: string }[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState<{ action: string; id: string } | null>(
    null,
  );
  const file = useRef<HTMLInputElement>(null);
  async function perform(body: Record<string, unknown>) {
    setBusy(true);
    setError("");
    try {
      const result = await api<{ id?: string }>(body);
      await query.refetch();
      setDialog(null);
      setConfirm(null);
      setAuthorization(null);
      setAuthFilename("");
      if (body.action === "account.save" && result.id) setAccountID(result.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function openAccount(current: Account | null) {
    setReconnect(current);
    setProvider(current?.provider ?? "drive");
    setLabel(current?.label ?? "");
    setAuthorization(null);
    setAuthFilename("");
    setError("");
    setDialog("account");
  }
  async function browse(path: string) {
    if (!account) return;
    setBusy(true);
    setError("");
    try {
      const data = await api<{ folders: { name: string; path: string }[] }>({
        action: "folders",
        account: account.id,
        path,
      });
      setRemote(path);
      setFolders(data.folders);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const tasks = state.tasks.filter((t) => t.account === account?.id);
  const taskName = (id: string) =>
    state.tasks.find((t) => t.id === id)?.name ?? tr("removedTask");
  return (
    <WaitingSurface busy={busy && !dialog && !confirm}>
      <div className="page-heading">
        <div>
          <h1>{tr("title")}</h1>
          <p className="muted">{tr("subtitle")}</p>
        </div>
        <Button
          title={tr("addAccount")}
          aria-label={tr("addAccount")}
          onClick={() => openAccount(null)}
        >
          <Icon path={mdiPlus} />
        </Button>
      </div>
      {(error || query.error) && (
        <Notice error>{error || query.error?.message}</Notice>
      )}
      <div className="cloud-sync">
        <aside className="surface">
          {state.accounts.map((a) => (
            <button
              key={a.id}
              className="cloud-account"
              aria-current={a.id === account?.id}
              onClick={() => setAccountID(a.id)}
            >
              <Icon path={providerIcon(a.provider)} />
              <span>
                {a.label}
                <small>
                  {a.provider === "drive" ? "Google Drive" : "Dropbox"}
                </small>
              </span>
              {a.error && <Icon path={mdiAlertCircleOutline} />}
            </button>
          ))}
          {!state.accounts.length && (
            <p className="muted">{tr("noAccounts")}</p>
          )}
        </aside>
        <div className="cloud-content">
          {account ? (
            <>
              <div className="page-heading">
                <div>
                  <h2>{account.label}</h2>
                  <p className="muted">
                    {account.provider === "drive" ? "Google Drive" : "Dropbox"}
                  </p>
                </div>
                <div className="actions">
                  <Button
                    title={tr("reconnect")}
                    aria-label={tr("reconnect")}
                    onClick={() => openAccount(account)}
                  >
                    <Icon path={mdiLinkVariant} />
                  </Button>
                  <Button
                    title={tr("disconnect")}
                    aria-label={tr("disconnect")}
                    disabled={busy || tasks.length > 0}
                    onClick={() =>
                      setConfirm({ action: "account.remove", id: account.id })
                    }
                  >
                    <Icon path={mdiDeleteOutline} />
                  </Button>
                </div>
              </div>
              {account.error && <Notice error>{account.error}</Notice>}
              <div className="cloud-tabs" role="tablist">
                <Button
                  role="tab"
                  aria-selected={tab === "tasks"}
                  onClick={() => setTab("tasks")}
                >
                  {tr("tasks")}
                </Button>
                <Button
                  role="tab"
                  aria-selected={tab === "history"}
                  onClick={() => setTab("history")}
                >
                  {tr("history")}
                </Button>
                {tab === "tasks" && (
                  <Button
                    title={tr("addTask")}
                    aria-label={tr("addTask")}
                    onClick={() => {
                      setName("");
                      setLocal("");
                      setRemote("");
                      setDirection("download");
                      setFolders([]);
                      setError("");
                      setDialog("task");
                    }}
                  >
                    <Icon path={mdiPlus} />
                  </Button>
                )}
              </div>
              {tab === "tasks" ? (
                <>
                  {!tasks.length && <p className="muted">{tr("noTasks")}</p>}
                  {tasks.map((t) => (
                    <article className="surface cloud-task" key={t.id}>
                      <div className="page-heading">
                        <strong>{t.name}</strong>
                        <div className="actions">
                          <Button
                            disabled={busy}
                            title={tr(t.paused ? "resume" : "pause")}
                            aria-label={tr(t.paused ? "resume" : "pause")}
                            onClick={() =>
                              void perform({
                                action: t.paused ? "task.resume" : "task.pause",
                                id: t.id,
                              })
                            }
                          >
                            <Icon path={t.paused ? mdiPlay : mdiPause} />
                          </Button>
                          <Button
                            disabled={busy || t.status === "running"}
                            title={tr("run")}
                            aria-label={tr("run")}
                            onClick={() =>
                              void perform({ action: "task.run", id: t.id })
                            }
                          >
                            <Icon path={mdiSync} />
                          </Button>
                          <Button
                            disabled={busy || t.status === "running"}
                            title={tr("removeTask")}
                            aria-label={tr("removeTask")}
                            onClick={() =>
                              setConfirm({ action: "task.remove", id: t.id })
                            }
                          >
                            <Icon path={mdiDeleteOutline} />
                          </Button>
                        </div>
                      </div>
                      <div className="cloud-paths">
                        <span>{t.local}</span>
                        <Icon
                          path={
                            t.direction === "both"
                              ? mdiSwapHorizontal
                              : t.direction === "upload"
                                ? mdiArrowRight
                                : mdiArrowLeft
                          }
                        />
                        <span>/{t.remote}</span>
                      </div>
                      <div className="cloud-status">
                        {tr(t.paused ? "paused" : t.status)}
                        {t.last_sync > 0 && (
                          <small>
                            {new Date(t.last_sync * 1000).toLocaleString()}
                          </small>
                        )}
                      </div>
                      {t.error && <Notice error>{t.error}</Notice>}
                    </article>
                  ))}
                </>
              ) : (
                <section className="surface cloud-task">
                  {state.history
                    .filter((h) => tasks.some((t) => t.id === h.task))
                    .map((h) => (
                      <p key={h.id}>
                        <small>
                          {new Date(h.at * 1000).toLocaleString()} ·{" "}
                          {taskName(h.task)}
                        </small>
                        {h.message}
                      </p>
                    ))}
                </section>
              )}
            </>
          ) : (
            <section className="surface cloud-task">
              <h2>{tr("welcome")}</h2>
              <p>{tr("welcomeBody")}</p>
              <p className="muted">{tr("sleep")}</p>
            </section>
          )}
        </div>
      </div>
      <Dialog.Root
        open={dialog !== null}
        onOpenChange={(open) => {
          if (!open && !busy) {
            setDialog(null);
            setAuthorization(null);
            setAuthFilename("");
          }
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <DialogContent busy={busy} className="settings-dialog cloud-sync-dialog">
            <div className="page-heading">
              <Dialog.Title>
                {tr(dialog === "account" ? "addAccount" : "addTask")}
              </Dialog.Title>
              <Button
                disabled={busy}
                title={tr("cancel")}
                aria-label={tr("cancel")}
                onClick={() => {
                  setDialog(null);
                  setAuthorization(null);
                }}
              >
                <Icon path={mdiClose} />
              </Button>
            </div>
            <Dialog.Description>
              {tr(dialog === "account" ? "accountHelp" : "taskHelp")}
            </Dialog.Description>
            {error && <Notice error>{error}</Notice>}
            {dialog === "account" ? (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void perform({
                    action: "account.save",
                    id: reconnect?.id,
                    provider,
                    label,
                    authorization,
                  });
                }}
              >
                <label>
                  {tr("provider")}
                  <select
                    value={provider}
                    disabled={!!reconnect}
                    onChange={(e) => {
                      setProvider(e.target.value);
                      setAuthorization(null);
                      setAuthFilename("");
                    }}
                  >
                    <option value="drive">Google Drive</option>
                    <option value="dropbox">Dropbox</option>
                  </select>
                </label>
                <label>
                  {tr("connectionName")}
                  <input
                    value={label}
                    onChange={(e) => setLabel(e.target.value)}
                    required
                    maxLength={100}
                  />
                </label>
                <a
                  className="button"
                  href="/api/v1/module-api/cloud-sync/authorize-helper"
                  download
                >
                  <Icon path={mdiDownload} />
                  {tr("helper")}
                </a>
                <code className="cloud-helper">
                  python3 panasms-cloud-authorize.py {provider}
                </code>
                <p className="muted small">{tr("helperHint")}</p>
                <input
                  ref={file}
                  type="file"
                  accept=".json"
                  hidden
                  onChange={async (e) => {
                    const f = e.target.files?.[0];
                    e.target.value = "";
                    if (f) {
                      try {
                        if (f.size > 64000) throw Error(tr("invalidFile"));
                        setAuthorization(JSON.parse(await f.text()));
                        setAuthFilename(f.name);
                      } catch {
                        setError(tr("invalidFile"));
                      }
                    }
                  }}
                />
                <Button type="button" onClick={() => file.current?.click()}>
                  {authFilename || tr("import")}
                </Button>
                <div className="actions">
                  <Button disabled={busy || !authorization}>
                    {tr(busy ? "working" : "connect")}
                  </Button>
                  <Button
                    type="button"
                    disabled={busy}
                    onClick={() => setDialog(null)}
                  >
                    {tr("cancel")}
                  </Button>
                </div>
              </form>
            ) : (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void perform({
                    action: "task.create",
                    account: account?.id,
                    name,
                    local,
                    remote,
                    direction,
                  });
                }}
              >
                <label>
                  {tr("taskName")}
                  <input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    required
                    maxLength={100}
                  />
                </label>
                <label>
                  {tr("local")}
                  <input
                    value={local}
                    onChange={(e) => setLocal(e.target.value)}
                    placeholder="/srv/disk/cloud"
                    required
                  />
                </label>
                <label>
                  {tr("remote")}
                  <div className="actions">
                    <input
                      value={remote}
                      onChange={(e) => setRemote(e.target.value)}
                    />
                    <Button
                      type="button"
                      disabled={busy}
                      title={tr("browse")}
                      aria-label={tr("browse")}
                      onClick={() => void browse(remote)}
                    >
                      <Icon path={mdiFolderOutline} />
                    </Button>
                  </div>
                </label>
                <div className="cloud-folders">
                  {remote && (
                    <Button
                      type="button"
                      disabled={busy}
                      onClick={() =>
                        void browse(remote.split("/").slice(0, -1).join("/"))
                      }
                    >
                      ../
                    </Button>
                  )}
                  {folders.map((f) => (
                    <Button
                      type="button"
                      disabled={busy}
                      key={f.path}
                      onClick={() => void browse(f.path)}
                    >
                      {f.name}
                    </Button>
                  ))}
                </div>
                <label>
                  {tr("direction")}
                  <select
                    value={direction}
                    onChange={(e) => setDirection(e.target.value)}
                  >
                    <option value="download">{tr("download")}</option>
                    <option value="upload">{tr("upload")}</option>
                    <option value="both">{tr("both")}</option>
                  </select>
                </label>
                <Notice>
                  {tr(direction === "both" ? "bothHelp" : "copyHelp")}
                </Notice>
                <div className="actions">
                  <Button disabled={busy}>
                    {tr(busy ? "working" : "create")}
                  </Button>
                  <Button
                    type="button"
                    disabled={busy}
                    onClick={() => setDialog(null)}
                  >
                    {tr("cancel")}
                  </Button>
                </div>
              </form>
            )}
          </DialogContent>
        </Dialog.Portal>
      </Dialog.Root>
      <Dialog.Root
        open={!!confirm}
        onOpenChange={(open) => {
          if (!open && !busy) setConfirm(null);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <DialogContent busy={busy} className="settings-dialog cloud-sync-dialog">
            <Dialog.Title>{tr("removeConfirm")}</Dialog.Title>
            <Dialog.Description>{tr("preserveFiles")}</Dialog.Description>
            <div className="actions">
              <Button
                disabled={busy}
                onClick={() => confirm && void perform(confirm)}
              >
                {tr("confirm")}
              </Button>
              <Button disabled={busy} onClick={() => setConfirm(null)}>
                {tr("cancel")}
              </Button>
            </div>
          </DialogContent>
        </Dialog.Portal>
      </Dialog.Root>
    </WaitingSurface>
  );
}
registerModule({
  id: "cloud-sync",
  title: tr("title"),
  path: "/cloud-sync",
  icon: mdiCloudSyncOutline,
  component: CloudSyncPage,
});
