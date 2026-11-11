// The explorer front end. It keeps a little state, asks the server for the
// view that state describes, and draws it as nested boxes with links over
// them. No build step and no dependencies: this file is served as written.

const $ = (id) => document.getElementById(id);
const SVG = 'http://www.w3.org/2000/svg';

// Groupings offered in the menu. Anything else typed into the address bar
// still works; it is added to the menu when seen.
const GROUPINGS = [
  ['source,account,region,module', 'State, account, region, module'],
  ['source,account,region,network', 'State, account, region, network'],
  ['account,region,network,module', 'Account, region, network, module'],
  ['source,module', 'State, module'],
  ['region', 'Region only'],
];

const state = {
  groupBy: GROUPINGS[0][0],
  open: null, // Set of expanded group IDs, or null to let the server choose.
  selected: '', // Node ID of the resource shown in the panel.
  search: '',
  type: '',
  account: '',
  region: '',
  status: '',
  glue: false,
  around: '', // Node ID to restrict the view to, with its neighbours.
};

let meta = { budget: 300, generation: 0, accounts: [], regions: [], statuses: [] };
let opened = []; // Group IDs in the order they were opened, oldest first.
let doc = { items: [], links: [] }; // The view last received.
let elements = new Map(); // Item ID to its element.
let paths = []; // {from, to, el} for each drawn link.
let request = 0; // Guards against an older response landing after a newer one.
let selectedLink = null; // The link whose edges the panel lists, if any.

// ---- State in the address bar -------------------------------------------

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  state.groupBy = p.get('g') || GROUPINGS[0][0];
  state.open = p.has('o') ? new Set(p.getAll('o')) : null;
  state.selected = p.get('s') || '';
  state.search = p.get('q') || '';
  state.type = p.get('t') || '';
  state.account = p.get('a') || '';
  state.region = p.get('r') || '';
  state.status = p.get('st') || '';
  state.glue = p.get('glue') === '1';
  state.around = p.get('around') || '';
}

function writeHash() {
  const p = new URLSearchParams();
  if (state.groupBy !== GROUPINGS[0][0]) p.set('g', state.groupBy);
  if (state.open) for (const id of frontier()) p.append('o', id);
  if (state.selected) p.set('s', state.selected);
  if (state.search) p.set('q', state.search);
  if (state.type) p.set('t', state.type);
  if (state.account) p.set('a', state.account);
  if (state.region) p.set('r', state.region);
  if (state.status) p.set('st', state.status);
  if (state.glue) p.set('glue', '1');
  if (state.around) p.set('around', state.around);
  const hash = p.toString();
  history.replaceState(null, '', hash ? '#' + hash : location.pathname);
}

// The open groups that have no open group inside them. The server opens
// everything above a group it is told to open, so these say it all.
function frontier() {
  const parent = new Map(doc.items.map((i) => [i.id, i.parent || '']));
  const leaves = new Set(state.open);
  for (const id of state.open) {
    for (let up = parent.get(id); up; up = parent.get(up)) leaves.delete(up);
  }
  return [...leaves];
}

// ---- Talking to the server ----------------------------------------------

function filterParams() {
  const p = new URLSearchParams({ group_by: state.groupBy });
  if (state.search) p.set('search', state.search);
  if (state.type) p.set('type', state.type);
  if (state.account) p.set('account', state.account);
  if (state.region) p.set('region', state.region);
  if (state.status) p.set('status', state.status);
  if (state.glue) p.set('glue', '1');
  if (state.around) p.set('around', state.around);
  return p;
}

async function getJSON(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  noteGeneration(Number(res.headers.get('X-Moonwalk-Generation')));
  return res.json();
}

// The server counts the times it has loaded its states. A new count means
// a state file changed on disk and everything on screen may be stale.
let refreshing = false;
function noteGeneration(generation) {
  if (!generation || generation === meta.generation || refreshing) return;
  const first = !meta.generation;
  meta.generation = generation;
  if (first) return;
  refreshing = true;
  setTimeout(async () => {
    try {
      await loadMeta();
      await load();
      flash('The state changed on disk; the view was reloaded.');
    } finally {
      refreshing = false;
    }
  }, 0);
}

async function loadMeta() {
  const next = await getJSON('api/meta');
  meta = next;
  fillFilter('account', 'All accounts', meta.accounts);
  fillFilter('region', 'All regions', meta.regions);
  fillFilter('status', 'Any status', meta.statuses);
  syncControls();
}

// Offers the values present, plus the one in use even if it is gone.
function fillFilter(id, all, values) {
  const menu = $(id);
  const options = [new Option(all, '')];
  for (const v of new Set([...values, state[id]].filter(Boolean))) options.push(new Option(v, v));
  menu.replaceChildren(...options);
  menu.hidden = values.length < 2 && !state[id];
}

// load fetches the view the state describes and draws it. justOpened names
// a group the user has just expanded, for keeping within the budget.
async function load(justOpened) {
  const mine = ++request;
  const p = filterParams();
  if (state.open) {
    const open = frontier();
    // With every group closed there is nothing to name; depth 1 says that.
    if (open.length) for (const id of open) p.append('open', id);
    else p.set('depth', '1');
  }
  try {
    const next = await getJSON('api/view?' + p);
    if (mine !== request) return;
    doc = next;
    state.open = new Set(doc.items.filter((i) => i.expanded).map((i) => i.id));
    opened = opened.filter((id) => state.open.has(id));
    if (justOpened && makeRoom(justOpened)) return load();
    render();
    writeHash();
    if (selectedLink && !doc.links.some((l) => l.from === selectedLink.from && l.to === selectedLink.to && l.kind === selectedLink.kind)) {
      selectedLink = null; // The link is no longer on screen.
    }
    if (state.selected) showResource(state.selected, false);
    else if (selectedLink) showLink(selectedLink);
    else closePanel();
  } catch (err) {
    if (mine === request) notice(err.message, true);
  }
}

// makeRoom closes other groups, oldest first, when opening one has put more
// items on screen than the budget allows. The group just opened and the
// groups it sits in stay open. It reports whether it closed anything.
function makeRoom(justOpened) {
  let total = doc.items.length;
  if (total <= meta.budget) return false;
  const parent = new Map(doc.items.map((i) => [i.id, i.parent || '']));
  const keep = new Set();
  for (let up = justOpened; up; up = parent.get(up)) keep.add(up);
  // How many items each open group holds, at any depth.
  const inside = new Map();
  for (const item of doc.items) {
    for (let up = parent.get(item.id); up; up = parent.get(up)) inside.set(up, (inside.get(up) || 0) + 1);
  }
  const age = (id) => { const i = opened.indexOf(id); return i < 0 ? -1 : i; }; // Opened by the server: oldest.
  let closed = 0;
  while (total > meta.budget) {
    // Only a group with no open group inside it can close on its own.
    const candidates = frontier().filter((id) => !keep.has(id)).sort((a, b) => age(a) - age(b));
    if (!candidates.length) break;
    const id = candidates[0];
    state.open.delete(id);
    const removed = inside.get(id) || 0;
    total -= removed;
    for (let up = parent.get(id); up; up = parent.get(up)) inside.set(up, inside.get(up) - removed);
    closed++;
  }
  if (closed) flash(`Closed ${plural(closed, 'other group')} to keep the view within ${meta.budget} items.`);
  return closed > 0;
}

// ---- Drawing the view ---------------------------------------------------

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

const kindName = (kind) => kind.replace('_', ' ');
const plural = (n, noun) => `${n.toLocaleString()} ${noun}${n === 1 ? '' : 's'}`;

function render() {
  const root = $('root');
  root.replaceChildren();
  elements = new Map();
  const holders = new Map([['', root]]);

  for (const item of doc.items) {
    let node;
    if (item.expanded) {
      node = el('section', 'group');
      const title = el('button', 'title', item.label);
      title.append(el('small', '', `${kindName(item.kind)} · ${item.resources.toLocaleString()}`));
      title.title = 'Collapse';
      title.addEventListener('click', () => toggle(item.id));
      const children = el('div', 'children');
      node.append(title, children);
      holders.set(item.id, children);
    } else if (item.kind === 'resource') {
      node = el('button', 'item resource', item.label);
      node.addEventListener('click', () => select(item.id));
    } else {
      node = el('button', 'item collapsed', item.label);
      node.append(el('small', '', item.resources.toLocaleString()));
      node.title = `${kindName(item.kind)}, ${plural(item.resources, 'resource')}. Expand`;
      node.addEventListener('click', () => toggle(item.id));
    }
    node.dataset.id = item.id;
    node.addEventListener('mouseenter', () => highlight(item.id));
    node.addEventListener('mouseleave', () => highlight(state.selected));
    elements.set(item.id, node);
    (holders.get(item.parent || '') || root).append(node);
  }

  summarize();
  drawLinks();
  highlight(state.selected);
}

function summarize() {
  const parts = [plural(doc.resources, 'resource')];
  const left = doc.total - doc.glue_folded - doc.resources;
  if (left > 0) parts.push(`${left.toLocaleString()} filtered out`);
  if (doc.glue_folded > 0) parts.push(`${doc.glue_folded.toLocaleString()} glue folded`);
  $('summary').textContent = parts.join(' · ');

  if (flashText) {
    notice(flashText);
    flashText = '';
  } else if (state.around) {
    const p = $('notice');
    p.className = '';
    p.hidden = false;
    p.textContent = `Showing only ${shortID(state.around)} and what is directly related to it.`;
    const clear = el('button', '', 'Show everything');
    clear.addEventListener('click', () => refilter(() => { state.around = ''; }));
    p.append(clear);
  } else if (doc.items.length > meta.budget) {
    notice(`${doc.items.length.toLocaleString()} items are open. Collapse a group to keep the picture readable.`);
  } else {
    $('notice').hidden = true;
  }
}

// flash shows a message with the next view that is drawn.
let flashText = '';
function flash(text) { flashText = text; }

function notice(text, isError) {
  const p = $('notice');
  p.className = isError ? 'error' : '';
  p.textContent = text;
  p.hidden = false;
}

const shortID = (id) => id.slice(id.indexOf('::') + 2);

// Links run between the borders of the two boxes, slightly curved so that
// a pair of links in opposite directions does not draw as one line. Each
// has a wide invisible twin to click on, and its count when it stands for
// more than one edge.
function drawLinks() {
  const svg = $('links');
  for (const old of svg.querySelectorAll('.link, .hit, .count')) old.remove();
  paths = [];
  const base = $('stage').getBoundingClientRect();
  const rect = (id) => {
    const node = elements.get(id);
    if (!node) return null;
    const r = node.getBoundingClientRect();
    return { x: r.left - base.left, y: r.top - base.top, w: r.width, h: r.height };
  };
  const make = (tag, className) => {
    const node = document.createElementNS(SVG, tag);
    node.setAttribute('class', className);
    return node;
  };
  for (const link of doc.links) {
    const a = rect(link.from), b = rect(link.to);
    if (!a || !b) continue;
    const ca = { x: a.x + a.w / 2, y: a.y + a.h / 2 }, cb = { x: b.x + b.w / 2, y: b.y + b.h / 2 };
    const start = edgePoint(a, ca, cb), end = edgePoint(b, cb, ca);
    const dx = end.x - start.x, dy = end.y - start.y, len = Math.hypot(dx, dy) || 1;
    const bend = Math.min(26, len / 6);
    const mid = { x: (start.x + end.x) / 2 - (dy / len) * bend, y: (start.y + end.y) / 2 + (dx / len) * bend };
    const d = `M${start.x} ${start.y}Q${mid.x} ${mid.y} ${end.x} ${end.y}`;

    const path = make('path', 'link ' + link.kind);
    path.setAttribute('d', d);
    const hit = make('path', 'hit');
    hit.setAttribute('d', d);
    hit.addEventListener('click', () => selectLink(link));
    hit.addEventListener('mouseenter', () => path.classList.add('hover'));
    hit.addEventListener('mouseleave', () => path.classList.remove('hover'));
    const what = link.kind === 'links' ? 'glue link' : 'dependency';
    const tip = make('title', '');
    tip.textContent = link.count === 1 ? `1 ${what}` : `${link.count.toLocaleString()} ${what === 'dependency' ? 'dependencies' : 'glue links'}`;
    hit.append(tip);
    svg.append(path, hit);

    let count = null;
    if (link.count > 1) {
      // On the curve itself, a quarter of the way along the bend.
      count = make('text', 'count');
      count.setAttribute('x', (start.x + 2 * mid.x + end.x) / 4);
      count.setAttribute('y', (start.y + 2 * mid.y + end.y) / 4 + 4);
      count.textContent = link.count.toLocaleString();
      svg.append(count);
    }
    paths.push({ from: link.from, to: link.to, kind: link.kind, el: path, count });
  }
}

// The point where the line from the centre of a box towards another point
// leaves the box.
function edgePoint(r, from, to) {
  const dx = to.x - from.x, dy = to.y - from.y;
  if (!dx && !dy) return from;
  const scale = Math.min(dx ? r.w / 2 / Math.abs(dx) : Infinity, dy ? r.h / 2 / Math.abs(dy) : Infinity);
  return { x: from.x + dx * scale, y: from.y + dy * scale };
}

// Lights up one item, its links and the items at their other ends, and dims
// everything else. With no item, nothing is dimmed. The selected resource
// or link keeps its mark whichever item is lit.
function highlight(id) {
  const lit = Boolean(id && elements.has(id));
  const chosen = selectedLink;
  $('stage').classList.toggle('dim', lit || Boolean(chosen));
  for (const node of elements.values()) node.classList.remove('selected', 'lit', 'related');
  for (const p of paths) {
    const isChosen = chosen && p.from === chosen.from && p.to === chosen.to && p.kind === chosen.kind;
    const on = isChosen || (lit && (p.from === id || p.to === id));
    p.el.classList.toggle('on', Boolean(on));
    p.count?.classList.toggle('on', Boolean(on));
    if (isChosen) {
      elements.get(p.from)?.classList.add('related');
      elements.get(p.to)?.classList.add('related');
    } else if (on) {
      elements.get(p.from === id ? p.to : p.from)?.classList.add('related');
    }
  }
  if (lit) elements.get(id).classList.add('lit');
  elements.get(state.selected)?.classList.add('selected');
}

// ---- Acting on it -------------------------------------------------------

function toggle(id) {
  if (!state.open) state.open = new Set();
  if (state.open.has(id)) {
    // Close the group and every open group inside it.
    const parent = new Map(doc.items.map((i) => [i.id, i.parent || '']));
    for (const other of [...state.open]) {
      for (let up = other; up; up = parent.get(up)) {
        if (up === id) { state.open.delete(other); break; }
      }
    }
    opened = opened.filter((other) => state.open.has(other));
    load();
  } else {
    state.open.add(id);
    opened.push(id);
    load(id);
  }
}

function select(id) {
  selectedLink = null;
  state.selected = id;
  writeHash();
  highlight(id);
  showResource(id, false);
}

// Opens whatever is needed for a resource to be on screen, then selects it.
async function reveal(id) {
  try {
    const r = await getJSON(`api/resource?id=${encodeURIComponent(id)}&${filterParams()}`);
    selectedLink = null;
    state.selected = id;
    if (r.shown && !elements.has(id)) {
      if (!state.open) state.open = new Set();
      if (r.group) { state.open.add(r.group); opened.push(r.group); }
      await load(r.group);
    } else {
      writeHash();
      highlight(id);
      showResource(id, false);
    }
    elements.get(id)?.scrollIntoView({ block: 'center', inline: 'center', behavior: 'smooth' });
    elements.get(id)?.focus({ preventScroll: true });
  } catch (err) {
    notice(err.message, true);
  }
}

// The panel is always there, so that choosing a resource never moves the
// boxes under the pointer.
function closePanel() {
  $('panel').replaceChildren(el('p', 'note', 'Choose a resource to see what it is and what it is related to.'));
  highlight(state.selected);
}

function deselect() {
  selectedLink = null;
  state.selected = '';
  writeHash();
  closePanel();
}

// ---- The details panel --------------------------------------------------

async function showResource(id) {
  let r;
  try {
    r = await getJSON(`api/resource?id=${encodeURIComponent(id)}&${filterParams()}`);
  } catch (err) {
    if (state.selected === id) deselect();
    return;
  }
  if (state.selected !== id) return;
  const n = r.node;
  const panel = $('panel');
  panel.replaceChildren();

  const close = el('button', 'close', '×');
  close.title = 'Close (Esc)';
  close.setAttribute('aria-label', 'Close details');
  close.addEventListener('click', deselect);
  panel.append(close, el('h2', '', n.address), el('p', 'sub', `${n.type} · ${n.source}`));

  const facts = el('dl');
  const fact = (name, value) => { if (value) facts.append(el('dt', '', name), el('dd', '', value)); };
  fact('Account', n.account && n.account + (n.account_inferred ? ' (inferred)' : ''));
  fact('Region', n.region && n.region + (n.region_inferred ? ' (inferred)' : ''));
  fact('Status', n.status);
  fact('Kind', n.mode === 'data' ? 'data source' : n.glue ? 'managed, glue' : 'managed');
  fact('Provider', n.provider);
  fact('Module', n.module);
  panel.append(facts);

  if (!r.shown) {
    panel.append(el('p', 'note', n.glue && !state.glue
      ? 'Not drawn: this glue resource is folded into a link between what it joins.'
      : 'Not drawn: the current filter leaves this resource out.'));
  }
  const only = el('button', 'action', 'Show only this and what is related to it');
  only.addEventListener('click', () => refilter(() => { state.around = id; }));
  const actions = el('p', 'actions');
  actions.append(only);
  panel.append(actions);

  if (r.container) refList(panel, 'Inside', { total: 1, items: [r.container] });
  refList(panel, 'Joins', r.joins);
  refList(panel, 'Depends on', r.depends_on);
  refList(panel, 'Depended on by', r.depended_on_by);
  refList(panel, 'Attached to', r.linked_to);
  refList(panel, 'Contains', r.contains);

  if (n.attributes_withheld) {
    panel.append(el('h3', '', 'Attributes'), el('p', 'note',
      'Withheld. This state was written by a version of Terraform or OpenTofu that did not record ' +
      'which attributes are sensitive, so none can be shown safely. Load the output of ' +
      '"terraform show -json" from version 1.5 or later to see them.'));
  }
  if (n.attributes && Object.keys(n.attributes).length) {
    const details = el('details');
    details.append(el('summary', '', 'Attributes'), el('pre', '', JSON.stringify(n.attributes, null, 2)));
    panel.append(details);
  }
  highlight(state.selected);
}

// Shows the edges a link between two items stands for.
async function selectLink(link) {
  selectedLink = link;
  state.selected = '';
  writeHash();
  highlight('');
  showLink(link);
}

async function showLink(link) {
  let r;
  try {
    const p = filterParams();
    p.set('from', link.from);
    p.set('to', link.to);
    r = await getJSON('api/link?' + p);
  } catch (err) {
    return;
  }
  if (selectedLink !== link) return;
  const label = (id) => doc.items.find((i) => i.id === id)?.label || shortID(id);
  const panel = $('panel');
  panel.replaceChildren();
  const close = el('button', 'close', '×');
  close.title = 'Close (Esc)';
  close.setAttribute('aria-label', 'Close details');
  close.addEventListener('click', deselect);
  const glue = link.kind === 'links';
  panel.append(close, el('h2', '', `${label(link.from)} ${glue ? '—' : '→'} ${label(link.to)}`),
    el('p', 'sub', glue ? plural(r.total, 'glue link') : (r.total === 1 ? '1 dependency' : `${r.total.toLocaleString()} dependencies`)));
  const ul = el('ul');
  for (const pair of r.pairs) {
    const li = el('li', 'pair');
    li.append(pairButton(pair.from), el('span', 'more', glue ? ' — ' : ' → '), pairButton(pair.to));
    if (pair.via) li.append(el('span', 'more', ' by '), pairButton(pair.via));
    ul.append(li);
  }
  if (r.total > r.pairs.length) ul.append(el('li', 'more', `and ${(r.total - r.pairs.length).toLocaleString()} more`));
  panel.append(ul);
  highlight('');
}

function pairButton(ref) {
  const button = el('button', 'ref', ref.address);
  button.title = `${ref.type} · ${ref.source}`;
  button.addEventListener('click', () => reveal(ref.id));
  return button;
}

function refList(panel, name, list) {
  if (!list.total) return;
  panel.append(el('h3', '', `${name} (${list.total.toLocaleString()})`));
  const ul = el('ul');
  for (const ref of list.items) {
    const li = el('li');
    li.append(refButton(ref));
    if (ref.via) li.append(el('span', 'more', ' by '), refButton(ref.via));
    ul.append(li);
  }
  if (list.total > list.items.length) {
    ul.append(el('li', 'more', `and ${(list.total - list.items.length).toLocaleString()} more`));
  }
  panel.append(ul);
}

function refButton(ref) {
  const label = ref.source === shownSource() ? ref.address : `${ref.source}::${ref.address}`;
  const button = el('button', 'ref', label);
  button.title = ref.type;
  button.addEventListener('click', () => reveal(ref.id));
  return button;
}

// The source of the selected resource; other sources are spelled out.
function shownSource() {
  const i = state.selected.indexOf('::');
  return i < 0 ? '' : state.selected.slice(0, i);
}

// ---- Controls -----------------------------------------------------------

function debounce(fn, ms) {
  let timer;
  return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), ms); };
}

function syncControls() {
  const menu = $('group-by');
  if (![...menu.options].some((o) => o.value === state.groupBy)) {
    menu.append(new Option(state.groupBy.replaceAll(',', ', '), state.groupBy));
  }
  menu.value = state.groupBy;
  $('search').value = state.search;
  $('type').value = state.type;
  for (const id of ['account', 'region', 'status']) $(id).value = state[id];
  $('glue').checked = state.glue;
}

// A change of grouping or filter makes a different tree, so the open groups
// no longer apply; the server picks a fresh starting depth.
function refilter(change) {
  change();
  state.open = null;
  opened = [];
  selectedLink = null;
  load();
}

async function start() {
  for (const [value, label] of GROUPINGS) $('group-by').append(new Option(label, value));
  $('group-by').addEventListener('change', (e) => refilter(() => { state.groupBy = e.target.value; }));
  $('search').addEventListener('input', debounce((e) => refilter(() => { state.search = e.target.value.trim(); }), 250));
  $('type').addEventListener('input', debounce((e) => refilter(() => { state.type = e.target.value.trim(); }), 250));
  $('glue').addEventListener('change', (e) => refilter(() => { state.glue = e.target.checked; }));
  for (const id of ['account', 'region', 'status']) {
    $(id).addEventListener('change', (e) => refilter(() => { state[id] = e.target.value; }));
  }

  document.addEventListener('keydown', (e) => {
    const typing = /^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement?.tagName || '');
    if (e.key === '/' && !typing) { e.preventDefault(); $('search').focus(); }
    if (e.key === 'Escape') { if (typing) document.activeElement.blur(); else if (state.selected || selectedLink) deselect(); }
  });
  window.addEventListener('resize', debounce(() => { drawLinks(); highlight(state.selected); }, 100));
  window.addEventListener('hashchange', () => { readHash(); syncControls(); load(); });

  readHash();
  try {
    await loadMeta();
  } catch (err) {
    notice(err.message, true);
  }
  load();
  // Ask now and then whether a state file has changed. Any answer carries
  // the server's generation, which noteGeneration acts on.
  setInterval(() => { if (!document.hidden) getJSON('api/meta').catch(() => {}); }, 3000);
}

start();
