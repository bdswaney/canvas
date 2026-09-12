import { createElement, type ImgHTMLAttributes } from 'react';
import type { Components } from 'react-markdown';

function currentPageOrigin(): string | undefined {
  if (typeof window === 'undefined' || window.location.origin === 'null') {
    return undefined;
  }
  return window.location.origin;
}

/**
 * Returns whether an image source points at an HTTP(S) origin other than the
 * page rendering the preview. URL parsing also normalizes backslashes, which
 * browsers accept in network-path references.
 */
export function isBlockedRemoteImageSource(source: string, pageOrigin = currentPageOrigin()): boolean {
  if (!source) {
    return false;
  }

  const fallbackOrigin = 'http://localhost';

  try {
    const parsed = new URL(source, pageOrigin ?? fallbackOrigin);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return false;
    }

    if (pageOrigin !== undefined) {
      return parsed.origin !== pageOrigin;
    }

    // Server-side rendering has no page origin to compare against. Keep
    // relative URLs usable while conservatively blocking absolute and
    // network-path HTTP(S) references. Parsing against a second origin
    // distinguishes those references without inspecting slash prefixes.
    const alternate = new URL(source, 'https://nply.invalid');
    return parsed.origin !== fallbackOrigin || alternate.origin !== 'https://nply.invalid';
  } catch {
    // A malformed absolute HTTP(S) source must not reach an <img> element.
    return true;
  }
}

type MarkdownImageProps = Pick<ImgHTMLAttributes<HTMLImageElement>, 'src' | 'alt' | 'title'>;

/**
 * Markdown images are allowed to load only from this page or a relative URL.
 * A blocked image becomes text instead of an <img>, so merely opening a
 * document cannot send a request to an author-controlled remote host.
 */
export function MarkdownImage({ src, alt, title }: MarkdownImageProps) {
  if (!src) {
    return createElement(
      'span',
      { className: 'preview-image-blocked', role: 'img', 'aria-label': 'Image unavailable' },
      'Image unavailable',
    );
  }

  if (isBlockedRemoteImageSource(src)) {
    const message = alt ? `External image blocked for privacy: ${alt}` : 'External image blocked for privacy';
    return createElement(
      'span',
      { className: 'preview-image-blocked', role: 'img', 'aria-label': message },
      message,
    );
  }

  return createElement('img', { src, alt: alt ?? '', ...(title ? { title } : {}) });
}

export const markdownComponents = {
  img: MarkdownImage,
} satisfies Components;
