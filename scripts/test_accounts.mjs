import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const root = resolve(dirname(new URL(import.meta.url).pathname), "..");
const frontend = resolve(
  process.env.FRONTEND_PACKAGE ?? resolve(root, "../../frontend/package.json"),
);
const frontendRoot = dirname(frontend);
const require = createRequire(frontend);
const { chromium, expect } = require("@playwright/test");
const { createServer } = await import(
  pathToFileURL(require.resolve("vite")).href
);
const { default: react } = await import(
  pathToFileURL(require.resolve("@vitejs/plugin-react")).href
);
const { default: tailwind } = await import(
  pathToFileURL(require.resolve("@tailwindcss/vite")).href
);
const dictionaries = Object.fromEntries(
  await Promise.all(
    ["en", "ru", "uk"].map(async (lang) => [
      lang,
      JSON.parse(
        await readFile(resolve(root, `frontend/locales/${lang}.json`), "utf8"),
      ),
    ]),
  ),
);
const aliases = Object.entries({
  "@fixture/sdk": "src/app/module-sdk.ts",
  "@fixture/registry": "src/app/module-registry.ts",
  "@fixture/i18n": "src/i18n/index.ts",
  "@fixture/interaction": "src/shared/interaction.tsx",
  "@fixture/style": "src/style.css",
  "@fixture/design": "src/design-system.css",
  "@fixture/home": "src/home/components.css",
}).map(([find, path]) => ({ find, replacement: resolve(frontendRoot, path) }));
for (const name of [
  "react/jsx-runtime",
  "react-dom/client",
  "react",
  "react-router-dom",
  "@tanstack/react-query",
])
  aliases.push({
    find: new RegExp("^" + name + "$"),
    replacement: require.resolve(name),
  });

await Promise.all([
  readFile(resolve(root, "dist/ui/index.js")),
  readFile(resolve(root, "dist/ui/index.css")),
]);
const server = await createServer({
  configFile: false,
  root,
  plugins: [react(), tailwind()],
  resolve: { alias: aliases },
  server: {
    host: "127.0.0.1",
    port: 15181,
    strictPort: true,
    fs: { allow: [root, frontendRoot] },
  },
});
await server.listen();
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || "/opt/google/chrome/chrome",
  headless: true,
  args: ["--no-sandbox"],
});
const accountOne = "11111111111111111111111111111111";
const accountTwo = "22222222222222222222222222222222";
const missingAccount = "99999999999999999999999999999999";
const taskOne = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const taskTwo = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
const taskCreated = "cccccccccccccccccccccccccccccccc";
const labelOne = "Studio Drive shared archive";
const labelTwo = "Studio Drive shared archive backup";
const taskNameOne = "Studio photographs";
const taskNameTwo = "Studio photographs archive";
const languages = ["en", "ru", "uk"];
const themes = [
  { theme: "light", surface: "solid" },
  { theme: "light", surface: "glass" },
  { theme: "dark", surface: "solid" },
  { theme: "dark", surface: "glass" },
];
const widths = [320, 1440];

function fixtureURL({
  lang = "en",
  theme = "light",
  surface = "solid",
  width = 1440,
  account,
  sync,
  other,
} = {}) {
  const params = new URLSearchParams({ lang, theme, surface });
  if (account) params.set("account", account);
  if (sync) params.set("sync", sync);
  if (other) params.set("other", other);
  return `http://127.0.0.1:15181/tests/accounts.html?${params}`;
}

async function setup(options = {}) {
  const page = await browser.newPage({
    viewport: { width: options.width ?? 1440, height: 900 },
  });
  const errors = [];
  const fontResponses = [];
  const failedHTTP = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(message.text());
  });
  page.on("response", (response) => {
    if (
      response.request().resourceType() === "font" &&
      response.status() === 200
    )
      fontResponses.push(response.url());
    if (response.status() >= 400)
      failedHTTP.push(
        `${new URL(response.url()).pathname}:${response.status()}`,
      );
  });
  await page.goto(fixtureURL(options));
  const text = dictionaries[options.lang ?? "en"];
  await expect(
    page.getByRole("heading", { name: text.title, exact: true }),
  ).toBeVisible();
  const loaded = await page.evaluate(async (sample) => {
    await document.fonts.ready;
    const faces = await document.fonts.load('16px "Manrope Variable"', sample);
    return {
      loaded: faces.some(
        (face) =>
          face.family === "Manrope Variable" && face.status === "loaded",
      ),
      family: getComputedStyle(document.body).fontFamily,
      checked: document.fonts.check('16px "Manrope Variable"', sample),
    };
  }, `${labelTwo} · ${taskNameTwo}`);
  assert.ok(loaded.loaded, "the Manrope font face must actually load");
  assert.match(loaded.family, /Manrope Variable/);
  assert.equal(loaded.checked, true);
  await expect(page.locator("html")).toHaveAttribute(
    "data-theme",
    options.theme ?? "light",
  );
  await expect(page.locator("html")).toHaveAttribute(
    "data-surface",
    options.surface ?? "solid",
  );
  return {
    page,
    text,
    errors,
    fontResponses,
    failedHTTP,
    location: () => page.evaluate(() => window.cloudAccountFixture.location()),
    fixtureState: () =>
      page.evaluate(() => ({
        accounts: window.cloudAccountFixture.accounts,
        tasks: window.cloudAccountFixture.tasks,
        writes: window.cloudAccountFixture.writes,
        apiErrors: window.cloudAccountFixture.apiErrors,
      })),
    refresh: () => page.evaluate(() => window.cloudAccountFixture.refresh()),
    mutate: (operation, value) =>
      page.evaluate(
        ([name, item]) => {
          const fixture = window.cloudAccountFixture;
          if (name === "removeTask")
            fixture.tasks.splice(
              0,
              fixture.tasks.length,
              ...fixture.tasks.filter((task) => task.id !== item),
            );
          if (name === "removeAccount")
            fixture.accounts.splice(
              0,
              fixture.accounts.length,
              ...fixture.accounts.filter((account) => account.id !== item),
            );
          if (name === "removeAll") {
            fixture.accounts.splice(0);
            fixture.tasks.splice(0);
          }
        },
        [operation, value],
      ),
    navigate: (location) =>
      page.evaluate(
        (value) => window.cloudAccountFixture.navigate(value),
        location,
      ),
    back: () => page.evaluate(() => window.cloudAccountFixture.back()),
  };
}

async function apiState(test) {
  return test.fixtureState();
}

async function accountView(test, label, width = 1440) {
  await expect(test.page.locator(".settings-content h2").first()).toHaveText(
    label,
  );
  await expect(test.page.locator(".cloud-connection-section")).toContainText(
    label,
  );
  await expect(
    test.page
      .getByRole("button", { name: test.text.addTask, exact: true })
      .first(),
  ).toBeVisible();
  assert.equal(width, test.page.viewportSize().width);
}

async function openTaskDelete(test, name) {
  const trigger = test.page.getByRole("button", {
    name: test.text.removeTask,
    exact: true,
  });
  await expect(trigger).toBeVisible();
  await trigger.click();
  const dialog = test.page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  return dialog;
}

async function hitTargets(locator, viewportWidth) {
  let observed;
  try {
    await expect(async () => {
      const shape = await locator.evaluate((nav) => {
        const bounds = (element) => {
          const rect = element.getBoundingClientRect();
          return {
            left: rect.left,
            right: rect.right,
            top: rect.top,
            bottom: rect.bottom,
            width: rect.width,
            height: rect.height,
          };
        };
        const items = [...nav.querySelectorAll("button")].filter(
          (button) => button.getClientRects().length,
        );
        return {
          nav: bounds(nav),
          items: items.map((button) => {
            const rect = button.getBoundingClientRect();
            const points = [
              [rect.left + rect.width / 2, rect.top + rect.height / 2],
              [rect.left + 4, rect.top + rect.height / 2],
              [rect.right - 4, rect.top + rect.height / 2],
              [rect.left + rect.width / 2, rect.top + 4],
              [rect.left + rect.width / 2, rect.bottom - 4],
            ];
            return {
              box: bounds(button),
              hits: points.map(([x, y]) => {
                const target = document.elementFromPoint(x, y);
                return target === button || button.contains(target);
              }),
            };
          }),
          viewport: {
            width: innerWidth,
            height: innerHeight,
            scrollWidth: document.documentElement.scrollWidth,
          },
        };
      });
      observed = shape;
      assert.ok(
        shape.nav.left >= -1 && shape.nav.right <= viewportWidth + 1,
        "navigation must fit the viewport width",
      );
      assert.ok(
        shape.nav.top >= 0 && shape.nav.bottom <= shape.viewport.height + 1,
        "navigation must remain reachable",
      );
      assert.ok(
        shape.viewport.scrollWidth <= shape.viewport.width + 1,
        "navigation must not cause page horizontal overflow",
      );
      assert.ok(shape.items.length > 0, "navigation items must render");
      assert.ok(
        shape.items.every(
          ({ box, hits }) =>
            box.left >= -1 &&
            box.right <= viewportWidth + 1 &&
            box.top >= 0 &&
            box.bottom <= shape.viewport.height + 1 &&
            box.width >= 43.9 &&
            box.height >= 43.9 &&
            hits.every(Boolean),
        ),
        "navigation buttons must meet target, bounds and hit-test requirements",
      );
    }).toPass({ timeout: 4000 });
  } catch (error) {
    throw new Error(
      `${error.message}\nLast navigation geometry: ${JSON.stringify(observed)}`,
    );
  }
}

async function checkNavigation(test, width) {
  const page = test.page;
  if (width <= 640) {
    const switcher = page.locator(".section-switcher");
    await expect(switcher).toBeVisible();
    await expect(async () => {
      const rect = await switcher.evaluate((button) => {
        const box = button.getBoundingClientRect();
        return {
          left: box.left,
          right: box.right,
          top: box.top,
          bottom: box.bottom,
          width: box.width,
          height: box.height,
          viewportWidth: innerWidth,
          viewportHeight: innerHeight,
        };
      });
      assert.ok(
        rect.left >= 0 &&
          rect.right <= rect.viewportWidth &&
          rect.top >= 0 &&
          rect.bottom <= rect.viewportHeight,
      );
      assert.ok(rect.width >= 43.9 && rect.height >= 43.9);
    }).toPass({ timeout: 4000 });
    await switcher.click();
    const sheet = page.getByRole("dialog");
    await expect(sheet).toBeVisible();
    await hitTargets(sheet.locator(".section-nav"), width);
    await page.keyboard.press("Escape");
    await expect(sheet).toHaveCount(0);
  } else {
    await hitTargets(page.locator("nav.section-nav").first(), width);
  }
}

async function assertClean(test, expectedWrites = 0) {
  const state = await apiState(test);
  assert.equal(
    state.writes.length,
    expectedWrites,
    `mock API writes: ${JSON.stringify(state.writes)}`,
  );
  assert.deepEqual(
    state.apiErrors,
    [],
    "fixture should receive only declared mock API requests",
  );
  assert.deepEqual(test.errors, [], "browser console and page errors");
  assert.deepEqual(test.failedHTTP, [], "unexpected failed HTTP responses");
  assert.ok(
    test.fontResponses.length > 0,
    "a font resource must load successfully",
  );
  assert.ok(
    await test.page.evaluate(() =>
      document.fonts.check(
        '16px "Manrope Variable"',
        "Studio Drive shared archive backup",
      ),
    ),
  );
}

async function accountButton(test, label) {
  const button = test.page
    .locator("nav.section-nav")
    .getByRole("button", { name: new RegExp("^" + label + " Google Drive") });
  await expect(button).toBeVisible();
  return button;
}

try {
  for (const lang of languages)
    for (const { theme, surface } of themes)
      for (const width of widths) {
        const test = await setup({
          lang,
          theme,
          surface,
          width,
          account: accountOne,
          sync: taskTwo,
        });
        await expect(
          test.page.locator(".settings-content h2").first(),
        ).toHaveText(taskNameTwo);
        await expect(
          test.page.locator(".cloud-connection-section"),
        ).toContainText(labelTwo);
        await expect(test.page.locator("nav.section-nav")).toContainText(
          labelOne,
        );
        await expect(test.page.locator("nav.section-nav")).toContainText(
          labelTwo,
        );
        const current = await test.location();
        assert.equal(
          current.search,
          `?account=${accountTwo}&sync=${taskTwo}`,
          "task selection must select and canonicalize its actual owner",
        );
        await checkNavigation(test, width);
        await assertClean(test);
        await test.page.close();
      }

  {
    const test = await setup({
      account: accountTwo,
      sync: taskTwo,
      other: "retain-this",
    });
    await expect(test.page.locator(".settings-content h2").first()).toHaveText(
      taskNameTwo,
    );
    await test.page.reload();
    await expect(test.page.locator(".settings-content h2").first()).toHaveText(
      taskNameTwo,
    );
    assert.equal(
      (await test.location()).search,
      `?account=${accountTwo}&sync=${taskTwo}&other=retain-this`,
    );
    await accountButton(test, labelOne).then((button) => button.click());
    await expect(test.page.locator(".settings-content h2").first()).toHaveText(
      labelOne,
    );
    assert.equal(
      (await test.location()).search,
      `?account=${accountOne}&sync=account%3A${accountOne}&other=retain-this`,
    );
    await test.back();
    await expect(test.page.locator(".settings-content h2").first()).toHaveText(
      taskNameTwo,
    );
    assert.equal(
      (await test.location()).search,
      `?account=${accountTwo}&sync=${taskTwo}&other=retain-this`,
    );
    await assertClean(test);
    await test.page.close();
  }

  {
    const test = await setup();
    await expect(test.page.locator(".settings-content h2").first()).toHaveText(
      taskNameOne,
    );
    assert.equal(
      (await test.location()).search,
      `?account=${accountOne}&sync=${taskOne}`,
    );
    await assertClean(test);
    await test.page.close();
  }

  {
    const test = await setup({
      account: accountTwo,
      sync: `account:${accountTwo}`,
    });
    await test.mutate("removeTask", taskTwo);
    await test.refresh();
    await accountView(test, labelTwo);
    assert.equal(
      (await test.location()).search,
      `?account=${accountTwo}&sync=account%3A${accountTwo}`,
    );
    const disconnect = test.page
      .locator(".cloud-connection-section")
      .getByRole("button", { name: test.text.disconnect, exact: true });
    await expect(disconnect).toBeEnabled();
    const state = await apiState(test);
    assert.equal(
      state.tasks.some((task) => task.account === accountTwo),
      false,
    );
    assert.equal(state.writes.length, 0);
    await assertClean(test);
    await test.page.close();
  }

  {
    const test = await setup({
      account: accountTwo,
      sync: taskTwo,
      other: "keep",
    });
    const dialog = await openTaskDelete(test, taskNameTwo);
    await dialog
      .getByRole("button", { name: test.text.cancel, exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    assert.equal(
      (await apiState(test)).writes.length,
      0,
      "cancelled task removal must not call the mock API",
    );
    await openTaskDelete(test, taskNameTwo);
    const currentDialog = test.page.getByRole("dialog");
    await currentDialog
      .getByRole("button", { name: test.text.removeTask, exact: true })
      .click();
    await expect(currentDialog).toHaveCount(0);
    await accountView(test, labelTwo);
    assert.equal(
      (await test.location()).search,
      `?account=${accountTwo}&sync=account%3A${accountTwo}&other=keep`,
    );
    const taskWrite = (await apiState(test)).writes[0];
    assert.deepEqual(taskWrite.body, { action: "task.remove", id: taskTwo });
    await expect(
      test.page
        .locator(".cloud-connection-section")
        .getByRole("button", { name: test.text.disconnect, exact: true }),
    ).toBeEnabled();
    await test.page
      .getByRole("button", { name: test.text.addTask, exact: true })
      .last()
      .click();
    const wizard = test.page.getByRole("dialog");
    await expect(wizard).toBeVisible();
    await expect(wizard).toContainText(labelTwo);
    await expect(
      wizard.getByRole("button", { name: test.text.next, exact: true }),
    ).toBeDisabled();
    await wizard.locator(".folder-field-trigger").nth(0).click();
    await expect(wizard.locator(".folder-picker")).toBeVisible();
    await wizard
      .locator(".modal-footer")
      .getByRole("button", { name: test.text.selectFolder, exact: true })
      .click();
    await wizard.locator(".folder-field-trigger").nth(1).click();
    await expect(wizard.locator(".cloud-tree-name")).toBeVisible();
    await wizard.locator(".cloud-tree-name").click();
    await wizard
      .locator(".modal-footer")
      .getByRole("button", { name: test.text.selectFolder, exact: true })
      .click();
    await expect(
      wizard.getByRole("button", { name: test.text.next, exact: true }),
    ).toBeEnabled();
    await wizard
      .getByRole("button", { name: test.text.next, exact: true })
      .click();
    await expect(
      wizard.getByRole("button", { name: test.text.start, exact: true }),
    ).toBeEnabled();
    await wizard
      .getByRole("button", { name: test.text.start, exact: true })
      .click();
    await expect(wizard).toHaveCount(0);
    const createWrite = (await apiState(test)).writes[1];
    assert.deepEqual(createWrite.body, {
      action: "task.create",
      account: accountTwo,
      name: "",
      local: "/home/fixture",
      remote: "",
      direction: "download",
    });
    await expect(test.page.locator(".settings-content h2").first()).toHaveText(
      "Fixture task",
    );
    assert.equal(
      (await test.location()).search,
      `?account=${accountTwo}&sync=${taskCreated}&other=keep`,
    );
    await assertClean(test, 2);
    await test.page.close();
  }

  {
    const test = await setup({
      account: accountTwo,
      sync: `account:${accountTwo}`,
    });
    await test.mutate("removeTask", taskTwo);
    await test.refresh();
    const disconnect = test.page
      .locator(".cloud-connection-section")
      .getByRole("button", { name: test.text.disconnect, exact: true });
    await expect(disconnect).toBeEnabled();
    await disconnect.click();
    let dialog = test.page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await dialog
      .getByRole("button", { name: test.text.cancel, exact: true })
      .click();
    assert.equal(
      (await apiState(test)).writes.length,
      0,
      "cancelled account removal must not call the mock API",
    );
    await disconnect.click();
    dialog = test.page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await dialog
      .getByRole("button", { name: test.text.disconnect, exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    const state = await apiState(test);
    assert.deepEqual(state.writes[0].body, {
      action: "account.remove",
      id: accountTwo,
    });
    assert.equal(
      state.accounts.some((account) => account.id === accountTwo),
      false,
    );
    await expect(test.page.locator(".settings-content h2").first()).toHaveText(
      taskNameOne,
    );
    assert.equal(
      (await test.location()).search,
      `?account=${accountOne}&sync=${taskOne}`,
    );
    await assertClean(test, 1);
    await test.page.close();
  }

  {
    const test = await setup({
      account: accountTwo,
      sync: `account:${accountTwo}`,
    });
    await test.mutate("removeTask", taskTwo);
    await test.refresh();
    await test.mutate("removeAccount", accountTwo);
    await test.refresh();
    await expect(
      test.page.getByRole("heading", {
        name: test.text.selectionUnavailable,
        exact: true,
      }),
    ).toBeVisible();
    await expect(test.page.locator(".cloud-connection-section")).toHaveCount(0);
    assert.equal(
      (await test.location()).search,
      `?account=${accountTwo}&sync=account%3A${accountTwo}`,
    );
    await expect(test.page.locator("nav.section-nav")).toContainText(labelOne);
    assert.equal((await apiState(test)).writes.length, 0);
    await assertClean(test);
    await test.page.close();
  }

  {
    const test = await setup({
      account: missingAccount,
      sync: `account:${missingAccount}`,
    });
    await expect(
      test.page.getByRole("heading", {
        name: test.text.selectionUnavailable,
        exact: true,
      }),
    ).toBeVisible();
    await expect(test.page.locator(".settings-content")).not.toContainText(
      labelOne,
    );
    await expect(test.page.locator(".settings-content")).not.toContainText(
      labelTwo,
    );
    assert.equal((await apiState(test)).writes.length, 0);
    await assertClean(test);
    await test.page.close();
  }

  {
    const test = await setup();
    await test.mutate("removeAll");
    await test.refresh();
    await expect(
      test.page.getByRole("heading", {
        name: test.text.noAccounts,
        exact: true,
      }),
    ).toBeVisible();
    assert.equal((await test.location()).search, "");
    await expect(
      test.page.getByRole("button", {
        name: test.text.addAccount,
        exact: true,
      }),
    ).toBeVisible();
    await test.page
      .getByRole("button", { name: test.text.addAccount, exact: true })
      .click();
    const wizard = test.page.getByRole("dialog");
    await expect(wizard).toBeVisible();
    await expect(wizard).toContainText(test.text.addAccount);
    assert.equal((await apiState(test)).writes.length, 0);
    await assertClean(test);
    await test.page.close();
  }

  console.log(
    "PASS UX11 accounts: account/task URL resolution, reload/back, empty account after last-task removal, exact task-create/account-remove IDs, stale selection, no-accounts CTA, EN/RU/UK × four themes/surfaces × 320/1440, fonts and navigation hit targets",
  );
} finally {
  await browser.close();
  await server.close();
}
