import assert from 'node:assert/strict';
import test from 'node:test';
import { createComparisonRequestController, beginComparisonRequest } from '../src/diff/comparison.ts';
import { createWorkspaceComparisonEntries } from '../src/diff/workspaceComparison.ts';

test('Workspace comparison entry points invoke both idle handlers', async () => {
  const state = createComparisonRequestController();
  const calls = [];
  const entries = createWorkspaceComparisonEntries(state, {
    captureLiveComparison: async () => calls.push('live'),
    compareSavedVersions: async (version) => calls.push(`saved:${version}`),
  });

  await entries.captureLiveComparison();
  await entries.compareSavedVersions(7);

  assert.deepEqual(calls, ['live', 'saved:7']);
});

test('Workspace comparison entry points suppress handlers only while active', async () => {
  const state = createComparisonRequestController();
  const calls = [];
  const entries = createWorkspaceComparisonEntries(state, {
    captureLiveComparison: async () => calls.push('live'),
    compareSavedVersions: async (version) => calls.push(`saved:${version}`),
  });
  const active = beginComparisonRequest(state);

  await entries.captureLiveComparison();
  await entries.compareSavedVersions(7);
  assert.deepEqual(calls, []);

  active.controller.abort();
  state.active = null;
  await entries.captureLiveComparison();
  await entries.compareSavedVersions(7);
  assert.deepEqual(calls, ['live', 'saved:7']);
});
