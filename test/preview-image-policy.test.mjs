import assert from 'node:assert/strict';
import test from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import Markdown from 'react-markdown';
import rehypeSanitize from 'rehype-sanitize';
import remarkGfm from 'remark-gfm';
import { isBlockedRemoteImageSource, MarkdownImage } from '../src/editor/MarkdownImage.ts';
import { relayURL } from '../src/collab/sync.ts';

function renderMarkdown(markdown) {
  return renderToStaticMarkup(
    createElement(
      Markdown,
      {
        remarkPlugins: [remarkGfm],
        rehypePlugins: [rehypeSanitize],
        components: { img: MarkdownImage },
      },
      markdown,
    ),
  );
}

test('remote Markdown images render a privacy placeholder, not a loading img', () => {
  const html = renderMarkdown('![Tracking pixel](https://tracker.example/pixel.png)');

  assert.doesNotMatch(html, /<img\b/);
  assert.ok(!html.includes('tracker.example'));
  assert.match(html, /External image blocked for privacy: Tracking pixel/);
});

test('relative Markdown images remain usable', () => {
  const html = renderMarkdown('![Diagram](images/diagram.png)');

  assert.match(html, /<img\s+src="images\/diagram\.png"/);
  assert.match(html, /alt="Diagram"/);
});

test('same-origin absolute images are distinct from remote images', () => {
  assert.equal(isBlockedRemoteImageSource('https://app.example/image.png', 'https://app.example'), false);
  assert.equal(isBlockedRemoteImageSource('/image.png', 'https://app.example'), false);
  assert.equal(isBlockedRemoteImageSource('https://tracker.example/image.png', 'https://app.example'), true);
  assert.equal(isBlockedRemoteImageSource('//tracker.example/image.png', 'https://app.example'), true);
});

test('backslash and mixed network paths do not emit img elements', () => {
  const sources = [String.raw`\\tracker.example\pixel.png`, String.raw`/\tracker.example\pixel.png`];

  for (const source of sources) {
    assert.equal(isBlockedRemoteImageSource(source, 'https://app.example'), true, source);
    const html = renderToStaticMarkup(createElement(MarkdownImage, { src: source, alt: 'Tracking pixel' }));
    assert.doesNotMatch(html, /<img\b/, source);
  }
});

test('WebSocket relay follows the page scheme for CSP-compatible connections', () => {
  assert.equal(relayURL({ protocol: 'http:', host: 'localhost:8080' }), 'ws://localhost:8080/api/sync/doc');
  assert.equal(relayURL({ protocol: 'https:', host: 'nply.example' }), 'wss://nply.example/api/sync/doc');
});

test('raw HTML remains excluded from the Markdown renderer', () => {
  const html = renderMarkdown('<img src="https://tracker.example/raw.png" alt="raw">');

  assert.doesNotMatch(html, /<img\b/);
  assert.ok(!html.includes('tracker.example'));
});
