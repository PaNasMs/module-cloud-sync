import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryRouter,
  RouterProvider,
  useLocation,
} from "react-router-dom";
import { initializeLanguage } from "@fixture/i18n";
import { modules } from "@fixture/registry";
import { DirtyFormsProvider } from "@fixture/interaction";
import "@fixture/sdk";
import "@fixture/style";
import "@fixture/design";
import "@fixture/home";

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
  recovery?: string;
  retry_at?: number;
  status: string;
  error: string;
  last_sync: number;
};
type FixtureState = {
  accounts: Account[];
  tasks: Task[];
  writes: { body: Record<string, unknown> }[];
  apiErrors: string[];
  refresh: () => Promise<void>;
  navigate: (location: string) => Promise<void>;
  back: () => Promise<void>;
  location: () => { pathname: string; search: string };
  holdNextAction: () => void;
  waitForHeldAction: () => Promise<void>;
  releaseHeldAction: () => void;
  waitForStateFetch: () => Promise<void>;
  releaseStateFetch: () => void;
};

declare global {
  interface Window {
    cloudAccountFixture: FixtureState;
  }
}

const params = new URLSearchParams(location.search);
function deferred() {
  let start!: () => void;
  let release!: () => void;
  const started = new Promise<void>((resolve) => {
    start = resolve;
  });
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { started, pending, start, release };
}
let nextActionGate: ReturnType<typeof deferred> | null = null;
let heldActionGate: ReturnType<typeof deferred> | null = null;
let stateFetchGate = params.get("fixtureHoldState") === "1" ? deferred() : null;
const heldStateFetchGate = stateFetchGate;
const lang = params.get("lang") ?? "en";
await initializeLanguage(lang);
document.documentElement.dataset.theme = params.get("theme") ?? "light";
document.documentElement.dataset.surface = params.get("surface") ?? "solid";
const style = document.createElement("link");
style.rel = "stylesheet";
style.href = "/dist/ui/index.css";
document.head.append(style);
await new Promise<void>((resolve, reject) => {
  const script = document.createElement("script");
  script.src = "/dist/ui/index.js";
  script.onload = () => resolve();
  script.onerror = () =>
    reject(Error("Built Cloud Sync bundle failed to load"));
  document.head.append(script);
});
const module = modules().find((item) => item.id === "cloud-sync");
if (!module?.component) throw Error("Cloud Sync module did not register");
const Page = module.component;

const firstAccount = "11111111111111111111111111111111";
const secondAccount = "22222222222222222222222222222222";
const firstTask = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const secondTask = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
const accounts: Account[] = [
  {
    id: firstAccount,
    provider: "drive",
    label: "Studio Drive shared archive",
    identity: "fixture-drive-identity-one",
    error: "",
  },
  {
    id: secondAccount,
    provider: "drive",
    label: "Studio Drive shared archive backup",
    identity: "fixture-drive-identity-two",
    error: "",
  },
];
const tasks: Task[] = [
  {
    id: firstTask,
    account: firstAccount,
    name: "Studio photographs",
    local: "/home/fixture/Photos",
    remote: "Photos",
    direction: "download",
    paused: 0,
    status: "idle",
    error: "",
    last_sync: 10,
  },
  {
    id: secondTask,
    account: secondAccount,
    name: "Studio photographs archive",
    local: "/home/fixture/Archive",
    remote: "Archive",
    direction: "download",
    paused: 0,
    status: "idle",
    error: "",
    last_sync: 20,
  },
];
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
    mutations: { retry: false },
  },
});
if (params.get("fixtureStale") === "1")
  queryClient.setQueryData(["cloud-sync"], {
    accounts: structuredClone(accounts),
    tasks: structuredClone(tasks.filter((task) => task.id !== secondTask)),
    history: [],
  });
const initialSearch = new URLSearchParams();
for (const [key, value] of params.entries()) {
  if (
    !["lang", "theme", "surface", "fixtureStale", "fixtureHoldState"].includes(
      key,
    )
  )
    initialSearch.set(key, value);
}
const initialEntry =
  "/cloud-sync" + (initialSearch.size ? "?" + initialSearch.toString() : "");
function FixtureRoute() {
  const { pathname } = useLocation();
  return (
    <DirtyFormsProvider>
      <main>{pathname === "/elsewhere" ? <h1>Elsewhere</h1> : <Page />}</main>
    </DirtyFormsProvider>
  );
}
const router = createMemoryRouter(
  [
    {
      path: "*",
      element: <FixtureRoute />,
    },
  ],
  { initialEntries: [initialEntry] },
);
const fixture: FixtureState = {
  accounts,
  tasks,
  writes: [],
  apiErrors: [],
  refresh: async () => {
    await queryClient.invalidateQueries({ queryKey: ["cloud-sync"] });
  },
  navigate: async (location) => {
    await router.navigate(
      location.startsWith("/") ? location : "/cloud-sync?" + location,
    );
  },
  back: async () => {
    await router.navigate(-1);
  },
  holdNextAction: () => {
    nextActionGate = deferred();
    heldActionGate = nextActionGate;
  },
  waitForHeldAction: () => heldActionGate?.started ?? Promise.resolve(),
  releaseHeldAction: () => heldActionGate?.release(),
  waitForStateFetch: () => heldStateFetchGate?.started ?? Promise.resolve(),
  releaseStateFetch: () => heldStateFetchGate?.release(),
  location: () => ({
    pathname: router.state.location.pathname,
    search: router.state.location.search,
  }),
};
window.cloudAccountFixture = fixture;

window.fetch = async (input, init) => {
  const raw =
    typeof input === "string"
      ? input
      : input instanceof URL
        ? input.href
        : input.url;
  const url = new URL(raw, location.href);
  const method = (
    init?.method ?? (input instanceof Request ? input.method : "GET")
  ).toUpperCase();
  if (
    url.pathname === "/api/v1/module-api/cloud-sync/state" &&
    method === "GET"
  ) {
    if (stateFetchGate) {
      const gate = stateFetchGate;
      stateFetchGate = null;
      gate.start();
      await gate.pending;
    }
    return Response.json({
      accounts: structuredClone(fixture.accounts),
      tasks: structuredClone(fixture.tasks),
      history: [],
    });
  }
  if (
    url.pathname === "/api/v1/module-api/cloud-sync/action" &&
    method === "POST"
  ) {
    const body = (init?.body ? JSON.parse(String(init.body)) : {}) as Record<
      string,
      unknown
    >;
    if (body.action === "folders") return Response.json({ folders: [] });
    if (body.action === "local.folders") {
      if (body.path === "")
        return Response.json({
          roots: [
            { name: "Fixture home", path: "/home/fixture", kind: "home" },
          ],
          folders: [],
          selectable: true,
        });
      return Response.json({ folders: [], selectable: true });
    }
    fixture.writes.push({ body: structuredClone(body) });
    if (body.action === "task.remove") {
      if (nextActionGate) {
        const gate = nextActionGate;
        nextActionGate = null;
        gate.start();
        await gate.pending;
      }
      const index = fixture.tasks.findIndex((task) => task.id === body.id);
      if (index < 0 || fixture.tasks[index].status === "running") {
        return Response.json(
          { error: "Fixture task unavailable" },
          { status: 409 },
        );
      }
      fixture.tasks.splice(index, 1);
      return Response.json({});
    }
    if (body.action === "account.remove") {
      const accountIndex = fixture.accounts.findIndex(
        (account) => account.id === body.id,
      );
      if (
        accountIndex < 0 ||
        fixture.tasks.some((task) => task.account === body.id)
      ) {
        return Response.json(
          { error: "Fixture account unavailable" },
          { status: 409 },
        );
      }
      fixture.accounts.splice(accountIndex, 1);
      return Response.json({});
    }
    if (body.action === "task.create") {
      const accountID = String(body.account ?? "");
      if (!fixture.accounts.some((account) => account.id === accountID)) {
        return Response.json(
          { error: "Fixture account unavailable" },
          { status: 409 },
        );
      }
      const id = "cccccccccccccccccccccccccccccccc";
      fixture.tasks.push({
        id,
        account: accountID,
        name: String(body.name || "Fixture task"),
        local: String(body.local ?? ""),
        remote: String(body.remote ?? ""),
        direction: String(body.direction ?? "download"),
        paused: 0,
        status: "idle",
        error: "",
        last_sync: 30,
      });
      return Response.json({ id });
    }
    fixture.apiErrors.push("Unexpected action " + String(body.action));
    return Response.json(
      { error: "Unexpected fixture action" },
      { status: 404 },
    );
  }
  if (url.pathname === "/api/v1/external/connections" && method === "GET")
    return Response.json([]);
  fixture.apiErrors.push(`Unexpected fixture API ${method} ${url.pathname}`);
  return Response.json({ error: "Unexpected fixture API" }, { status: 404 });
};

createRoot(document.getElementById("root")!).render(
  <QueryClientProvider client={queryClient}>
    <RouterProvider router={router} />
  </QueryClientProvider>,
);
