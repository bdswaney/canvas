import { memo } from 'react';
import { Text, Typography } from '@mantine/core';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeSanitize from 'rehype-sanitize';
import { markdownComponents } from './MarkdownImage';
import './preview.css';

// remark-gfm keeps the preview reading the same dialect the editor
// highlights: tables, task lists, strikethrough, and bare-URL autolinks.
const remarkPlugins = [remarkGfm];

// The content is written by one person and rendered in everyone else's
// browser, so it is treated as untrusted. Two things stand between it and the
// DOM: raw HTML in the source is never parsed (rehype-raw is deliberately not
// installed), and rehype-sanitize drops anything outside its allowed schema.
// react-markdown also rejects javascript: and other unsafe URLs by default.
// MarkdownImage adds the stricter preview policy for cross-origin HTTP(S)
// images without changing the raw-HTML or sanitization boundaries.
const rehypePlugins = [rehypeSanitize];

/**
 * Preview renders the shared Markdown. It is memoized on the text because
 * parsing runs on every render, and the snapshot it receives is debounced.
 */
export const Preview = memo(function Preview({ text }: { text: string }) {
  if (text.trim() === '') {
    return (
      <Text c="dimmed" size="sm">
        The preview of the shared document appears here.
      </Text>
    );
  }

  return (
    <Typography className="preview">
      <Markdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={markdownComponents}
      >
        {text}
      </Markdown>
    </Typography>
  );
});
