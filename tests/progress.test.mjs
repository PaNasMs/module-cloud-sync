import { test } from 'node:test';
import assert from 'node:assert/strict';
import ts from 'typescript';
import { readFileSync } from 'node:fs';
const source = readFileSync(new URL('../frontend/progress.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, {compilerOptions:{module:ts.ModuleKind.ESNext,target:ts.ScriptTarget.ES2022}}).outputText;
const { progressView } = await import('data:text/javascript;base64,' + Buffer.from(js).toString('base64'));
const now = 1700000000000;
const p = { phase: 'transferring', observedAt: now/1000, measured: true, bytes: 40, totalBytes: 100, checks: 4, totalChecks: 10, transfers: 1, totalTransfers: 3, speed: 10, eta: 6 };
test('measured stage progress, unknown totals and finalization', () => {
  assert.deepEqual(progressView(p, false, now), { stale:false, percent:40, eta:6 });
  assert.equal(progressView({...p, totalBytes:0}, false, now).percent, undefined);
  assert.equal(progressView({...p, bytes:100}, false, now).percent, 99);
  assert.equal(progressView({...p, bytes:100}, false, now).eta, undefined);
  assert.equal(progressView({...p, measured:false}, false, now).percent, undefined);
});
test('checking uses files, excludes byte throughput ETA', () => {
  assert.deepEqual(progressView({...p, phase:'checking', checks:7}, false, now), {stale:false,percent:70,eta:undefined});
});
test('lost connection or stale samples hide live estimates', () => {
  for (const v of [progressView(p,true,now),progressView(p,false,now+11000)]) {
    assert.equal(v.stale,true);assert.equal(v.percent,undefined);assert.equal(v.eta,undefined);
  }
});
