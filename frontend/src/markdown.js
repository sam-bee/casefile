import DOMPurify from 'dompurify';
import MarkdownIt from 'markdown-it';

const parser = new MarkdownIt({
  html: false,
  linkify: false,
  typographer: false,
});

const sanitizeOptions = {
  ALLOW_ARIA_ATTR: false,
  ALLOW_DATA_ATTR: false,
  ALLOWED_TAGS: [
    'a', 'blockquote', 'br', 'code', 'del', 'em', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
    'hr', 'li', 'ol', 'p', 'pre', 'strong', 'table', 'tbody', 'td', 'th', 'thead', 'tr', 'ul',
  ],
  ALLOWED_ATTR: ['href', 'start', 'title'],
};

export function renderMarkdown(element, source) {
  const markup = parser.render(String(source ?? ''));
  element.innerHTML = DOMPurify.sanitize(markup, sanitizeOptions);
}
