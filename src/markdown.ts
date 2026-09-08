import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { markdown, markdownLanguage } from '@codemirror/lang-markdown';
import { tags } from '@lezer/highlight';
import type { Extension } from '@codemirror/state';

// Markdown is read as much as it is written, so the styling leans on weight,
// size, and rhythm rather than color: headings look like headings, and the
// punctuation that marks them up recedes. Only the few rules that carry color
// differ between schemes; the shape of the document is the same in both.
const structure = [
  { tag: tags.heading1, fontSize: '1.6em', fontWeight: '700', lineHeight: 1.5 },
  { tag: tags.heading2, fontSize: '1.35em', fontWeight: '700', lineHeight: 1.5 },
  { tag: tags.heading3, fontSize: '1.15em', fontWeight: '700' },
  { tag: [tags.heading4, tags.heading5, tags.heading6], fontWeight: '700' },
  { tag: tags.strong, fontWeight: '700' },
  { tag: tags.emphasis, fontStyle: 'italic' },
  { tag: tags.strikethrough, textDecoration: 'line-through' },
  { tag: tags.link, textDecoration: 'underline' },
  { tag: tags.quote, fontStyle: 'italic' },
];

// The #, *, -, and ` characters stay visible but stop competing with the text
// they mark up. List and heading bodies keep the normal text color.
// Each style declares the theme it belongs to, so exactly one of them
// matches: an unscoped style would apply to both and win on precedence.
const lightHighlight = HighlightStyle.define(
  [
    ...structure,
    { tag: [tags.link, tags.url], color: '#1c7ed6' },
    { tag: tags.quote, color: '#495057', fontStyle: 'italic' },
    { tag: tags.monospace, color: '#c2255c' },
    { tag: tags.processingInstruction, color: '#adb5bd' },
    { tag: [tags.comment, tags.meta], color: '#868e96' },
  ],
  { themeType: 'light' },
);

// Dark backgrounds need lighter, less saturated accents to read at the same
// weight as the surrounding text.
const darkHighlight = HighlightStyle.define(
  [
    ...structure,
    { tag: [tags.link, tags.url], color: '#74c0fc' },
    { tag: tags.quote, color: '#adb5bd', fontStyle: 'italic' },
    { tag: tags.monospace, color: '#faa2c1' },
    { tag: tags.processingInstruction, color: '#5c636a' },
    { tag: [tags.comment, tags.meta], color: '#868e96' },
  ],
  { themeType: 'dark' },
);

/**
 * markdownSupport adds Markdown parsing, styling, and the editing commands
 * that come with it: Enter continues a list or blockquote, and Backspace at
 * the start of an item removes the marker. Both highlight styles are
 * registered; CodeMirror applies the one matching the active theme.
 */
export function markdownSupport(): Extension {
  return [
    markdown({ base: markdownLanguage, addKeymap: true }),
    syntaxHighlighting(lightHighlight),
    syntaxHighlighting(darkHighlight),
  ];
}
