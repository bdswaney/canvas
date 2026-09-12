export type DiffLineKind = 'context' | 'added' | 'removed';
export type WordChangeKind = 'equal' | 'added' | 'removed';

export type WordChange = {
  kind: WordChangeKind;
  text: string;
};

export type DiffLine = {
  kind: DiffLineKind;
  text: string;
  oldLine: number | null;
  newLine: number | null;
  // LF and an unterminated final line are omitted for compactness. CRLF/CR
  // are retained so separator-only changes remain explainable to readers.
  lineEnding?: 'CRLF' | 'CR';
  words?: WordChange[];
};

export type DiffHunk = {
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
  lines: DiffLine[];
};

export type MarkdownDiff = {
  changed: boolean;
  hunks: DiffHunk[];
};

export type DiffOptions = {
  context?: number;
  maxBytes?: number;
  maxLines?: number;
  maxTokens?: number;
  maxWork?: number;
  maxEditDistance?: number;
};

// Each saved artifact is bounded to 8 MiB by the API. The client permits
// one full pair while still refusing unbounded work before the sequence diff.
export const maxDiffBytes = 16 << 20;
export const maxDiffLines = 20_000;
export const maxDiffTokens = 100_000;
export const maxDiffWork = 4_000_000;
export const maxDiffEditDistance = 8_192;
export const defaultDiffContext = 3;

export class DiffError extends Error {
  readonly code: 'too-large' | 'invalid';

  constructor(code: 'too-large' | 'invalid', message: string) {
    super(message);
    this.name = 'DiffError';
    this.code = code;
  }
}

type Edit<T> =
  | { kind: 'equal'; value: T }
  | { kind: 'added'; value: T }
  | { kind: 'removed'; value: T };

type LineToken = {
  text: string;
  ending: 'lf' | 'crlf' | 'cr' | 'none';
};

type DiffBudget = {
  maxTokens: number;
  maxWork: number;
  maxEditDistance: number;
  tokens: number;
  work: number;
};

function tooLarge(message: string): never {
  throw new DiffError('too-large', message);
}

function consumeWork(budget: DiffBudget, amount = 1): void {
  budget.work += amount;
  if (budget.work > budget.maxWork) tooLarge(`document diff exceeded its ${budget.maxWork}-operation work limit`);
}

function consumeTokens(budget: DiffBudget, amount = 1): void {
  budget.tokens += amount;
  if (budget.tokens > budget.maxTokens) tooLarge(`document diff exceeded its ${budget.maxTokens}-token limit`);
}

function countLines(text: string): number {
  if (text.length === 0) return 0;
  let count = 1;
  for (let index = 0; index < text.length; index += 1) {
    if (text[index] === '\n') count += 1;
    else if (text[index] === '\r') {
      count += 1;
      if (text[index + 1] === '\n') index += 1;
    }
  }
  return count;
}

function splitLines(text: string): LineToken[] {
  if (text.length === 0) return [];
  const lines: LineToken[] = [];
  let start = 0;
  for (let index = 0; index < text.length; index += 1) {
    const character = text[index];
    let ending: LineToken['ending'] | undefined;
    let end = index + 1;
    if (character === '\r') {
      ending = text[index + 1] === '\n' ? 'crlf' : 'cr';
      if (ending === 'crlf') {
        end += 1;
        index += 1;
      }
    } else if (character === '\n') {
      ending = 'lf';
    }
    if (ending) {
      lines.push({ text: text.slice(start, index - (ending === 'crlf' ? 1 : 0)), ending });
      start = end;
    }
  }
  if (start < text.length || lines.length === 0) {
    // A trailing separator terminates the preceding line; do not manufacture
    // a synthetic empty line. This lets matched content lines expose a
    // separator-only change even when one side has a final newline.
    lines.push({ text: text.slice(start), ending: 'none' });
  }
  return lines;
}

function lineDisplay(token: LineToken): Pick<DiffLine, 'text' | 'lineEnding'> {
  return token.ending === 'crlf'
    ? { text: token.text, lineEnding: 'CRLF' }
    : token.ending === 'cr'
      ? { text: token.text, lineEnding: 'CR' }
      : { text: token.text };
}

/**
 * Return a shortest deterministic edit script using Myers' sequence diff.
 * Ties always walk the old sequence first, which keeps repeated Markdown
 * lines attached to the same side of a hunk across browsers.
 */
function sequenceDiff<T>(oldValues: T[], newValues: T[], equal: (left: T, right: T) => boolean, budget: DiffBudget): Edit<T>[] {
  const limit = oldValues.length + newValues.length;
  const trace: Map<number, number>[] = [];
  let frontier = new Map<number, number>([[0, 0]]);
  let distance = 0;

  for (; distance <= limit; distance += 1) {
    if (distance > budget.maxEditDistance) {
      tooLarge(`document diff exceeded its ${budget.maxEditDistance}-edit distance limit`);
    }
    consumeWork(budget, frontier.size);
    trace.push(new Map(frontier));
    const next = new Map<number, number>();
    for (let diagonal = -distance; diagonal <= distance; diagonal += 2) {
      consumeWork(budget);
      let x: number;
      if (diagonal === -distance || (diagonal !== distance && (frontier.get(diagonal - 1) ?? -1) < (frontier.get(diagonal + 1) ?? -1))) {
        x = frontier.get(diagonal + 1) ?? 0;
      } else {
        x = (frontier.get(diagonal - 1) ?? 0) + 1;
      }
      let y = x - diagonal;
      while (x < oldValues.length && y < newValues.length && equal(oldValues[x], newValues[y])) {
        consumeWork(budget);
        x += 1;
        y += 1;
      }
      next.set(diagonal, x);
      if (x >= oldValues.length && y >= newValues.length) {
        consumeWork(budget, next.size);
        trace.push(next);
        return backtrack(oldValues, newValues, trace, distance);
      }
    }
    frontier = next;
  }

  // The loop always reaches the end of two finite arrays. Keeping this guard
  // makes a future change to the algorithm fail as a bounded client error.
  throw new DiffError('invalid', 'could not compute document diff');
}

function backtrack<T>(oldValues: T[], newValues: T[], trace: Map<number, number>[], distance: number): Edit<T>[] {
  const edits: Edit<T>[] = [];
  let x = oldValues.length;
  let y = newValues.length;

  for (let current = distance; current > 0; current -= 1) {
    const previous = trace[current];
    const diagonal = x - y;
    const down = diagonal === -current || (diagonal !== current && (previous.get(diagonal - 1) ?? -1) < (previous.get(diagonal + 1) ?? -1));
    const previousDiagonal = down ? diagonal + 1 : diagonal - 1;
    const previousX = previous.get(previousDiagonal) ?? 0;
    const previousY = previousX - previousDiagonal;

    while (x > previousX && y > previousY) {
      edits.push({ kind: 'equal', value: oldValues[x - 1] });
      x -= 1;
      y -= 1;
    }
    if (x === previousX) {
      edits.push({ kind: 'added', value: newValues[y - 1] });
      y -= 1;
    } else {
      edits.push({ kind: 'removed', value: oldValues[x - 1] });
      x -= 1;
    }
  }
  while (x > 0 && y > 0) {
    edits.push({ kind: 'equal', value: oldValues[x - 1] });
    x -= 1;
    y -= 1;
  }
  while (x > 0) {
    edits.push({ kind: 'removed', value: oldValues[x - 1] });
    x -= 1;
  }
  while (y > 0) {
    edits.push({ kind: 'added', value: newValues[y - 1] });
    y -= 1;
  }
  return edits.reverse();
}

function isWordCharacter(value: string): boolean {
  return /[\p{L}\p{N}_]/u.test(value);
}

function wordTokens(text: string, budget: DiffBudget): string[] {
  const segmenterConstructor = (Intl as typeof Intl & {
    Segmenter?: new (locale?: string, options?: { granularity: 'word' | 'grapheme' }) => {
      segment(input: string): Iterable<{ segment: string; isWordLike?: boolean }>;
    };
  }).Segmenter;
  if (segmenterConstructor) {
    const segmenter = new segmenterConstructor(undefined, { granularity: 'word' });
    const tokens: string[] = [];
    for (const part of segmenter.segment(text)) {
      consumeTokens(budget);
      tokens.push(part.segment);
    }
    return tokens;
  }

  // The fallback still iterates code points rather than UTF-16 code units,
  // so an emoji can never be split into an invalid surrogate pair.
  const tokens: string[] = [];
  for (const character of Array.from(text)) {
    const previous = tokens[tokens.length - 1];
    if (previous && isWordCharacter(character) && isWordCharacter(previous[previous.length - 1])) {
      tokens[tokens.length - 1] += character;
    } else {
      consumeTokens(budget);
      tokens.push(character);
    }
  }
  return tokens;
}

function wordDiff(oldText: string, newText: string, budget: DiffBudget): { old: WordChange[]; next: WordChange[] } {
  const oldTokens = wordTokens(oldText, budget);
  const newTokens = wordTokens(newText, budget);
  const edits = sequenceDiff(oldTokens, newTokens, (left, right) => left === right, budget);
  const old: WordChange[] = [];
  const next: WordChange[] = [];

  for (const edit of edits) {
    if (edit.kind === 'equal') {
      appendWord(old, 'equal', edit.value);
      appendWord(next, 'equal', edit.value);
    } else if (edit.kind === 'removed') {
      appendWord(old, 'removed', edit.value);
    } else {
      appendWord(next, 'added', edit.value);
    }
  }
  return { old, next };
}

function appendWord(output: WordChange[], kind: WordChangeKind, text: string): void {
  const previous = output[output.length - 1];
  if (previous?.kind === kind) previous.text += text;
  else output.push({ kind, text });
}

function withWordDetails(lines: DiffLine[], budget: DiffBudget): DiffLine[] {
  const result = lines.slice();
  for (let index = 0; index < result.length; index += 1) {
    if (result[index].kind === 'context') continue;
    const removed: number[] = [];
    const added: number[] = [];
    while (index < result.length && result[index].kind !== 'context') {
      if (result[index].kind === 'removed') removed.push(index);
      else added.push(index);
      index += 1;
    }
    index -= 1;
    const pairs = Math.min(removed.length, added.length);
    for (let pair = 0; pair < pairs; pair += 1) {
      const detail = wordDiff(result[removed[pair]].text, result[added[pair]].text, budget);
      result[removed[pair]] = { ...result[removed[pair]], words: detail.old };
      result[added[pair]] = { ...result[added[pair]], words: detail.next };
    }
    for (let pair = pairs; pair < removed.length; pair += 1) {
      result[removed[pair]] = { ...result[removed[pair]], words: [{ kind: 'removed', text: result[removed[pair]].text }] };
    }
    for (let pair = pairs; pair < added.length; pair += 1) {
      result[added[pair]] = { ...result[added[pair]], words: [{ kind: 'added', text: result[added[pair]].text }] };
    }
  }
  return result;
}

function makeLineEdits(oldLines: LineToken[], newLines: LineToken[], budget: DiffBudget): DiffLine[] {
  // Match by content, then retain every matched separator difference as an
  // explicit remove/add pair. Trailing separators are represented on the
  // preceding line, rather than by a synthetic empty token, so additions or
  // deletions elsewhere cannot hide an exact line-ending change.
  const edits = sequenceDiff(oldLines, newLines, (left, right) => left.text === right.text, budget);
  const lines: DiffLine[] = [];
  let oldLine = 0;
  let newLine = 0;
  for (const edit of edits) {
    if (edit.kind === 'equal') {
      const oldToken = oldLines[oldLine];
      const newToken = newLines[newLine];
      oldLine += 1;
      newLine += 1;
      if (oldToken.ending !== newToken.ending) {
        lines.push({ kind: 'removed', ...lineDisplay(oldToken), oldLine, newLine: null });
        lines.push({ kind: 'added', ...lineDisplay(newToken), oldLine: null, newLine });
      } else {
        lines.push({ kind: 'context', ...lineDisplay(newToken), oldLine, newLine });
      }
    } else if (edit.kind === 'removed') {
      oldLine += 1;
      lines.push({ kind: 'removed', ...lineDisplay(edit.value), oldLine, newLine: null });
    } else {
      newLine += 1;
      lines.push({ kind: 'added', ...lineDisplay(edit.value), oldLine: null, newLine });
    }
  }
  return withWordDetails(lines, budget);
}

function makeHunks(lines: DiffLine[], context: number): DiffHunk[] {
  const changed = lines.flatMap((line, index) => line.kind === 'context' ? [] : [index]);
  if (changed.length === 0) return [];
  const ranges: Array<[number, number]> = [];
  for (const index of changed) {
    const start = Math.max(0, index - context);
    const end = Math.min(lines.length, index + context + 1);
    const previous = ranges[ranges.length - 1];
    if (previous && start <= previous[1]) previous[1] = end;
    else ranges.push([start, end]);
  }

  return ranges.map(([start, end]) => {
    const hunkLines = lines.slice(start, end);
    const first = hunkLines[0];
    const oldStart = first.oldLine ?? first.newLine ?? 1;
    const newStart = first.newLine ?? first.oldLine ?? 1;
    return {
      oldStart,
      oldCount: hunkLines.filter((line) => line.oldLine !== null).length,
      newStart,
      newCount: hunkLines.filter((line) => line.newLine !== null).length,
      lines: hunkLines,
    };
  });
}

export function diffMarkdown(oldText: string, newText: string, options: DiffOptions = {}): MarkdownDiff {
  if (typeof oldText !== 'string' || typeof newText !== 'string') {
    throw new DiffError('invalid', 'document diff inputs must be strings');
  }
  const maxBytes = options.maxBytes ?? maxDiffBytes;
  const maxLines = options.maxLines ?? maxDiffLines;
  const maxTokens = options.maxTokens ?? maxDiffTokens;
  const maxWork = options.maxWork ?? maxDiffWork;
  const maxEditDistance = options.maxEditDistance ?? maxDiffEditDistance;
  const requestedContext = options.context ?? defaultDiffContext;
  if (![maxBytes, maxLines, maxTokens, maxWork, maxEditDistance, requestedContext].every((value) => Number.isFinite(value) && value >= 0)) {
    throw new DiffError('invalid', 'document diff bounds must be finite and non-negative');
  }
  const bytes = new TextEncoder().encode(oldText).byteLength + new TextEncoder().encode(newText).byteLength;
  if (bytes > maxBytes) throw new DiffError('too-large', `document diff is limited to ${maxBytes} bytes`);
  const oldLineCount = countLines(oldText);
  const newLineCount = countLines(newText);
  if (oldLineCount + newLineCount > maxLines) {
    throw new DiffError('too-large', `document diff is limited to ${maxLines} lines`);
  }
  if (oldText === newText) return { changed: false, hunks: [] };
  const budget: DiffBudget = {
    maxTokens: Math.floor(maxTokens),
    maxWork: Math.floor(maxWork),
    maxEditDistance: Math.floor(maxEditDistance),
    tokens: 0,
    work: 0,
  };
  const oldLines = splitLines(oldText);
  const newLines = splitLines(newText);
  const context = Math.floor(requestedContext);
  const lines = makeLineEdits(oldLines, newLines, budget);
  return { changed: lines.some((line) => line.kind !== 'context'), hunks: makeHunks(lines, context) };
}
