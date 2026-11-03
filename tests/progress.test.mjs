import { test } from 'node:test';
import assert from 'node:assert/strict';
import ts from 'typescript';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

function compiledURL(file, imports = {}) {
  const source = readFileSync(new URL(file, import.meta.url), 'utf8');
  let js = ts.transpileModule(source, {compilerOptions:{module:ts.ModuleKind.ESNext,target:ts.ScriptTarget.ES2022,jsx:ts.JsxEmit.ReactJSX}}).outputText;
  for (const [name, url] of Object.entries(imports)) js = js.replaceAll(JSON.stringify(name), JSON.stringify(url));
  return 'data:text/javascript;base64,' + Buffer.from(js).toString('base64');
}
const progressURL = compiledURL('../frontend/progress.ts');
const { progressView } = await import(progressURL);
const { TaskProgress } = await import(compiledURL('../frontend/task-progress.tsx', {
  './progress': progressURL, react: import.meta.resolve('react'), 'react/jsx-runtime': import.meta.resolve('react/jsx-runtime'),
}));
const locales = Object.fromEntries(['en','ru','uk'].map(lang => [lang, JSON.parse(readFileSync(new URL(`../frontend/locales/${lang}.json`, import.meta.url), 'utf8'))]));
const now = 1700000000000;
const p = { phase: 'transferring', observedAt: now/1000, measured: true, ageSeconds:0, bytes: 40, totalBytes: 100, checks: 4, totalChecks: 10, transfers: 1, totalTransfers: 3, speed: 10, eta: 6 };
function render(progress = p, {status='running',paused=0,disconnected=false,language='en',missing=false}={}) {
  return renderToStaticMarkup(createElement(TaskProgress, {task:{name:'Cloud task',status,paused,progress:missing?undefined:progress},disconnected,fetchedAt:Date.now(),translate:key=>locales[language][key] ?? key}));
}

test('growing rclone totals never become a final denominator or 99 percent', () => {
  for (const phase of ['checking','comparing','transferring']) {
    for (const count of [1096,1110,50000]) {
      const html=render({...p,phase,checks:count,totalChecks:count+2,bytes:count,totalBytes:count+2,transfers:count,totalTransfers:count+2});
      assert.match(html,/class="waiting-spinner" aria-hidden="true"/);
      assert.doesNotMatch(html,/<progress[^>]*\bvalue=|aria-valuenow|%|\s\/\s/);
      assert.doesNotMatch(html,/Estimated time/);
      assert.match(html,/Counters show work completed in this stage/);
      assert.ok(html.includes(count.toLocaleString()));
    }
  }
});
test('verification reports reads and completed checks, not transferred files', () => {
  const html=render({...p,phase:'checking'});
  assert.match(html,/Read for verification/);assert.match(html,/Files checked/);assert.match(html,/Speed/);
  assert.doesNotMatch(html,/Files transferred/);
});
test('unknown, growing and fully consumed totals remain indeterminate', () => {
  for (const totalBytes of [0,40,100,1000]) assert.doesNotMatch(render({...p,totalBytes}),/<progress[^>]*\bvalue=|Estimated time/);
});
test('lost connection, stale samples and pausing hide live activity and speed', () => {
  for (const html of [render(p,{disconnected:true}),render({...p,ageSeconds:11}),render(p,{paused:1})]) {
    assert.doesNotMatch(html,/waiting-spinner|<progress|<dt>Speed<\/dt>|Estimated time/);
    assert.match(html,/Files transferred/);
  }
  assert.match(render(p,{disconnected:true}),/Progress has not updated/);
  assert.match(render(p,{disconnected:true,missing:true}),/Progress has not updated/);
  assert.match(render(p,{paused:1}),/Pause requested/);
});
test('terminal states do not keep a running indicator or previous counters', () => {
  for (const status of ['idle','queued','error']) assert.equal(render(p,{status}),'');
});
test('unmeasured stages and dry-run rebuilding do not invent completed counts', () => {
  for (const phase of ['scanning','preparing','rebuilding','repository','finishing']) {
    const html=render({...p,phase});
    assert.doesNotMatch(html,/<dl|%|\bvalue=/);
    assert.match(html,/This stage is running/);
  }
  assert.doesNotMatch(render({...p,measured:false}),/<dl/);
});
test('EN RU UK have explanatory totals and translated verification labels', () => {
  for (const language of ['en','ru','uk']) {
    const html=render({...p,phase:'checking'},{language});
    assert.ok(html.includes(locales[language].progressStageHelp));
    assert.ok(html.includes(locales[language].progressCheckedBytes));
  }
});
test('server clock skew does not affect freshness; elapsed server and client age count', () => {
  assert.equal(progressView(p,true,now,now).stale,true);
  assert.equal(progressView(p,false,now+11000,now).stale,true);
  for (const skew of [-3600,3600]) {
    assert.equal(progressView({...p,observedAt:p.observedAt+skew},false,now,now).stale,false);
    assert.equal(progressView({...p,observedAt:p.observedAt+skew,ageSeconds:11},false,now,now).stale,true);
    assert.equal(progressView({...p,observedAt:p.observedAt+skew,ageSeconds:5},false,now+6000,now).stale,true);
  }
});
