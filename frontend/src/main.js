import './style.css';
import { mountGraph } from './graph.js';
import { renderMarkdown } from './markdown.js';

const PAGE_SIZE = 20;
const REFRESH_INTERVAL = 15_000;
const SUMMARY_LIMIT = 1_400;
const state = {
  query: '', kind: '', status: '', reviewScore: null, reviewOperator: 'eq', tags: [], offset: 0,
  total: 0, casenotes: [], selectedId: null, selected: null,
  searchRequest: 0, detailRequest: 0, tagRequest: 0, tagCloudRequest: 0, editing: false,
  searchSignature: null, selectedSignature: null,
  projects: [], projectId: null, projectSlug: '', view: 'list',
  graphRequest: 0, graph: null, graphSignature: null, graphTransform: null,
  tagCloudSignature: null, detailMutations: 0, refreshing: false,
};

const dom = Object.fromEntries([
  'actorInput', 'themeToggle', 'newItemButton', 'emptyNewButton', 'searchInput', 'kindFilter',
  'statusFilter', 'reviewScoreOperator', 'reviewScoreFilter', 'tagFilterInput', 'addTagFilterButton', 'tagFilterChips',
  'searchButton', 'clearFiltersButton', 'tagCloud', 'resultCount', 'searchActivity', 'resultList',
  'previousPageButton', 'nextPageButton', 'pageIndicator', 'detailEmpty',
  'detailView', 'itemDialog', 'itemForm', 'dialogTitle', 'closeDialogButton',
  'cancelDialogButton', 'saveItemButton', 'itemTitle', 'itemKind', 'itemStatus',
  'itemSummary', 'itemSummaryCount', 'itemBody', 'itemRevision', 'itemTags', 'itemReviewScore', 'itemFields', 'knownTags', 'toast',
  'projectSelect', 'newProjectButton', 'projectSlug', 'projectDialog',
  'projectForm', 'projectName', 'projectSlugInput', 'closeProjectDialogButton',
  'cancelProjectButton', 'createProjectButton', 'dialogProject',
  'resultsPane', 'detailPane', 'graphPane', 'graphViewButton', 'listViewButton',
  'graphProjectName', 'graphProjectSlug', 'graphActivity', 'graphTruncation',
  'graphCanvas', 'graphCount', 'graphZoomIn', 'graphZoomOut', 'graphReset',
].map((id) => [id, document.getElementById(id)]));

let toastTimer;
const THEME_KEY = 'nightwatch-casefile-theme';

function setTheme(theme, save = false) {
  const dark = theme === 'dark';
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  dom.themeToggle.textContent = dark ? '☀' : '☾';
  dom.themeToggle.setAttribute('aria-label', dark ? 'Switch to light mode' : 'Switch to dark mode');
  dom.themeToggle.title = dark ? 'Switch to light mode' : 'Switch to dark mode';
  dom.themeToggle.setAttribute('aria-pressed', String(dark));
  document.querySelector('meta[name="theme-color"]').content = dark ? '#101b26' : '#10243a';
  if (save) localStorage.setItem(THEME_KEY, dark ? 'dark' : 'light');
}

setTheme(localStorage.getItem(THEME_KEY) === 'light' ? 'light' : 'dark');

function el(tag, className = '', text = null) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== null) element.textContent = String(text);
  return element;
}

function humanize(value) {
  return String(value || 'unspecified').replaceAll('-', ' ');
}

function compactDate(value) {
  if (!value) return 'Unknown date';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', year: 'numeric' }).format(date);
}

function showToast(message, isError = false) {
  clearTimeout(toastTimer);
  dom.toast.textContent = message;
  dom.toast.classList.toggle('toast-error', isError);
  dom.toast.classList.add('toast-visible');
  toastTimer = setTimeout(() => dom.toast.classList.remove('toast-visible'), 5000);
}

async function post(path, payload = {}) {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(data.error || `Request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return data;
}

function actor() {
  const name = dom.actorInput.value.trim();
  if (/^[^()]+ \([A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\)$/.test(name)) {
    localStorage.setItem('nightwatch-casefile-actor', name);
    return name;
  }
  dom.actorInput.focus();
  showToast('Use an actor such as research-agent (deepseek/deepseek-flash).', true);
  return null;
}

function parseTags(value) {
  return [...new Set(value.split(',').map((tag) => tag.trim().toLowerCase()).filter(Boolean))];
}

function summaryCodePointCount(value) {
  return Array.from(String(value).trim()).length;
}

function updateSummaryCount() {
  const count = summaryCodePointCount(dom.itemSummary.value);
  const overLimit = count > SUMMARY_LIMIT;
  dom.itemSummaryCount.textContent = `${count.toLocaleString()} / ${SUMMARY_LIMIT.toLocaleString()} characters${overLimit ? ' — over limit' : ''}`;
  dom.itemSummaryCount.classList.toggle('is-invalid', overLimit);
  dom.itemSummary.setAttribute('aria-invalid', String(overLimit));
  return count;
}

function reviewScore(casenote) {
  const score = casenote?.fields?.human_review_score;
  return Number.isInteger(score) && score >= 0 && score <= 5 ? score : 0;
}

function hashLocation() {
  const match = location.hash.match(/^#project-([^/]+)(?:\/casenote-(\d+))?$/);
  if (match) {
    try { return { slug: decodeURIComponent(match[1]), itemId: match[2] ? Number(match[2]) : null }; }
    catch { return {}; }
  }
  const legacy = location.hash.match(/^#casenote-(\d+)$/);
  return legacy ? { itemId: Number(legacy[1]) } : {};
}

function projectHash(itemId = null) {
  const base = `#project-${encodeURIComponent(state.projectSlug)}`;
  return itemId ? `${base}/casenote-${itemId}` : base;
}

function currentProject() {
  return state.projects.find((project) => Number(project.id) === state.projectId) || null;
}

function renderProjects() {
  dom.projectSelect.replaceChildren(...state.projects.map((project) => {
    const option = el('option', '', project.name);
    option.value = String(project.id);
    return option;
  }));
  dom.projectSelect.value = String(state.projectId);
  const project = currentProject();
  dom.projectSlug.textContent = project ? `Namespace: ${project.slug}` : '';
  dom.graphProjectName.textContent = project?.name || 'Project';
  dom.graphProjectSlug.textContent = project?.slug || '';
}

function resetFilters() {
  dom.searchInput.value = '';
  dom.kindFilter.value = '';
  dom.statusFilter.value = '';
  dom.reviewScoreOperator.value = 'eq';
  dom.reviewScoreFilter.value = '';
  dom.tagFilterInput.value = '';
  state.query = ''; state.kind = ''; state.status = ''; state.reviewScore = null; state.reviewOperator = 'eq';
  state.tags = []; state.offset = 0;
  renderTagFilters();
}

async function switchProject(project, { initial = false, itemId = null } = {}) {
  if (!project) return;
  const changed = Number(project.id) !== state.projectId;
  if (changed) {
    state.projectId = Number(project.id);
    state.projectSlug = project.slug;
    localStorage.setItem('nightwatch-casefile-project', project.slug);
    state.searchRequest++;
    state.detailRequest++;
    state.tagRequest++;
    state.tagCloudRequest++;
    state.graphRequest++;
    state.graph?.destroy(); state.graph = null; state.graphSignature = null;
    state.graphTransform = null;
    state.tagCloudSignature = null;
    state.searchSignature = null;
    clearDetail();
    resetFilters();
    renderProjects();
    history.replaceState(null, '', projectHash());
    refreshTagSuggestions('');
    refreshTagCloud();
    await refreshSearch();
    if (state.view === 'graph') loadGraph();
  } else if (initial) {
    renderProjects();
    refreshTagCloud();
    await refreshSearch();
  }
  if (itemId) selectItem(itemId);
}

async function loadProjects() {
  const result = await post('/api/projects/list');
  state.projects = Array.isArray(result.projects) ? result.projects : [];
  if (!state.projects.length) throw new Error('No research project is available.');
  const hash = hashLocation();
  if (hash.itemId && !hash.slug) {
    try {
      const detail = await post('/api/casenotes/get', { id: hash.itemId });
      hash.slug = detail.casenote?.project;
    } catch { hash.itemId = null; }
  }
  const stored = localStorage.getItem('nightwatch-casefile-project');
  const project = state.projects.find((entry) => entry.slug === hash.slug)
    || state.projects.find((entry) => entry.slug === stored)
    || state.projects[0];
  await switchProject(project, { initial: true, itemId: hash.itemId });
  if (!hash.itemId && state.casenotes.length) selectItem(state.casenotes[0].id);
}

function tagPill(tag, onClick, removable = false) {
  const button = el('button', `tag-pill${removable ? ' tag-removable' : ''}`, tag);
  button.type = 'button';
  button.title = removable ? `Remove ${tag} filter` : `Filter by ${tag}`;
  button.addEventListener('click', onClick);
  if (removable) button.append(el('span', 'tag-x', '×'));
  return button;
}

function addTagFilter(value) {
  let changed = false;
  for (const tag of parseTags(value)) {
    if (!state.tags.includes(tag)) {
      state.tags.push(tag);
      changed = true;
    }
  }
  dom.tagFilterInput.value = '';
  if (changed) {
    state.offset = 0;
    renderTagFilters();
    refreshSearch();
  }
}

async function refreshTagCloud({ quiet = false } = {}) {
  if (!state.projectId) return;
  const projectId = state.projectId;
  const request = ++state.tagCloudRequest;
  if (!quiet && state.tagCloudSignature === null) dom.tagCloud.textContent = 'Loading tags…';
  try {
    const result = await post('/api/tags/cloud', { project_id: projectId });
    if (projectId !== state.projectId || request !== state.tagCloudRequest) return;
    const tags = Array.isArray(result.tags) ? result.tags : [];
    const signature = JSON.stringify(tags);
    if (signature === state.tagCloudSignature) return;
    const scrollTop = dom.tagCloud.scrollTop;
    const focusedTag = dom.tagCloud.contains(document.activeElement) ? document.activeElement.dataset.tag : null;
    state.tagCloudSignature = signature;
    if (!tags.length) { dom.tagCloud.textContent = 'No tags yet.'; dom.tagCloud.scrollTop = scrollTop; return; }
    const max = Math.max(...tags.map((item) => Number(item.count) || 0), 1);
    dom.tagCloud.replaceChildren(...tags.map((item) => {
      const count = Number(item.count) || 0;
      const casenoteLabel = `${count} casenote${count === 1 ? '' : 's'}`;
      const button = el('button', 'cloud-tag');
      button.type = 'button';
      button.dataset.tag = item.tag;
      button.style.fontSize = `${10 + Math.round(5 * Math.sqrt(count / max))}px`;
      button.textContent = `${item.tag} [${count}]`;
      button.title = `${casenoteLabel} · filter by ${item.tag}`;
      button.setAttribute('aria-label', `${item.tag}, ${casenoteLabel}; filter by tag`);
      button.addEventListener('click', () => addTagFilter(item.tag));
      return button;
    }));
    dom.tagCloud.scrollTop = scrollTop;
    if (focusedTag) [...dom.tagCloud.querySelectorAll('[data-tag]')]
      .find((button) => button.dataset.tag === focusedTag)?.focus({ preventScroll: true });
  } catch {
    if (!quiet) {
      state.tagCloudSignature = null;
      dom.tagCloud.textContent = 'Tag cloud unavailable.';
    }
  }
}

function renderTagFilters() {
  dom.tagFilterChips.replaceChildren(...state.tags.map((tag) => tagPill(tag, () => {
    state.tags = state.tags.filter((value) => value !== tag);
    state.offset = 0;
    renderTagFilters();
    refreshSearch();
  }, true)));
}

function renderSearchResults() {
  dom.resultCount.textContent = String(state.total);
  dom.resultList.replaceChildren();
  if (state.casenotes.length === 0) {
    const empty = el('div', 'empty-results');
    empty.append(el('div', 'empty-results-icon', '⌕'));
    empty.append(el('h3', '', 'No casenotes found'));
    empty.append(el('p', '', state.query || state.kind || state.status || state.reviewScore !== null || state.tags.length
      ? 'Try another search or clear a filter.'
      : `Create the first research casenote in ${currentProject()?.name || 'this project'}.`));
    dom.resultList.append(empty);
  }
  for (const casenote of state.casenotes) {
    const card = el('button', `result-card${Number(casenote.id) === state.selectedId ? ' is-selected' : ''}`);
    card.type = 'button';
    card.dataset.casenoteId = String(casenote.id);
    card.setAttribute('aria-label', `Open casenote ${casenote.id}: ${casenote.title}`);
    const top = el('div', 'result-card-top');
    const identity = el('span', 'result-identity');
    identity.append(el('span', 'casenote-id', `#${casenote.id}`));
    identity.append(el('span', 'casenote-project', casenote.project || state.projectSlug));
    top.append(identity);
    top.append(el('span', 'kind-label', humanize(casenote.kind)));
    card.append(top);
    card.append(el('h3', '', casenote.title || 'Untitled casenote'));
    const snippet = casenote.summary || casenote.body || '';
    const snippetCharacters = Array.from(snippet);
    if (snippet) card.append(el('p', 'result-snippet', snippetCharacters.length > 170 ? `${snippetCharacters.slice(0, 170).join('').trimEnd()}…` : snippet));
    const bottom = el('div', 'result-card-bottom');
    const meta = el('span', 'result-meta');
    meta.append(el('span', 'status-dot'));
    meta.append(document.createTextNode(humanize(casenote.status)));
    bottom.append(meta);
    if (reviewScore(casenote) > 0) bottom.append(el('span', 'review-score', `Review ${reviewScore(casenote)}/5`));
    bottom.append(el('span', 'result-date', compactDate(casenote.updated_at || casenote.created_at)));
    card.append(bottom);
    if (Array.isArray(casenote.tags) && casenote.tags.length) {
      const tags = el('div', 'result-tags');
      casenote.tags.slice(0, 3).forEach((tag) => tags.append(el('span', 'mini-tag', tag)));
      if (casenote.tags.length > 3) tags.append(el('span', 'mini-tag-more', `+${casenote.tags.length - 3}`));
      card.append(tags);
    }
    card.addEventListener('click', () => selectItem(casenote.id));
    dom.resultList.append(card);
  }
  const page = Math.floor(state.offset / PAGE_SIZE) + 1;
  const maxPage = Math.max(1, Math.ceil(state.total / PAGE_SIZE));
  dom.pageIndicator.textContent = `${page} / ${maxPage}`;
  dom.previousPageButton.disabled = state.offset === 0;
  dom.nextPageButton.disabled = state.offset + PAGE_SIZE >= state.total;
}

async function refreshSearch({ silent = false } = {}) {
  if (!state.projectId) return;
  const request = ++state.searchRequest;
  if (!silent) dom.searchActivity.textContent = 'Searching…';
  try {
    const reviewFilter = state.reviewScore === null ? {} : {
      review_score: state.reviewScore, review_operator: state.reviewOperator,
    };
    const result = await post('/api/casenotes/search', {
      query: state.query, kind: state.kind, status: state.status,
      tags: state.tags, limit: PAGE_SIZE, offset: state.offset,
      project_id: state.projectId, ...reviewFilter,
    });
    if (request !== state.searchRequest) return;
    const casenotes = Array.isArray(result.casenotes) ? result.casenotes : [];
    const total = Number(result.total) || 0;
    const signature = JSON.stringify([state.query, state.kind, state.status, state.reviewScore, state.reviewOperator, state.tags, state.offset, total, casenotes]);
    const changed = signature !== state.searchSignature;
    state.casenotes = casenotes;
    state.total = total;
    state.searchSignature = signature;
    if (changed) {
      const scrollTop = dom.resultList.scrollTop;
      const active = dom.resultList.contains(document.activeElement) ? document.activeElement : null;
      const focusedId = active?.dataset.casenoteId;
      renderSearchResults();
      dom.resultList.scrollTop = scrollTop;
      if (focusedId) [...dom.resultList.querySelectorAll('[data-casenote-id]')]
        .find((card) => card.dataset.casenoteId === focusedId)?.focus({ preventScroll: true });
    }
    if (!silent) dom.searchActivity.textContent = '';
  } catch (error) {
    if (request !== state.searchRequest) return;
    if (!silent) {
      dom.searchActivity.textContent = 'Search unavailable';
      showToast(error.message, true);
    }
  }
}

function selectItem(id) {
  const numericId = Number(id);
  if (!Number.isSafeInteger(numericId) || numericId <= 0) return;
  const changedSelection = numericId !== state.selectedId;
  state.selectedId = numericId;
  if (changedSelection) state.selectedSignature = null;
  history.replaceState(null, '', projectHash(numericId));
  renderSearchResults();
  loadDetail(numericId, { silent: !changedSelection });
}

function clearDetail() {
  state.detailRequest++;
  state.selected = null;
  state.selectedId = null;
  state.selectedSignature = null;
  dom.detailView.hidden = true;
  dom.detailEmpty.hidden = false;
  dom.detailView.replaceChildren();
}

function detailDraftSnapshot(root) {
  const values = new Map();
  root.querySelectorAll('[data-detail-draft]').forEach((field) => {
    values.set(field.dataset.detailDraft, {
      value: field.value,
      selectionStart: field.selectionStart,
      selectionEnd: field.selectionEnd,
      selectionDirection: field.selectionDirection,
    });
  });
  const active = document.activeElement;
  return {
    values,
    focusKey: root.contains(active) ? active.dataset.detailDraft || null : null,
    scrollTop: root.scrollTop,
  };
}

function restoreDetailDraft(root, snapshot) {
  if (!snapshot) return;
  root.querySelectorAll('[data-detail-draft]').forEach((field) => {
    const saved = snapshot.values.get(field.dataset.detailDraft);
    if (!saved) return;
    field.value = saved.value;
    if (saved.selectionStart !== null && saved.selectionStart !== undefined) {
      try { field.setSelectionRange(saved.selectionStart, saved.selectionEnd, saved.selectionDirection); }
      catch { /* Number inputs do not support text selection. */ }
    }
  });
  root.scrollTop = snapshot.scrollTop;
  if (snapshot.focusKey) {
    const active = [...root.querySelectorAll('[data-detail-draft]')]
      .find((field) => field.dataset.detailDraft === snapshot.focusKey);
    active?.focus({ preventScroll: true });
  }
}

async function loadDetail(id, { silent = false } = {}) {
  const request = ++state.detailRequest;
  if (!silent) {
    dom.detailEmpty.hidden = true;
    dom.detailView.hidden = false;
    dom.detailView.replaceChildren(el('div', 'detail-loading', 'Loading casenote…'));
  }
  try {
    const data = await post('/api/casenotes/get', { id, project_id: state.projectId });
    if (request !== state.detailRequest) return;
    if (Number(data.casenote?.project_id) !== state.projectId) {
      const mismatch = new Error('Casenote is outside the selected project.');
      mismatch.status = 404;
      throw mismatch;
    }
    if (silent && (
      state.detailMutations > 0 || dom.itemDialog.open ||
      Number(id) !== state.selectedId || Number(state.projectId) !== Number(data.casenote?.project_id)
    )) return;
    const signature = JSON.stringify(data);
    if (silent && signature === state.selectedSignature) return;
    const snapshot = silent ? detailDraftSnapshot(dom.detailView) : null;
    state.selected = data;
    state.selectedSignature = signature;
    renderDetail(data, { preserveDraft: silent, snapshot });
  } catch (error) {
    if (request !== state.detailRequest) return;
    if (error.status === 404) {
      const fallback = state.casenotes.find((casenote) => Number(casenote.id) !== Number(id));
      if (fallback) selectItem(fallback.id);
      else {
        clearDetail();
        history.replaceState(null, '', projectHash());
        renderSearchResults();
      }
      return;
    }
    if (!silent) {
      dom.detailView.replaceChildren(el('div', 'detail-error', error.message));
      showToast(error.message, true);
    }
  }
}

function renderSectionHeading(kicker, title, count = null) {
  const heading = el('div', 'section-heading');
  const group = el('div');
  group.append(el('p', 'eyebrow', kicker));
  const h = el('h3', '', title);
  if (count !== null) h.append(el('span', 'section-count', String(count)));
  group.append(h);
  heading.append(group);
  return heading;
}

function renderDetail(data, { preserveDraft = false, snapshot = null } = {}) {
  const { casenote } = data;
  const casenote_comments = Array.isArray(data.comments) ? data.comments : [];
  const casenote_links = Array.isArray(data.links) ? data.links : [];
  const activity = Array.isArray(data.activity) ? data.activity : [];
  const root = dom.detailView;
  root.replaceChildren();

  const hero = el('section', 'detail-hero');
  const top = el('div', 'detail-topline');
  top.append(el('span', 'casenote-id detail-id', `RECORD #${casenote.id} / ${casenote.project || state.projectSlug}`));
  const edit = el('button', 'button button-outline button-small', 'Edit casenote');
  edit.type = 'button';
  edit.addEventListener('click', () => openEditor(true));
  top.append(edit);
  hero.append(top);
  hero.append(el('h2', '', casenote.title || 'Untitled casenote'));
  if (typeof casenote.summary === 'string' && casenote.summary.trim()) {
    const summary = el('section', 'detail-summary');
    summary.append(el('h3', 'detail-summary-label', 'Summary'));
    summary.append(el('p', 'detail-summary-text', casenote.summary.trim()));
    hero.append(summary);
  }
  const badges = el('div', 'detail-badges');
  badges.append(el('span', 'detail-badge', humanize(casenote.kind)));
  badges.append(el('span', 'detail-badge detail-badge-state', humanize(casenote.status)));
  badges.append(el('span', 'detail-badge detail-badge-review', `Human review ${reviewScore(casenote)}/5`));
  hero.append(badges);
  const metadata = el('div', 'detail-metadata');
  metadata.append(el('span', '', `By ${casenote.author || 'unknown'}`));
  metadata.append(el('span', 'metadata-separator', '·'));
  metadata.append(el('span', '', `Updated ${compactDate(casenote.updated_at || casenote.created_at)}`));
  hero.append(metadata);
  root.append(hero);

  const body = el('section', 'detail-section content-section');
  body.append(renderSectionHeading('Record', 'What we know'));
  const bodyContent = el('div', 'casenote-body markdown-body');
  renderMarkdown(bodyContent, casenote.body || 'No description yet.');
  body.append(bodyContent);
  if (casenote.source_revision) {
    const revision = el('div', 'source-revision');
    revision.append(el('span', 'source-label', 'Source revision'));
    revision.append(el('code', '', casenote.source_revision));
    body.append(revision);
  }
  if (Array.isArray(casenote.tags) && casenote.tags.length) {
    const tags = el('div', 'detail-tags');
    casenote.tags.forEach((tag) => tags.append(tagPill(tag, () => addTagFilter(tag))));
    body.append(tags);
  }
  const fields = casenote.fields && typeof casenote.fields === 'object' && !Array.isArray(casenote.fields)
    ? casenote.fields : {};
  if (Object.keys(fields).length) {
    body.append(el('h4', 'optional-fields-heading', 'Optional fields'));
    const list = el('dl', 'optional-fields');
    for (const key of Object.keys(fields).sort()) {
      const value = fields[key];
      list.append(el('dt', '', key));
      list.append(el('dd', '', value !== null && typeof value === 'object' ? JSON.stringify(value) : String(value)));
    }
    body.append(list);
  }
  root.append(body);

  const linkSection = el('section', 'detail-section');
  linkSection.append(renderSectionHeading('Connections', 'Linked casenotes', casenote_links.length));
  const linkList = el('div', 'link-list');
  if (!casenote_links.length) linkList.append(el('p', 'empty-section', 'No connections yet. Link supporting, conflicting, or related work.'));
  for (const link of casenote_links) {
    const outgoing = Number(link.from_casenote_id) === Number(casenote.id);
    const relatedId = outgoing ? link.to_casenote_id : link.from_casenote_id;
    const row = el('div', 'link-row');
    row.append(el('span', 'link-direction', outgoing ? '↗' : '↙'));
    const content = el('div', 'link-content');
    const target = el('button', 'link-target', `#${relatedId}`);
    target.type = 'button';
    target.addEventListener('click', () => selectItem(relatedId));
    content.append(target);
    content.append(el('span', 'link-relation', humanize(link.relation)));
    content.append(el('span', 'link-weight', `weight ${link.weight}`));
    if (link.note) content.append(el('p', 'link-note', link.note));
    row.append(content);
    linkList.append(row);
  }
  linkSection.append(linkList);
  const linkForm = el('form', 'inline-form link-form');
  const linkFormHeading = el('h4', '', 'Add a connection');
  linkForm.append(linkFormHeading);
  const linkFields = el('div', 'link-form-fields');
  const targetInput = el('input');
  targetInput.type = 'number'; targetInput.min = '1'; targetInput.required = true;
  targetInput.placeholder = 'Casenote ID'; targetInput.setAttribute('aria-label', 'Linked casenote ID');
  targetInput.dataset.detailDraft = 'link-target';
  const relationInput = el('input');
  relationInput.type = 'text'; relationInput.required = true; relationInput.maxLength = 80;
  relationInput.placeholder = 'Relationship, e.g. supports'; relationInput.setAttribute('aria-label', 'Relationship');
  relationInput.setAttribute('list', 'relationOptions');
  relationInput.dataset.detailDraft = 'link-relation';
  const weightInput = el('input');
  weightInput.type = 'number'; weightInput.min = '0'; weightInput.max = '1'; weightInput.step = 'any'; weightInput.required = true;
  weightInput.placeholder = 'Weight 0–1'; weightInput.setAttribute('aria-label', 'Required link weight from zero to one');
  weightInput.dataset.detailDraft = 'link-weight';
  linkFields.append(targetInput, relationInput, weightInput);
  linkForm.append(linkFields);
  const noteInput = el('input');
  noteInput.type = 'text'; noteInput.maxLength = 500;
  noteInput.placeholder = 'Why are these connected?'; noteInput.setAttribute('aria-label', 'Link explanation');
  noteInput.dataset.detailDraft = 'link-note';
  linkForm.append(noteInput);
  const linkSubmit = el('button', 'button button-outline button-small', 'Add link');
  linkSubmit.type = 'submit';
  linkForm.append(linkSubmit);
  linkForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const name = actor(); if (!name) return;
    linkSubmit.disabled = true;
    state.detailMutations++;
    try {
      await post('/api/links/create', {
        project_id: casenote.project_id,
        from_casenote_id: casenote.id, to_casenote_id: Number(targetInput.value),
        relation: relationInput.value.trim(),
        weight: Number(weightInput.value),
        note: noteInput.value.trim(), author: name,
      });
      showToast('Connection added.');
      await loadDetail(casenote.id);
    } catch (error) { showToast(error.message, true); }
    finally { state.detailMutations--; linkSubmit.disabled = false; }
  });
  linkSection.append(linkForm);
  root.append(linkSection);

  const commentSection = el('section', 'detail-section comment-section');
  commentSection.append(renderSectionHeading('Discussion', 'Comments', casenote_comments.length));
  const commentList = el('div', 'comment-list');
  if (!casenote_comments.length) commentList.append(el('p', 'empty-section', 'No comments yet. Add a test result, correction, or next question.'));
  for (const comment of casenote_comments) {
    const entry = el('article', 'comment');
    const first = el('div', 'comment-meta');
    first.append(el('strong', '', comment.author || 'unknown'));
    first.append(el('time', '', compactDate(comment.created_at)));
    entry.append(first);
    const commentBody = el('div', 'comment-body markdown-body');
    renderMarkdown(commentBody, comment.body || '');
    entry.append(commentBody);
    commentList.append(entry);
  }
  commentSection.append(commentList);
  const commentForm = el('form', 'inline-form comment-form');
  const commentInput = el('textarea');
  commentInput.rows = 3; commentInput.required = true;
  commentInput.placeholder = 'Add evidence, a correction, or a question…';
  commentInput.setAttribute('aria-label', 'New comment');
  commentInput.dataset.detailDraft = 'comment-body';
  const commentSubmit = el('button', 'button button-primary button-small', 'Post comment');
  commentSubmit.type = 'submit';
  commentForm.append(commentInput, commentSubmit);
  commentForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const name = actor(); if (!name) return;
    commentSubmit.disabled = true;
    state.detailMutations++;
    try {
      await post('/api/comments/create', { project_id: casenote.project_id, casenote_id: casenote.id, author: name, body: commentInput.value.trim() });
      showToast('Comment added.');
      await Promise.all([loadDetail(casenote.id), refreshSearch()]);
    } catch (error) { showToast(error.message, true); }
    finally { state.detailMutations--; commentSubmit.disabled = false; }
  });
  commentSection.append(commentForm);
  root.append(commentSection);

  const activitySection = el('section', 'detail-section activity-section');
  activitySection.append(renderSectionHeading('Traceability', 'Activity history', activity.length));
  const activityList = el('div', 'activity-list');
  for (const entry of activity) {
    const row = el('div', 'activity-entry');
    row.append(el('strong', '', entry.actor));
    row.append(el('span', '', `${entry.summary} · ${compactDate(entry.created_at)}`));
    if (entry.details && Object.keys(entry.details).length) {
      const details = el('details', 'activity-details');
      details.append(el('summary', '', 'Event details'));
      details.append(el('pre', '', JSON.stringify(entry.details, null, 2)));
      row.append(details);
    }
    activityList.append(row);
  }
  activitySection.append(activityList);
  root.append(activitySection);
  if (preserveDraft) restoreDetailDraft(root, snapshot);
}

function openEditor(editing = false) {
  if (!state.projectId) { showToast('Choose a research project first.', true); return; }
  const existing = editing ? state.selected?.casenote : null;
  if (editing && !existing) return;
  state.editing = editing;
  dom.dialogTitle.textContent = editing ? `Edit casenote #${existing.id}` : 'New casenote';
  dom.dialogProject.textContent = `${currentProject()?.name || 'Project'} / ${state.projectSlug}`;
  dom.saveItemButton.textContent = editing ? 'Save changes' : 'Create casenote';
  dom.itemTitle.value = existing?.title || '';
  dom.itemSummary.value = existing?.summary || '';
  updateSummaryCount();
  dom.itemKind.value = existing?.kind || 'observation';
  dom.itemStatus.value = existing?.status || 'needs-work';
  dom.itemBody.value = existing?.body || '';
  dom.itemRevision.value = existing?.source_revision || '';
  dom.itemTags.value = Array.isArray(existing?.tags) ? existing.tags.join(', ') : '';
  dom.itemReviewScore.value = String(reviewScore(existing));
  dom.itemFields.value = JSON.stringify(existing?.fields || {}, null, 2);
  dom.itemDialog.showModal();
  dom.itemTitle.focus();
}

async function saveItem(event) {
  event.preventDefault();
  const summary = dom.itemSummary.value.trim();
  if (updateSummaryCount() > SUMMARY_LIMIT) {
    dom.itemSummary.focus();
    showToast('Summary must be 1,400 characters or fewer after trimming.', true);
    return;
  }
  const name = actor(); if (!name) return;
  let fields;
  try {
    fields = JSON.parse(dom.itemFields.value.trim() || '{}');
    if (!fields || typeof fields !== 'object' || Array.isArray(fields)) throw new Error('not an object');
  } catch {
    dom.itemFields.focus();
    showToast('Optional fields must be a JSON object, such as {"cvss_score": 7.5}.', true);
    return;
  }
  const existing = state.editing ? state.selected?.casenote : null;
  fields.human_review_score = Number(dom.itemReviewScore.value);
  const payload = {
    project_id: state.projectId,
    title: dom.itemTitle.value.trim(),
    summary,
    kind: dom.itemKind.value.trim(),
    status: dom.itemStatus.value.trim(),
    body: dom.itemBody.value.trim(),
    tags: parseTags(dom.itemTags.value),
    fields,
    source_revision: dom.itemRevision.value.trim(),
  };
  if (existing) {
    payload.id = existing.id;
    payload.version = existing.version;
    payload.actor = name;
  } else {
    payload.author = name;
  }
  dom.saveItemButton.disabled = true;
  try {
    const data = await post(existing ? '/api/casenotes/update' : '/api/casenotes/create', payload);
    dom.itemDialog.close();
    showToast(existing ? 'Casenote updated.' : 'Casenote created.');
    state.offset = 0;
    await refreshSearch();
    selectItem(data.casenote.id);
    refreshTagSuggestions('');
    refreshTagCloud();
  } catch (error) { showToast(error.message, true); }
  finally { dom.saveItemButton.disabled = false; }
}

async function refreshTagSuggestions(query) {
  if (!state.projectId) return;
  const request = ++state.tagRequest;
  try {
    const result = await post('/api/tags/list', { query, project_id: state.projectId });
    if (request !== state.tagRequest) return;
    dom.knownTags.replaceChildren(...(result.tags || []).map((tag) => {
      const option = el('option');
      option.value = tag;
      return option;
    }));
  } catch { /* Tag suggestions are optional; writing free-text tags still works. */ }
}

function debounce(fn, delay = 250) {
  let timer;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), delay);
  };
}

function setView(view) {
  state.view = view;
  const graphVisible = view === 'graph';
  dom.resultsPane.hidden = graphVisible;
  dom.detailPane.hidden = graphVisible;
  dom.graphPane.hidden = !graphVisible;
  if (graphVisible) loadGraph();
  else {
    state.graphRequest++;
    if (state.graph) state.graphTransform = state.graph.getViewState();
    state.graph?.destroy(); state.graph = null;
  }
}

async function loadGraph({ silent = false } = {}) {
  if (!state.projectId || state.view !== 'graph') return;
  const request = ++state.graphRequest;
  if (!silent) {
    state.graphTransform = state.graph?.getViewState() || state.graphTransform;
    state.graph?.destroy(); state.graph = null;
    state.graphSignature = null;
    dom.graphCanvas.replaceChildren(el('div', 'graph-empty', 'Loading connections…'));
    dom.graphActivity.textContent = 'Loading…';
    dom.graphTruncation.hidden = true;
  }
  try {
    const result = await post('/api/graph/get', { project_id: state.projectId, limit: 200 });
    if (request !== state.graphRequest || state.view !== 'graph') return;
    const nodes = Array.isArray(result.nodes) ? result.nodes : [];
    const edges = Array.isArray(result.edges) ? result.edges : [];
    const signature = JSON.stringify([nodes, edges, result.truncated, result.truncated_edges, result.total_nodes]);
    if (silent && signature === state.graphSignature) return;
    const previousTransform = state.graph?.getViewState() || state.graphTransform;
    state.graph?.destroy(); state.graph = null;
    state.graphSignature = signature;
    state.graphTransform = previousTransform;
    dom.graphCount.textContent = `${nodes.length} casenote${nodes.length === 1 ? '' : 's'} · ${edges.length} connection${edges.length === 1 ? '' : 's'}`;
    const truncated = Boolean(result.truncated) || Boolean(result.truncated_edges) || Number(result.total_nodes) > nodes.length;
    dom.graphTruncation.hidden = !truncated;
    if (truncated) dom.graphTruncation.textContent = `Showing ${nodes.length} casenotes and ${edges.length} links from this project. Large graphs are capped for responsiveness; use the searchable index for the full set.`;
    if (!nodes.length) {
      dom.graphCanvas.replaceChildren();
      const empty = el('div', 'graph-empty');
      empty.append(el('div', 'graph-empty-symbol', '✧'));
      empty.append(el('h3', '', 'No connections to draw yet'));
      empty.append(el('p', '', 'Create a research casenote in this project, then connect it to another casenote.'));
      const create = el('button', 'button button-primary', 'Create a casenote');
      create.type = 'button'; create.addEventListener('click', () => openEditor());
      empty.append(create); dom.graphCanvas.append(empty);
    } else {
      state.graph = mountGraph(dom.graphCanvas, nodes, edges, (id) => {
        setView('list');
        selectItem(id);
      }, previousTransform);
      state.graphTransform = state.graph.getViewState();
    }
    dom.graphActivity.textContent = '';
  } catch (error) {
    if (request !== state.graphRequest) return;
    if (!silent) {
      dom.graphCanvas.replaceChildren(el('div', 'graph-empty graph-error', error.message));
      dom.graphActivity.textContent = 'Graph unavailable';
      showToast(error.message, true);
    }
  }
}

async function refreshVisibleData() {
  if (state.refreshing || document.hidden || !state.projectId) return;
  state.refreshing = true;
  const updates = [refreshSearch({ silent: true }), refreshTagCloud({ quiet: true })];
  if (state.selectedId && !dom.itemDialog.open && state.detailMutations === 0) {
    updates.push(loadDetail(state.selectedId, { silent: true }));
  }
  if (state.view === 'graph') updates.push(loadGraph({ silent: true }));
  try { await Promise.all(updates); }
  catch { /* Keep each pane's last good data if a background request fails. */ }
  finally { state.refreshing = false; }
}

async function saveProject(event) {
  event.preventDefault();
  dom.createProjectButton.disabled = true;
  try {
    const payload = { name: dom.projectName.value.trim() };
    if (dom.projectSlugInput.value.trim()) payload.slug = dom.projectSlugInput.value.trim();
    const result = await post('/api/projects/create', payload);
    if (!result.project) throw new Error('Project was not returned by the server.');
    state.projects.push(result.project);
    state.projects.sort((a, b) => a.name.localeCompare(b.name));
    dom.projectDialog.close();
    await switchProject(result.project);
    showToast(`Project “${result.project.name}” created.`);
  } catch (error) { showToast(error.message, true); }
  finally { dom.createProjectButton.disabled = false; }
}

function bindEvents() {
  dom.themeToggle.addEventListener('click', () => {
    setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark', true);
  });
  dom.actorInput.value = localStorage.getItem('nightwatch-casefile-actor') || '';
  dom.actorInput.addEventListener('change', () => {
    const value = dom.actorInput.value.trim();
    if (value) localStorage.setItem('nightwatch-casefile-actor', value);
  });
  dom.newItemButton.addEventListener('click', () => openEditor());
  dom.emptyNewButton.addEventListener('click', () => openEditor());
  dom.projectSelect.addEventListener('change', () => {
    const project = state.projects.find((entry) => Number(entry.id) === Number(dom.projectSelect.value));
    switchProject(project);
  });
  dom.newProjectButton.addEventListener('click', () => {
    dom.projectForm.reset();
    dom.projectDialog.showModal();
    dom.projectName.focus();
  });
  dom.closeProjectDialogButton.addEventListener('click', () => dom.projectDialog.close());
  dom.cancelProjectButton.addEventListener('click', () => dom.projectDialog.close());
  dom.projectForm.addEventListener('submit', saveProject);
  dom.closeDialogButton.addEventListener('click', () => dom.itemDialog.close());
  dom.cancelDialogButton.addEventListener('click', () => dom.itemDialog.close());
  dom.itemForm.addEventListener('submit', saveItem);
  dom.itemSummary.addEventListener('input', updateSummaryCount);

  const submitSearch = () => {
    state.query = dom.searchInput.value.trim();
    state.kind = dom.kindFilter.value.trim();
    state.status = dom.statusFilter.value.trim();
    state.reviewScore = dom.reviewScoreFilter.value === '' ? null : Number(dom.reviewScoreFilter.value);
    state.reviewOperator = dom.reviewScoreOperator.value;
    if (dom.tagFilterInput.value.trim()) {
      for (const tag of parseTags(dom.tagFilterInput.value)) {
        if (!state.tags.includes(tag)) state.tags.push(tag);
      }
      dom.tagFilterInput.value = '';
      renderTagFilters();
    }
    state.offset = 0;
    refreshSearch();
  };
  dom.searchButton.addEventListener('click', submitSearch);
  [dom.searchInput, dom.kindFilter, dom.statusFilter].forEach((input) => input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') { event.preventDefault(); submitSearch(); }
  }));
  dom.addTagFilterButton.addEventListener('click', () => addTagFilter(dom.tagFilterInput.value));
  dom.tagFilterInput.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') { event.preventDefault(); submitSearch(); }
  });
  const suggestTags = debounce((value) => refreshTagSuggestions(value), 200);
  [dom.tagFilterInput, dom.itemTags].forEach((input) => {
    input.addEventListener('focus', () => refreshTagSuggestions(input.value));
    input.addEventListener('input', () => suggestTags(input.value));
  });
  dom.clearFiltersButton.addEventListener('click', () => {
    resetFilters(); refreshSearch();
  });
  dom.previousPageButton.addEventListener('click', () => {
    state.offset = Math.max(0, state.offset - PAGE_SIZE); refreshSearch();
  });
  dom.nextPageButton.addEventListener('click', () => {
    state.offset += PAGE_SIZE; refreshSearch();
  });
  dom.graphViewButton.addEventListener('click', () => setView('graph'));
  dom.listViewButton.addEventListener('click', () => setView('list'));
  dom.graphZoomIn.addEventListener('click', () => state.graph?.zoomIn());
  dom.graphZoomOut.addEventListener('click', () => state.graph?.zoomOut());
  dom.graphReset.addEventListener('click', () => state.graph?.reset());
  window.addEventListener('hashchange', () => {
    const target = hashLocation();
    const project = target.slug ? state.projects.find((entry) => entry.slug === target.slug) : currentProject();
    if (project && Number(project.id) !== state.projectId) {
      if (target.itemId) setView('list');
      switchProject(project, { itemId: target.itemId });
    }
    else if (target.itemId) { setView('list'); selectItem(target.itemId); }
    else { clearDetail(); renderSearchResults(); }
  });
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) refreshVisibleData();
  });
  window.setInterval(refreshVisibleData, REFRESH_INTERVAL);
}

const relations = document.createElement('datalist');
relations.id = 'relationOptions';
['supports', 'contradicts', 'enables', 'depends-on', 'duplicates', 'relates-to'].forEach((relation) => {
  const option = document.createElement('option'); option.value = relation; relations.append(option);
});
document.body.append(relations);

bindEvents();
loadProjects().catch((error) => {
  showToast(error.message, true);
  dom.searchActivity.textContent = 'Projects unavailable';
});
