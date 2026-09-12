import assert from 'node:assert/strict';
import test from 'node:test';
import { DiffError, diffMarkdown } from '../src/diff/diff.ts';

function allLines(result) {
  return result.hunks.flatMap((hunk) => hunk.lines);
}

test('equal and empty Markdown have no hunks', () => {
  assert.deepEqual(diffMarkdown('', ''), { changed: false, hunks: [] });
  assert.equal(diffMarkdown('# same\nbody', '# same\nbody').changed, false);
  assert.deepEqual(allLines(diffMarkdown('', '# draft')).map((line) => line.kind), ['added']);
});

test('additions and deletions include readable line numbers', () => {
  const added = diffMarkdown('one\ntwo', 'one\ntwo\nthree', { context: 0 });
  assert.equal(added.changed, true);
  assert.deepEqual(added.hunks[0], {
    oldStart: 2,
    oldCount: 1,
    newStart: 2,
    newCount: 2,
    lines: [
      { kind: 'removed', text: 'two', oldLine: 2, newLine: null, words: [{ kind: 'equal', text: 'two' }] },
      { kind: 'added', text: 'two', oldLine: null, newLine: 2, words: [{ kind: 'equal', text: 'two' }] },
      { kind: 'added', text: 'three', oldLine: null, newLine: 3, words: [{ kind: 'added', text: 'three' }] },
    ],
  });

  const deleted = diffMarkdown('one\ntwo\nthree', 'one\ntwo', { context: 0 });
  assert.equal(deleted.hunks[0].oldStart, 2);
  assert.equal(deleted.hunks[0].newStart, 2);
  assert.deepEqual(deleted.hunks[0].lines.map((line) => line.kind), ['removed', 'added', 'removed']);
  assert.deepEqual(deleted.hunks[0].lines[2].words, [{ kind: 'removed', text: 'three' }]);
});

test('replacements retain context and word-level detail', () => {
  const result = diffMarkdown('before\nThe quick fox\nafter', 'before\nThe slow fox\nafter', { context: 1 });
  assert.equal(result.hunks.length, 1);
  assert.deepEqual(result.hunks[0].lines.map((line) => line.kind), ['context', 'removed', 'added', 'context']);
  assert.deepEqual(result.hunks[0].lines[1].words, [
    { kind: 'equal', text: 'The ' },
    { kind: 'removed', text: 'quick' },
    { kind: 'equal', text: ' fox' },
  ]);
  assert.deepEqual(result.hunks[0].lines[2].words, [
    { kind: 'equal', text: 'The ' },
    { kind: 'added', text: 'slow' },
    { kind: 'equal', text: ' fox' },
  ]);
});

test('Markdown fences and tables are treated as ordinary lines', () => {
  const oldText = ['```md', '| Name | Value |', '| --- | --- |', '| one | old |', '```'].join('\n');
  const newText = ['```md', '| Name | Value |', '| --- | --- |', '| one | new |', '```'].join('\n');
  const result = diffMarkdown(oldText, newText);
  const changed = allLines(result).filter((line) => line.kind !== 'context');
  assert.deepEqual(changed.map((line) => line.text), ['| one | old |', '| one | new |']);
  assert.equal(allLines(result).filter((line) => line.kind === 'context').some((line) => line.text === '```md'), true);
});

test('Unicode and emoji remain intact in line and word changes', () => {
  const result = diffMarkdown('café 😀', 'café 🌍 😀', { context: 0 });
  const changed = allLines(result).filter((line) => line.kind !== 'context');
  assert.deepEqual(changed.map((line) => line.text), ['café 😀', 'café 🌍 😀']);
  assert.equal(changed.every((line) => !line.text.includes('\ud83d') || line.text.includes('😀') || line.text.includes('🌍')), true);
  assert.equal(changed[1].words.some((word) => word.text.includes('🌍')), true);
});

test('configured byte and line bounds return explicit errors', () => {
  assert.throws(() => diffMarkdown('12345', '12345', { maxBytes: 4 }), (error) => {
    assert.ok(error instanceof DiffError);
    assert.equal(error.code, 'too-large');
    return true;
  });
  assert.throws(() => diffMarkdown('a\nb', 'a\nb', { maxLines: 3 }), (error) => {
    assert.ok(error instanceof DiffError);
    assert.equal(error.code, 'too-large');
    return true;
  });
});

test('line-ending-only changes are changed and explain their separators', () => {
  for (const [oldText, newText, ending] of [['a\r\nb', 'a\nb', 'CRLF'], ['a\rb', 'a\nb', 'CR']]) {
    const result = diffMarkdown(oldText, newText, { context: 0 });
    assert.equal(result.changed, true);
    const changed = allLines(result).filter((line) => line.kind !== 'context');
    assert.deepEqual(changed.map((line) => line.kind), ['removed', 'added']);
    assert.equal(changed[0].lineEnding, ending);
    assert.equal(changed[1].lineEnding, undefined);
  }
  assert.equal(diffMarkdown('a\nb', 'a\nb\n').changed, true);
});

test('separator changes remain visible beside additions and deletions', () => {
  const lfAddition = allLines(diffMarkdown('a\nb', 'a\nb\nc', { context: 0 }));
  assert.deepEqual(lfAddition.map((line) => [line.kind, line.text]), [
    ['removed', 'b'],
    ['added', 'b'],
    ['added', 'c'],
  ]);
  assert.equal(lfAddition[0].lineEnding, undefined);
  assert.equal(lfAddition[1].lineEnding, undefined);

  const crlfChange = allLines(diffMarkdown('a\r\nb\r\nc', 'a\r\nb\nc', { context: 0 }));
  const crlfChanged = crlfChange.filter((line) => line.kind !== 'context');
  assert.deepEqual(crlfChanged.map((line) => [line.kind, line.text]), [
    ['removed', 'b'],
    ['added', 'b'],
  ]);
  assert.equal(crlfChanged[0].lineEnding, 'CRLF');
  assert.equal(crlfChanged[1].lineEnding, undefined);

  const crDeletion = allLines(diffMarkdown('a\rb\nc', 'a\nb', { context: 0 }));
  assert.deepEqual(crDeletion.map((line) => [line.kind, line.text]), [
    ['removed', 'a'],
    ['added', 'a'],
    ['removed', 'b'],
    ['added', 'b'],
    ['removed', 'c'],
  ]);
  assert.equal(crDeletion[0].lineEnding, 'CR');
  // LF is intentionally compact in the public line metadata; the paired
  // removed/added lines still make the exact separator change observable.
  assert.equal(crDeletion[1].lineEnding, undefined);
});

test('large edit distance fails before Myers trace can grow without bound', () => {
  const oldText = Array.from({ length: 1_000 }, (_, index) => `old-${index}`).join('\n');
  const newText = Array.from({ length: 1_000 }, (_, index) => `new-${index}`).join('\n');
  assert.throws(() => diffMarkdown(oldText, newText), (error) => {
    assert.ok(error instanceof DiffError);
    assert.equal(error.code, 'too-large');
    return true;
  });
});

test('word token budgets reject pathological changed lines', () => {
  const oldText = Array.from({ length: 60_000 }, () => 'old').join(' ');
  const newText = Array.from({ length: 60_000 }, () => 'new').join(' ');
  assert.throws(() => diffMarkdown(oldText, newText), (error) => {
    assert.ok(error instanceof DiffError);
    assert.equal(error.code, 'too-large');
    return true;
  });
});
