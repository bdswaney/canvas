import assert from 'node:assert/strict';
import test from 'node:test';
import {
  beginComparisonRequest,
  createComparisonRequestController,
  finishComparisonRequest,
  isCurrentComparisonRequest,
} from '../src/diff/comparison.ts';

test('superseded comparison requests abort and cannot finish the current request', () => {
  const state = createComparisonRequestController();
  const first = beginComparisonRequest(state);
  const second = beginComparisonRequest(state);

  assert.equal(first.controller.signal.aborted, true);
  assert.equal(isCurrentComparisonRequest(state, first.id), false);
  assert.equal(isCurrentComparisonRequest(state, second.id), true);
  assert.equal(finishComparisonRequest(state, first.id), false);
  assert.equal(isCurrentComparisonRequest(state, second.id), true);
  assert.equal(finishComparisonRequest(state, second.id), true);
  assert.equal(isCurrentComparisonRequest(state, second.id), false);
});
