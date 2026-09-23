import { DialogContent, WaitingSurface } from "@panasms/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import * as Dialog from "@radix-ui/react-dialog";
import {
  mdiCloudSyncOutline,
  mdiGoogleDrive,
  mdiArrowUp,
  mdiChevronRight,
  mdiChevronDown,
  mdiHomeOutline,
  mdiHarddisk,
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
import { GoogleConnect } from "@panasms/external";
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
type Connection = { id: string; provider: string; email: string; name: string };
const empty: State = { accounts: [], tasks: [], history: [] };
const providerIcon = (id: string) =>
  id === "drive" ? mdiGoogleDrive : mdiCloudSyncOutline;
async function api<T>(body?: unknown): Promise<T> {
  const result = await request<T & { error?: string }>(
    "module-api/cloud-sync/" + (body ? "action" : "state"),
    body ? "POST" : "GET",
    body,
  );
  if (result.error) throw Error(result.error);
  return result;
}
type FolderNode = {
  name: string;
  path: string;
  kind?: string;
  reason?: string;
};
type FolderListing = {
  folders: FolderNode[];
  roots?: FolderNode[];
  selectable: boolean;
};
function LocalPath({ path }: { path: string }) {
  const roots = useQuery({
    queryKey: ["cloud-local-tree", "roots"],
    queryFn: () => api<FolderListing>({ action: "local.folders", path: "" }),
    staleTime: 30000,
  });
  const root = roots.data?.roots
    ?.filter(
      (r) => !r.reason && (path === r.path || path.startsWith(r.path + "/")),
    )
    .sort((a, b) => b.path.length - a.path.length)[0];
  const label = root?.kind === "home" ? tr("homeFolder") : root?.name;
  const parts = root
    ? [label!, ...path.slice(root.path.length).split("/").filter(Boolean)]
    : [path];
  return (
    <p className="cloud-location-path" title={path}>
      {root && (
        <Icon path={root.kind === "home" ? mdiHomeOutline : mdiHarddisk} />
      )}
      <span>{parts.join(" / ")}</span>
    </p>
  );
}
function LocalFolderBranch({
  node,
  selected,
  onSelect,
}: {
  node: FolderNode;
  selected: string;
  onSelect: (path: string) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  useEffect(() => {
    if (selected === node.path || selected.startsWith(node.path + "/"))
      setExpanded(true);
  }, [selected, node.path]);
  const data = useQuery({
    queryKey: ["cloud-local-tree", node.path],
    queryFn: () =>
      api<FolderListing>({ action: "local.folders", path: node.path }),
    enabled: expanded && !node.reason,
  });
  const title = node.kind === "home" ? tr("homeFolder") : node.name;
  return (
    <li>
      <div className="cloud-tree-row" data-selected={selected === node.path}>
        <Button
          type="button"
          disabled={!!node.reason}
          title={
            tr(expanded ? "collapseFolder" : "expandFolder") + " · " + title
          }
          aria-label={
            tr(expanded ? "collapseFolder" : "expandFolder") + " · " + title
          }
          aria-expanded={expanded}
          onClick={() => setExpanded(!expanded)}
        >
          <Icon path={expanded ? mdiChevronDown : mdiChevronRight} />
        </Button>
        <button
          type="button"
          className="cloud-tree-name"
          disabled={!!node.reason}
          title={node.path}
          aria-current={selected === node.path ? "true" : undefined}
          onClick={() => {
            setExpanded(true);
            onSelect(node.path);
          }}
        >
          <Icon
            path={
              node.kind === "home"
                ? mdiHomeOutline
                : node.kind === "volume"
                  ? mdiHarddisk
                  : mdiFolderOutline
            }
          />
          <span>
            {title}
            {node.kind && <small>{node.path}</small>}
          </span>
        </button>
      </div>
      {node.reason && (
        <p className="cloud-tree-note">{tr("unavailableVolume")}</p>
      )}
      {expanded && !node.reason && (
        <ul>
          {data.isPending ? (
            <li className="cloud-tree-note">{tr("working")}</li>
          ) : data.error ? (
            <li>
              <Notice error>{data.error.message}</Notice>
            </li>
          ) : data.data?.folders.length ? (
            data.data.folders.map((child) => (
              <LocalFolderBranch
                key={child.path}
                node={child}
                selected={selected}
                onSelect={onSelect}
              />
            ))
          ) : (
            <li className="cloud-tree-note">{tr("noFolders")}</li>
          )}
        </ul>
      )}
    </li>
  );
}
function LocalFolderTree({
  selected,
  onSelect,
}: {
  selected: string;
  onSelect: (path: string) => void;
}) {
  const data = useQuery({
    queryKey: ["cloud-local-tree", "roots"],
    queryFn: () => api<FolderListing>({ action: "local.folders", path: "" }),
  });
  return (
    <div className="cloud-local-tree">
      {data.isPending && <p>{tr("working")}</p>}
      {data.error && <Notice error>{data.error.message}</Notice>}
      {["home", "volume"].map((kind) => (
        <section key={kind}>
          <h3>{tr(kind === "home" ? "quickPlaces" : "volumes")}</h3>
          <ul>
            {data.data?.roots
              ?.filter((root) => root.kind === kind)
              .map((root) => (
                <LocalFolderBranch
                  key={root.kind + root.path}
                  node={root}
                  selected={selected}
                  onSelect={onSelect}
                />
              ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
export function CloudSyncPage() {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["cloud-sync"],
    queryFn: () => api<State>(),
    refetchInterval: 3000,
  });
  const state = query.data ?? empty;
  const [accountID, setAccountID] = useQueryValue("account");
  const [selectedID, setSelectedID] = useQueryValue("sync");
  const selectedTask = selectedID.startsWith("account:")
    ? undefined
    : (state.tasks.find((t) => t.id === selectedID) ??
      state.tasks.find((t) => t.account === accountID) ??
      state.tasks[0]);
  const account =
    state.accounts.find(
      (a) =>
        a.id ===
        (selectedTask?.account ||
          selectedID.replace(/^account:/, "") ||
          accountID),
    ) ?? state.accounts[0];
  const unconfigured = state.accounts.filter(
    (a) => !state.tasks.some((t) => t.account === a.id),
  );
  const [dialog, setDialog] = useState<"account" | "task" | null>(null);
  const [reconnect, setReconnect] = useState<Account | null>(null);
  const [label, setLabel] = useState("");
  const [step, setStep] = useState(1);
  const [taskAccount, setTaskAccount] = useState("");
  const [remoteChosen, setRemoteChosen] = useState(false);
  const [picker, setPicker] = useState<"local" | "remote" | null>(null);
  const [browsePath, setBrowsePath] = useState("");
  const [canChoose, setCanChoose] = useState(false);
  const [folderName, setFolderName] = useState("");
  const [newAccount, setNewAccount] = useState(false);
  const [connectionId, setConnectionId] = useState("");
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

  const connectionsQuery = useQuery({
    queryKey: ["external-connections"],
    queryFn: () => request<Connection[]>("external/connections"),
    enabled: dialog !== null && step === 1,
  });
  const googleConnections =
    connectionsQuery.data?.filter((c) => c.provider === "google") ?? [];
  const selectedConnectionId = connectionId || googleConnections[0]?.id || "";
  async function perform(body: Record<string, unknown>) {
    setBusy(true);
    setError("");
    try {
      const result = await api<{ id?: string }>(body);
      await query.refetch();
      setDialog(null);
      setConfirm(null);
      setConnectionId("");
      if (body.action === "account.save" && result.id) setAccountID(result.id);
      if (body.action === "task.create" && result.id) setSelectedID(result.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const grantCompletion = useRef<(id?: string) => void>(() => {});
  useEffect(() => {
    grantCompletion.current = (grantId) => {
      if (grantId) void connectAccount(grantId);
    };
  });
  const completeGoogle = useCallback(
    (grantId?: string) => grantCompletion.current(grantId),
    [],
  );
  async function connectAccount(grantId: string) {
    setBusy(true);
    setError("");
    try {
      const result = await api<{ id: string }>({
        action: "account.save",
        id: reconnect?.id,
        provider: "drive",
        label:
          label.trim() ||
          googleConnections.find((c) => c.id === selectedConnectionId)?.email ||
          "Google Drive",
        grantId,
      });
      await query.refetch();
      setAccountID(result.id);
      setSelectedID("account:" + result.id);
      setTaskAccount(result.id);
      if (reconnect) setDialog(null);
      else {
        setStep(2);
        setNewAccount(false);
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function openAccount(current: Account | null) {
    setReconnect(current);
    setLabel(current?.label ?? "");
    setConnectionId("");
    setTaskAccount("");
    setStep(1);
    setNewAccount(!!current || !state.accounts.length);
    setName("");
    setLocal("");
    setRemote("");
    setRemoteChosen(false);
    setDirection("download");
    setError("");
    setDialog("account");
  }
  const chosenAccount = state.accounts.find((a) => a.id === taskAccount);
  async function browse(kind: "local" | "remote", path: string) {
    setBusy(true);
    setError("");
    try {
      const data = await api<{
        folders: { name: string; path: string }[];
        selectable?: boolean;
      }>({
        action: kind === "local" ? "local.folders" : "folders",
        account: taskAccount,
        path,
      });
      setBrowsePath(path);
      setFolders(data.folders);
      setCanChoose(kind === "remote" || !!data.selectable);
      setPicker(kind);
      setFolderName("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function createFolder() {
    setBusy(true);
    setError("");
    try {
      const result = await api<{ path: string }>({
        action: "local.mkdir",
        path: browsePath,
        name: folderName,
      });
      await queryClient.invalidateQueries({ queryKey: ["cloud-local-tree"] });
      await browse("local", result.path);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const tasks = state.tasks.filter((t) => t.account === account?.id);
  const history = state.history.filter((h) => h.task === selectedTask?.id);
  function addTask() {
    if (!account) return;
    setTaskAccount(account.id);
    setStep(2);
    setReconnect(null);
    setRemoteChosen(false);
    setName("");
    setLocal("");
    setRemote("");
    setDirection("download");
    setFolders([]);
    setError("");
    setDialog("task");
  }
  return (
    <WaitingSurface busy={query.isPending || (busy && !dialog && !confirm)}>
      <div className="page-heading">
        <div>
          <h1>{tr("title")}</h1>
          <p className="muted">{tr("subtitle")}</p>
        </div>
        <Button
          title={tr("addTask")}
          aria-label={tr("addTask")}
          onClick={() => openAccount(null)}
        >
          <Icon path={mdiPlus} />
        </Button>
      </div>
      {((error && !dialog && !confirm) || query.error) && (
        <Notice error>{error || query.error?.message}</Notice>
      )}
      <div className="settings-layout settings-page cloud-sync-settings">
        <nav className="settings-nav" aria-label={tr("tasks")}>
          {state.tasks.map((t) => (
            <button
              key={t.id}
              type="button"
              data-state={selectedTask?.id === t.id ? "active" : "inactive"}
              aria-current={selectedTask?.id === t.id ? "page" : undefined}
              onClick={() => {
                setSelectedID(t.id);
                setError("");
              }}
            >
              <Icon
                path={
                  t.error
                    ? mdiAlertCircleOutline
                    : t.paused
                      ? mdiPause
                      : mdiCloudSyncOutline
                }
              />
              <span className="cloud-nav-label">
                <strong>{t.name}</strong>
                <small>
                  {state.accounts.find((a) => a.id === t.account)?.label}
                </small>
              </span>
            </button>
          ))}
          {unconfigured.length > 0 && (
            <span className="cloud-nav-caption">{tr("withoutTasks")}</span>
          )}
          {unconfigured.map((a) => (
            <button
              key={a.id}
              type="button"
              data-state={
                !selectedTask && account?.id === a.id ? "active" : "inactive"
              }
              aria-current={
                !selectedTask && account?.id === a.id ? "page" : undefined
              }
              onClick={() => {
                setSelectedID("account:" + a.id);
                setError("");
              }}
            >
              <Icon path={providerIcon(a.provider)} />
              <span className="cloud-nav-label">{a.label}</span>
            </button>
          ))}
          {!state.accounts.length && <p className="muted">{tr("noTasks")}</p>}
        </nav>
        <div className="settings-content">
          <div className="general-settings">
            {account ? (
              <>
                <section className="surface">
                  <div className="page-heading">
                    <div>
                      <h2>{selectedTask?.name || account.label}</h2>
                      <p className="muted">Google Drive · {account.label}</p>
                    </div>
                    <div className="actions">
                      {selectedTask && (
                        <>
                          <Button
                            disabled={busy}
                            title={tr(selectedTask.paused ? "resume" : "pause")}
                            aria-label={tr(
                              selectedTask.paused ? "resume" : "pause",
                            )}
                            onClick={() =>
                              void perform({
                                action: selectedTask.paused
                                  ? "task.resume"
                                  : "task.pause",
                                id: selectedTask.id,
                              })
                            }
                          >
                            <Icon
                              path={selectedTask.paused ? mdiPlay : mdiPause}
                            />
                          </Button>
                          <Button
                            disabled={busy || selectedTask.status === "running"}
                            title={tr("run")}
                            aria-label={tr("run")}
                            onClick={() =>
                              void perform({
                                action: "task.run",
                                id: selectedTask.id,
                              })
                            }
                          >
                            <Icon path={mdiSync} />
                          </Button>
                          <Button
                            disabled={busy || selectedTask.status === "running"}
                            title={tr("removeTask")}
                            aria-label={tr("removeTask")}
                            onClick={() =>
                              setConfirm({
                                action: "task.remove",
                                id: selectedTask.id,
                              })
                            }
                          >
                            <Icon path={mdiDeleteOutline} />
                          </Button>
                        </>
                      )}
                      <Button
                        title={tr("addTask")}
                        aria-label={tr("addTask")}
                        onClick={addTask}
                      >
                        <Icon path={mdiPlus} />
                      </Button>
                    </div>
                  </div>
                  {selectedTask ? (
                    <>
                      <div className="cloud-paths">
                        <div>
                          <span className="field-label">{tr("local")}</span>
                          <LocalPath path={selectedTask.local} />
                        </div>
                        <Icon
                          path={
                            selectedTask.direction === "both"
                              ? mdiSwapHorizontal
                              : selectedTask.direction === "upload"
                                ? mdiArrowRight
                                : mdiArrowLeft
                          }
                        />
                        <div>
                          <span className="field-label">{tr("remote")}</span>
                          <p>
                            {selectedTask.remote
                              ? "/" + selectedTask.remote
                              : tr("driveRoot")}
                          </p>
                        </div>
                      </div>
                      <dl className="cloud-review">
                        <dt>{tr("direction")}</dt>
                        <dd>{tr(selectedTask.direction)}</dd>
                        <dt>{tr("status")}</dt>
                        <dd>
                          {tr(
                            selectedTask.paused
                              ? "paused"
                              : selectedTask.status,
                          )}
                        </dd>
                        <dt>{tr("lastSync")}</dt>
                        <dd>
                          {selectedTask.last_sync
                            ? new Date(
                                selectedTask.last_sync * 1000,
                              ).toLocaleString()
                            : tr("notYet")}
                        </dd>
                      </dl>
                      {selectedTask.error && (
                        <Notice error>{selectedTask.error}</Notice>
                      )}
                    </>
                  ) : (
                    <p className="muted">{tr("noTasks")}</p>
                  )}
                </section>
                <section className="surface cloud-connection-section">
                  <div className="page-heading">
                    <h3>{tr("accountStep")}</h3>
                    <div className="actions">
                      <Button
                        title={tr("reconnect")}
                        aria-label={tr("reconnect")}
                        onClick={() => openAccount(account)}
                      >
                        <Icon path={mdiLinkVariant} />
                      </Button>
                      <Button
                        disabled={busy || tasks.length > 0}
                        title={tr("disconnect")}
                        aria-label={tr("disconnect")}
                        onClick={() =>
                          setConfirm({
                            action: "account.remove",
                            id: account.id,
                          })
                        }
                      >
                        <Icon path={mdiDeleteOutline} />
                      </Button>
                    </div>
                  </div>
                  <p className="cloud-account-summary">
                    <Icon path={providerIcon(account.provider)} />
                    {account.label}
                  </p>
                  {account.error && <Notice error>{account.error}</Notice>}
                </section>
                {selectedTask && (
                  <section className="surface cloud-history">
                    <h3>{tr("history")}</h3>
                    {!history.length && (
                      <p className="muted">{tr("noHistory")}</p>
                    )}
                    <ol>
                      {history.map((h) => (
                        <li key={h.id}>
                          <time dateTime={new Date(h.at * 1000).toISOString()}>
                            {new Date(h.at * 1000).toLocaleString()}
                          </time>
                          <span>{h.message}</span>
                        </li>
                      ))}
                    </ol>
                  </section>
                )}
              </>
            ) : (
              <section className="surface">
                <h2>{tr("welcome")}</h2>
                <p>{tr("welcomeBody")}</p>
                <p className="muted">{tr("sleep")}</p>
              </section>
            )}
          </div>
        </div>
      </div>
      <Dialog.Root
        open={dialog !== null}
        onOpenChange={(open) => {
          if (!open && !busy) {
            setDialog(null);
            setPicker(null);
          }
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <DialogContent
            busy={busy}
            className="settings-dialog cloud-sync-dialog"
            dirty={
              !!local ||
              remoteChosen ||
              !!name ||
              label !== (reconnect?.label ?? "") ||
              direction !== "download"
            }
            header={
              <>
                {" "}
                <div className="dialog-heading">
                  <Dialog.Title>
                    {tr(reconnect ? "reconnect" : "addTask")}
                  </Dialog.Title>
                </div>
                <Dialog.Description>
                  {tr(reconnect ? "driveAccountHelp" : "wizardHelp")}
                </Dialog.Description>{" "}
              </>
            }
            footer={
              <div className="actions">
                {picker ? (
                  <>
                    {" "}
                    <Button onClick={() => setPicker(null)} disabled={busy}>
                      {tr("cancel")}
                    </Button>
                    <Button
                      className="primary"
                      disabled={busy || !canChoose}
                      onClick={() => {
                        if (picker === "local") setLocal(browsePath);
                        else {
                          setRemote(browsePath);
                          setRemoteChosen(true);
                        }
                        setPicker(null);
                      }}
                    >
                      {tr("selectFolder")}
                    </Button>{" "}
                  </>
                ) : (
                  <>
                    {" "}
                    <Button
                      type="button"
                      disabled={busy}
                      onClick={() => setDialog(null)}
                      data-dialog-cancel
                    >
                      {tr("cancel")}
                    </Button>
                    {step > (dialog === "task" ? 2 : 1) && (
                      <Button
                        type="button"
                        disabled={busy}
                        onClick={() => setStep(step - 1)}
                      >
                        {tr("back")}
                      </Button>
                    )}
                    {(step > 1 || (!newAccount && !reconnect)) && (
                      <Button
                        className="primary"
                        type="submit"
                        form="cloud-sync-wizard"
                        disabled={
                          busy ||
                          (step === 1 && !taskAccount) ||
                          (step === 2 && (!local || !remoteChosen))
                        }
                      >
                        {tr(step === 3 ? "start" : "next")}
                      </Button>
                    )}{" "}
                  </>
                )}
              </div>
            }
            variant="form"
            intent="edit"
          >
            {!reconnect && (
              <ol className="cloud-steps">
                {["accountStep", "setupStep", "reviewStep"].map((key, i) => (
                  <li
                    key={key}
                    aria-current={step === i + 1 ? "step" : undefined}
                  >
                    <span>{i + 1}</span>
                    {tr(key)}
                  </li>
                ))}
              </ol>
            )}
            {error && <Notice error>{error}</Notice>}
            {picker ? (
              <div className="folder-picker">
                <div className="folder-picker-heading">
                  {picker === "remote" && (
                    <>
                      <Button
                        title={tr("up")}
                        aria-label={tr("up")}
                        disabled={busy || !browsePath}
                        onClick={() =>
                          void browse(
                            picker,
                            browsePath.split("/").slice(0, -1).join("/"),
                          )
                        }
                      >
                        <Icon path={mdiArrowUp} />
                      </Button>
                    </>
                  )}
                  <strong>
                    {picker === "local" ? tr("local") : "Google Drive"}
                  </strong>
                  <span className="cloud-break">
                    {browsePath ||
                      tr(picker === "local" ? "locations" : "driveRoot")}
                  </span>
                </div>
                {picker === "local" ? (
                  <LocalFolderTree
                    selected={browsePath}
                    onSelect={(path) => void browse("local", path)}
                  />
                ) : (
                  <>
                    {" "}
                    <ul className="folder-picker-list">
                      {folders.map((f) => (
                        <li key={f.path}>
                          <button
                            type="button"
                            disabled={busy}
                            onClick={() => void browse(picker, f.path)}
                          >
                            <Icon path={mdiFolderOutline} />
                            <span>{f.name}</span>
                          </button>
                        </li>
                      ))}
                      {!folders.length && (
                        <li className="folder-empty">{tr("noFolders")}</li>
                      )}
                    </ul>
                  </>
                )}
                {picker === "local" && canChoose && (
                  <div className="cloud-new-folder">
                    <label>
                      {tr("folderName")}
                      <input
                        value={folderName}
                        maxLength={255}
                        onChange={(e) => setFolderName(e.target.value)}
                      />
                    </label>
                    <Button
                      title={tr("newFolder")}
                      aria-label={tr("newFolder")}
                      disabled={
                        busy ||
                        !folderName.trim() ||
                        /[\/\\]/.test(folderName) ||
                        [".", ".."].includes(folderName)
                      }
                      onClick={() => void createFolder()}
                    >
                      <Icon path={mdiPlus} />
                    </Button>
                  </div>
                )}
              </div>
            ) : (
              <form
                id="cloud-sync-wizard"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (step === 2) setStep(3);
                  else if (step === 3)
                    void perform({
                      action: "task.create",
                      account: taskAccount,
                      name,
                      local,
                      remote,
                      direction,
                    });
                  else if (taskAccount && !newAccount) setStep(2);
                }}
              >
                {step === 1 && (
                  <>
                    {!reconnect && (
                      <label>
                        {tr("accountStep")}
                        <select
                          value={newAccount ? "new" : taskAccount}
                          onChange={(e) => {
                            setNewAccount(e.target.value === "new");
                            setRemote("");
                            setRemoteChosen(false);
                            setTaskAccount(
                              e.target.value === "new" ? "" : e.target.value,
                            );
                          }}
                        >
                          <option value="" disabled>
                            {tr("chooseAccount")}
                          </option>
                          {state.accounts
                            .filter((a) => a.provider === "drive")
                            .map((a) => (
                              <option key={a.id} value={a.id}>
                                {a.label}
                              </option>
                            ))}
                          <option value="new">{tr("addAccount")}</option>
                        </select>
                      </label>
                    )}
                    {(newAccount || reconnect) && (
                      <>
                        <label>
                          {tr("connectionName")}
                          <input
                            value={label}
                            onChange={(e) => setLabel(e.target.value)}
                            maxLength={100}
                          />
                        </label>
                        {googleConnections.length ? (
                          <label>
                            {tr("googleAccount")}
                            <select
                              value={selectedConnectionId}
                              onChange={(e) => setConnectionId(e.target.value)}
                            >
                              {googleConnections.map((c) => (
                                <option key={c.id} value={c.id}>
                                  {c.email || c.name}
                                </option>
                              ))}
                            </select>
                          </label>
                        ) : (
                          <p>
                            {tr("noLinkedAccounts")}{" "}
                            <a href="/profile/connections">
                              {tr("profileConnections")}
                            </a>
                          </p>
                        )}
                        <p className="muted">{tr("connectionOnly")}</p>
                        {selectedConnectionId && (
                          <GoogleConnect
                            grant={{
                              connectionId: selectedConnectionId,
                              consumer: "cloud-sync",
                              capability: "google-drive",
                            }}
                            onComplete={completeGoogle}
                          />
                        )}
                      </>
                    )}
                  </>
                )}
                {step === 2 && (
                  <>
                    <p className="cloud-account-summary">
                      <Icon path={mdiGoogleDrive} />
                      {chosenAccount?.label}
                    </p>
                    <label>
                      {tr("taskName")}
                      <input
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                        maxLength={100}
                        placeholder={tr("optionalName")}
                      />
                    </label>
                    <div className="cloud-folder-pair">
                      {(["remote", "local"] as const).map((kind) => (
                        <div className="folder-field" key={kind}>
                          <span className="field-label">{tr(kind)}</span>
                          <button
                            className="folder-field-trigger"
                            type="button"
                            onClick={() =>
                              void browse(
                                kind,
                                kind === "local" ? local : remote,
                              )
                            }
                          >
                            <Icon
                              path={
                                kind === "local"
                                  ? mdiFolderOutline
                                  : mdiGoogleDrive
                              }
                            />
                            <span>
                              {kind === "local"
                                ? local || tr("selectFolder")
                                : remoteChosen
                                  ? "/" + remote
                                  : tr("selectFolder")}
                            </span>
                          </button>
                        </div>
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
                    <p className="muted">{tr("taskHelp")}</p>
                  </>
                )}
                {step === 3 && (
                  <>
                    <dl className="cloud-review">
                      <dt>{tr("accountStep")}</dt>
                      <dd>{chosenAccount?.label}</dd>
                      <dt>{tr("remote")}</dt>
                      <dd>/{remote}</dd>
                      <dt>{tr("local")}</dt>
                      <dd>{local}</dd>
                      <dt>{tr("direction")}</dt>
                      <dd>{tr(direction)}</dd>
                    </dl>
                    <Notice>
                      {tr(direction === "both" ? "bothHelp" : "copyHelp")}
                    </Notice>
                  </>
                )}
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
          <DialogContent
            busy={busy}
            className="settings-dialog confirm-dialog"
            header={
              <>
                {" "}
                <Dialog.Title>{tr("removeConfirm")}</Dialog.Title>
                <Dialog.Description>
                  {tr("preserveFiles")}
                </Dialog.Description>{" "}
              </>
            }
            footer={
              <div className="actions">
                <Button
                  disabled={busy}
                  onClick={() => setConfirm(null)}
                  data-dialog-cancel
                >
                  {tr("cancel")}
                </Button>
                <Button
                  className="danger"
                  disabled={busy}
                  onClick={() => confirm && void perform(confirm)}
                >
                  {tr("confirm")}
                </Button>
              </div>
            }
            variant="compact"
            intent="confirm"
            dirty={false}
          ></DialogContent>
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
