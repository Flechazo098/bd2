'use strict';
const $ = id => document.getElementById(id);
const state = {session: null, players: [], view: ''};

/* ---------- Text ---------- */
const locales = [
  {id: 'en_us', tag: 'en-US'}, {id: 'ko_kr', tag: 'ko-KR'}, {id: 'ja_jp', tag: 'ja-JP'}, {id: 'zh_tw', tag: 'zh-TW'}, {id: 'zh_cn', tag: 'zh-CN'},
];
const localeStorageKey = 'bd2.console.locale';
const translations = {};
let locale = 'en_us';
function lookup(key) {
  for (const id of [locale, 'en_us']) {
    const table = translations[id];
    if (table && Object.hasOwn(table, key)) return table[key];
  }
  return undefined;
}

const staleRender = Symbol('stale render'), renderPasses = new Map(), renderNodes = new WeakMap();
function beginRender(id) {
  const store = typeof id === 'string' ? renderPasses : renderNodes;
  const pass = {id, locale, store};
  store.set(id, pass);
  return pass;
}
function rendered(value, pass) {
  if (locale !== pass.locale || pass.store.get(pass.id) !== pass) throw staleRender;
  return value;
}
const {Component} = window.BD2Text;
Component.configure({lookup, locale: () => locale, formatURL: '/admin/api/text/format'});
const t = key => lookup(key) ?? key;
const dynamicText = async (key, ...args) => await Component.translatable(key, ...args.map(value => value && typeof value.getString === 'function' ? value : value == null ? '' : String(value))).getString();
// Server-provided text: the translation when one exists, otherwise the server's own text.
const tr = (key, fallback) => lookup(key) ? t(key) : (fallback ?? '');
async function rich(key, ...values) {
  return [await dynamicText(key, ...values.map(value => value && value.nodeType ? value.textContent : value))];
}
const tag = () => locales.find(l => l.id === locale).tag;
const listJoin = items => items.join(t('console.list.separator'));
function detectLocale() {
  try { const saved = localStorage.getItem(localeStorageKey); if (locales.some(l => l.id === saved)) return saved; } catch {}
  for (const raw of navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language || '']) {
    const name = String(raw).toLowerCase();
    if (name.startsWith('ko')) return 'ko_kr';
    if (name.startsWith('ja')) return 'ja_jp';
    if (/^zh-(tw|hk|mo|hant)(-|$)/.test(name)) return 'zh_tw';
    if (name.startsWith('zh')) return 'zh_cn';
    if (name.startsWith('en')) return 'en_us';
  }
  return 'en_us';
}
async function loadTranslations() {
  await Promise.all(locales.map(async l => {
    try {
      const response = await fetch('assets/lang/' + l.id + '.json', {credentials: 'same-origin'});
      if (response.ok) translations[l.id] = await response.json();
    } catch {}
  }));
}
function localize(root = document) {
  root.querySelectorAll('[data-i18n]').forEach(node => { node.textContent = t(node.dataset.i18n); });
  root.querySelectorAll('[data-i18n-attr]').forEach(node => node.dataset.i18nAttr.split(';').forEach(pair => {
    const at = pair.indexOf(':');
    if (at > 0) node.setAttribute(pair.slice(0, at).trim(), t(pair.slice(at + 1).trim()));
  }));
}
async function applyLocale(id) {
  locale = id;
  for (const node of noted) if (!node.isConnected) noted.delete(node);
  document.documentElement.lang = tag();
  document.title = t('console.title');
  $('locale').value = id;
  localize();
  $('ask-ok').textContent = t('console.dialog.ok');
  await Promise.all([...noted].map(async node => node._text ? await note(node, node._text) : undefined));
}
$('locale').onchange = async () => {
  const id = $('locale').value;
  try { localStorage.setItem(localeStorageKey, id); } catch {}
  await applyLocale(id); await rerender();
};

/* ---------- Common ---------- */
async function api(path, {method = 'GET', body, headers = {}} = {}) {
  const h = {...headers};
  if (method !== 'GET') {
    h['X-BD2-Console'] = '1';
    if (state.session && state.session.csrf) h['X-CSRF-Token'] = state.session.csrf;
  }
  if (body !== undefined && typeof body !== 'string') { h['Content-Type'] = 'application/json'; body = JSON.stringify(body); }
  const response = await fetch('api/' + path, {method, headers: h, body, credentials: 'same-origin'});
  const text = await response.text();
  let value = null;
  if (text) { try { value = JSON.parse(text); } catch { value = null; } }
  if (response.status === 401 && path !== 'session') { await boot(); }
  if (!response.ok && response.status !== 202) {
    const code = value && value.code;
    const component = code && lookup('console.error.' + code) !== undefined ? Component.translatable('console.error.' + code)
      : value && value.error ? Component.literal(value.error) : Component.translatable('console.error.http', String(response.status));
    const error = new Error(await component.getString());
    error.text = component; error.status = response.status; error.code = code; error.value = value;
    throw error;
  }
  return {status: response.status, value};
}
const textOf = value => value instanceof Error ? value.text || Component.literal(value.message) : value instanceof Component ? value : Component.literal(value == null ? '' : String(value));
function esc(value) { const e = document.createElement('span'); e.textContent = value == null ? '' : String(value); return e.innerHTML.replace(/"/g, '&quot;'); }
function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value == null || value === false) continue;
    if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
    else if (value === true) node.setAttribute(key, '');
    else node.setAttribute(key, value);
  }
  for (const child of children.flat()) if (child != null && child !== false) node.append(child.nodeType ? child : document.createTextNode(child));
  return node;
}
// Dynamic text lines remember their text so a language switch can redraw them.
const noted = new Set();
async function note(node, text) {
  const component = textOf(text), pass = {}; node._text = component; node._textPass = pass; delete node.dataset.i18n; noted.add(node);
  const value = await component.getString();
  if (node._textPass === pass) node.textContent = value;
}
async function say(node, text, tone = '') { await note(node, text); node.className = 'status' + (tone ? ' ' + tone : ''); }
let toastTimer;
async function toast(text) {
    const renderPass = beginRender('toast');
    try { const box = $('toast'); box.textContent = rendered(await textOf(text).getString(), renderPass); box.classList.add('show'); clearTimeout(toastTimer); toastTimer = setTimeout(() => box.classList.remove('show'), 1800);
    } catch (error) { if (error !== staleRender) throw error; }
  }
async function copy(text, label) {
  try { await navigator.clipboard.writeText(text); }
  catch { await toast(Component.translatable('console.copy.failed')); return; }
  await toast(Component.translatable('console.copy.done', label));
}
function ask({title, body, extra = null, ok = t('console.dialog.ok'), danger = false}) {
  const dialog = $('ask');
  $('ask-title').textContent = title; $('ask-body').textContent = body;
  $('ask-extra').replaceChildren(...(extra ? [extra] : []));
  $('ask-ok').textContent = ok; $('ask-ok').classList.toggle('seal', true);
  return new Promise(resolve => {
    const done = answer => { $('ask-ok').onclick = $('ask-cancel').onclick = dialog.oncancel = null; dialog.close(); $('ask-ok').textContent = t('console.dialog.ok'); resolve(answer); };
    $('ask-ok').onclick = () => done(true); $('ask-cancel').onclick = () => done(false);
    dialog.oncancel = event => { event.preventDefault(); done(false); };
    dialog.showModal(); (danger ? $('ask-cancel') : $('ask-ok')).focus();
  });
}
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const fmt = n => Number(n).toLocaleString(tag());
const dateTime = value => new Date(value).toLocaleString(tag());
const playerLabel = p => p ? (p.uid ? 'UID ' + p.uid : '') + (p.name ? ' · ' + p.name : '') : '';

/* ---------- Sign-in and routing ---------- */
let loginTimer;
async function boot() {
  clearTimeout(loginTimer);
  try {
    state.session = (await api('session')).value;
  } catch (error) {
    showOnly('sign-in'); await say($('sign-in-status'), error, 'err'); return;
  }
  if (!state.session.signed_in) return await showSignIn();
  $('tabs').hidden = false; $('account').hidden = false;
  const views = allowedViews();
  document.querySelectorAll('#tabs a').forEach(a => { a.hidden = !views.includes(a.dataset.view); });
  await paintAccount();
  if (can('players.view')) await loadPlayers();
  await route();
}
async function paintAccount() {
    const renderPass = beginRender('account');
    try {
    const player = state.session && state.session.player;
    $('account-name').textContent = player ? rendered(await dynamicText(isOwner() ? 'console.account.as_owner' : 'console.account.as_admin', playerLabel(player)), renderPass) : t('console.role.owner');

    } catch (error) { if (error !== staleRender) throw error; }
  }
const isOwner = () => state.session && state.session.role === 'owner';
const can = permission => isOwner() || (state.session && (state.session.permissions || []).includes(permission));
function allowedViews() {
  const views = [];
  if (can('mail.send')) views.push('mail');
  if (can('players.view')) views.push('players');
  if (state.session.configuration) views.push('config');
  if (isOwner()) views.push('updates');
  return views;
}
function showOnly(view) {
  document.querySelectorAll('.view').forEach(v => { v.hidden = v.id !== 'view-' + view; });
  document.querySelectorAll('#tabs a').forEach(a => { if (a.dataset.view === view) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
}
const providerNames = {discord: 'Discord', google: 'Google'};
async function showSignIn() {
    const renderPass = beginRender('sign-in');
    try {
    $('tabs').hidden = true; $('account').hidden = true; showOnly('sign-in');
    const providers = state.session.providers || [];
    if (state.session.mode !== 'oauth' || !providers.length) {
      rendered(await note($('sign-in-lead'), Component.translatable('console.signin.local')), renderPass);
      $('providers').replaceChildren(); return;
    }
    rendered(await note($('sign-in-lead'), Component.translatable('console.signin.oauth')), renderPass);
    $('providers').replaceChildren(...rendered(await Promise.all(providers.map(async p => el('button', {type: 'button', class: 'btn' + (p === providers[0] ? ' seal' : ''), onclick: async () => await startLogin(p)}, await dynamicText('console.signin.provider', providerNames[p] || p)))), renderPass));

    } catch (error) { if (error !== staleRender) throw error; }
  }
async function startLogin(provider) {
  const popup = window.open('about:blank', 'bd2-console-login');
  await say($('sign-in-status'), Component.translatable('console.signin.opening'));
  try {
    const {value} = await api('login/start', {method: 'POST', body: {provider}});
    if (popup) { popup.opener = null; popup.location.href = value.start_url; await say($('sign-in-status'), Component.translatable('console.signin.continue_in_popup')); }
    else { $('sign-in-status')._text = null; const link = el('a', {href: value.start_url, target: '_blank', rel: 'noopener'}, t('console.signin.open_page')); $('sign-in-status').replaceChildren(...await rich('console.signin.popup_blocked', link), ' ', link); }
    await pollLogin(value.poll_interval_ms || 2000);
  } catch (error) { if (popup) popup.close(); await say($('sign-in-status'), error, 'err'); }
}
async function pollLogin(interval) {
  try {
    const result = await api('login/poll', {method: 'POST'});
    if (result.status === 202) { loginTimer = setTimeout(async () => await pollLogin(interval), interval); return; }
    await say($('sign-in-status'), Component.translatable('console.signin.success'), 'ok'); await boot();
  } catch (error) { await say($('sign-in-status'), error, 'err'); }
}
$('logout').onclick = async () => {
  if (config.dirty() && !await ask({title: t('console.logout.confirm.title'), body: t('console.logout.confirm.body'), ok: t('console.logout.confirm.ok'), danger: true})) return;
  config.reset();
  try { await api('logout', {method: 'POST'}); } catch {}
  state.session = null; await boot();
};
let currentView = '';
async function route() {
  if (!state.session || !state.session.signed_in) return;
  const views = allowedViews();
  if (!views.length) { showOnly('sign-in'); $('providers').replaceChildren(); await note($('sign-in-lead'), Component.translatable('console.signin.no_permissions')); return; }
  let view = (location.hash || '#' + views[0]).slice(1);
  if (!views.includes(view)) view = views[0];
  if (currentView === 'config' && view !== 'config' && config.dirty()) {
    if (!await ask({title: t('console.leave.title'), body: t('console.leave.body'), ok: t('console.leave.ok')})) { history.replaceState(null, '', '#config'); return; }
  }
  currentView = view; showOnly(view);
  if (view === 'updates') await updatesView.open(); else updatesView.close();
  if (view === 'mail') await mailTool.open();
  if (view === 'players') await roster.open();
  if (view === 'config') await config.open();
}
window.addEventListener('hashchange', route);
window.addEventListener('beforeunload', event => { if (config.dirty()) { event.preventDefault(); event.returnValue = ''; } });
// Redraws the visible screen in the newly selected language without reloading data.
async function rerender() {
  if (!state.session) return;
  if (!state.session.signed_in) { await showSignIn(); return; }
  await paintAccount();
  if (currentView === 'mail') await mailTool.relabel();
  if (currentView === 'players') await roster.relabel();
  if (currentView === 'config') await config.relabel();
  if (currentView === 'updates') await updatesView.relabel();
}

async function loadPlayers() {
  try { state.players = (await api('players')).value.players; } catch (error) { state.players = []; await toast(error); }
}

/* ---------- Mail ---------- */
const categoryName = category => tr('console.mail.category.' + category, category);
const mailTool = (() => {
  let all = [], cats = [], category = '', view = [], cursor = -1, chosen = null, byKey = new Map(), target = null, loaded = false, submitting = false, inventory = null, waitingCatalog = false;
  const log = [];
  const keyOf = x => x.element_type + ':' + x.id;
  const catName = x => categoryName(x.category);
  async function details(x) {
    if (x._detailsLocale === locale) return x._details;
    const language = locale;
    const text = await itemDetails(x);
    if (language !== locale) return await details(x);
    x._detailsLocale = language; x._details = text; return text;
  }
  async function itemDetails(x) {
    if (x.category === 'currency') return t('console.mail.detail.currency');
    if (!x.source_boxes) return '';
    const parts = [t('console.mail.detail.direct')];
    if (x.original_name) parts.push(await dynamicText('console.mail.detail.original_name', x.original_name));
    if (x.box_name) parts.push(await dynamicText('console.mail.detail.box_name', x.box_name));
    if (x.product_name) parts.push(await dynamicText('console.mail.detail.product_name', x.product_name));
    const first = listJoin(x.source_boxes.slice(0, 4));
    parts.push(await dynamicText('console.mail.detail.boxes', x.source_boxes.length > 4 ? await dynamicText('console.mail.detail.boxes_more', first, x.source_boxes.length) : first));
    return parts.join(t('console.sentence.separator'));
  }
  function highlight(text, term) {
    if (!term) return esc(text);
    const at = text.toLowerCase().indexOf(term);
    if (at < 0) return esc(text);
    return esc(text.slice(0, at)) + '<mark>' + esc(text.slice(at, at + term.length)) + '</mark>' + esc(text.slice(at + term.length));
  }
  const subline = async x => x.aliases && x.aliases.length ? await dynamicText('console.mail.aliases', listJoin(x.aliases)) : await details(x);
  const searchText = x => [x.id, x.element_type, keyOf(x), x.name, catName(x), ...(x.aliases || []), x.original_name, x.box_name, x.product_name, ...(x.source_boxes || []), t(x.category === 'currency' ? 'console.mail.detail.currency' : 'console.mail.detail.direct')].filter(value => value != null).join(' ').toLowerCase();
  function buildCategories() {
    const counts = new Map();
    all.forEach(x => counts.set(x.category, (counts.get(x.category) || 0) + 1));
    cats = [['', t('console.mail.category_all'), all.length], ...[...counts].map(([key, n]) => [key, categoryName(key), n])];
    $('cats').innerHTML = cats.map(([value, label, n], i) => `<button type="button" class="chip" data-i="${i}" aria-pressed="${value === category}">${esc(label)}<small>${esc(fmt(n))}</small></button>`).join('');
  }
  $('cats').onclick = async event => {
    const chip = event.target.closest('.chip'); if (!chip) return;
    category = cats[+chip.dataset.i][0];
    $('cats').querySelectorAll('.chip').forEach(c => c.setAttribute('aria-pressed', c === chip));
    await render(); $('q').focus();
  };
  async function render() {
    const renderPass = beginRender('mail-items');
    try {
      const terms = $('q').value.toLowerCase().trim().split(/\s+/).filter(Boolean);
      const matches = all.filter(x => (!category || x.category === category) && terms.every(term => x._h.includes(term)));
      view = matches.slice(0, 500); cursor = terms.length && view.length ? 0 : -1;
      $('count').textContent = all.length ? [matches.length === all.length ? rendered(await dynamicText('console.mail.count.all', fmt(all.length)), renderPass) : rendered(await dynamicText('console.mail.count.matched', fmt(matches.length), fmt(all.length)), renderPass),
        matches.length > view.length ? rendered(await dynamicText('console.mail.count.showing', fmt(view.length)), renderPass) : ''].filter(Boolean).join(' · ') : '';
      const term = terms.reduce((a, b) => b.length > a.length ? b : a, ''), picked = chosen && keyOf(chosen);
      $('items').innerHTML = rendered(await Promise.all(view.map(async (x, i) => {
        const sub = await subline(x);
        return `<li role="option" id="opt-${i}" data-i="${i}" class="item${keyOf(x) === picked ? ' chosen' : ''}" aria-selected="${keyOf(x) === picked}"><span class="n">${highlight(x.name, term)}</span><span class="c">${esc(catName(x))}</span><span class="i"><i>${x.element_type}·</i>${highlight(String(x.id), term)}</span>${sub ? `<span class="d" title="${esc(await details(x) || sub)}">${highlight(sub, term)}</span>` : ''}</li>`;
      })), renderPass).join('') || `<li class="empty">${esc(t('console.mail.items.none'))}${category ? `<br><small>${esc(rendered(await dynamicText('console.mail.items.none_in_category', categoryName(category)), renderPass))}</small>` : ''}</li>`;
      paintCursor(true); renderRecent(!terms.length);

    } catch (error) { if (error !== staleRender) throw error; }
  }
  function paintCursor(scroll) {
    const old = $('items').querySelector('.active'); if (old) old.classList.remove('active');
    const row = $('opt-' + cursor);
    if (row) { row.classList.add('active'); if (scroll) row.scrollIntoView({block: 'nearest'}); }
    $('q').setAttribute('aria-activedescendant', row ? row.id : '');
  }
  $('items').onmousemove = event => { const row = event.target.closest('[data-i]'); if (row && +row.dataset.i !== cursor) { cursor = +row.dataset.i; paintCursor(false); } };
  $('items').onclick = async event => { const row = event.target.closest('[data-i]'); if (row) await pick(view[+row.dataset.i]); };
  $('q').oninput = render;
  $('q').onkeydown = async event => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); if (!view.length) return; cursor = (cursor + (event.key === 'ArrowDown' ? 1 : -1) + view.length) % view.length; paintCursor(true); }
    else if (event.key === 'Enter') { event.preventDefault(); const x = view[Math.max(cursor, 0)]; if (x) await pick(x); }
    else if (event.key === 'Escape') { event.preventDefault(); if ($('q').value) { $('q').value = ''; await render(); } else $('q').blur(); }
  };
  const recentKey = 'bd2-console-recent';
  function recentList() { try { return JSON.parse(localStorage.getItem(recentKey)) || []; } catch { return []; } }
  function remember(x) { try { localStorage.setItem(recentKey, JSON.stringify([keyOf(x), ...recentList().filter(k => k !== keyOf(x))].slice(0, 8))); } catch {} }
  function renderRecent(visible) {
    const items = recentList().map(k => byKey.get(k)).filter(Boolean), box = $('recent');
    box.hidden = !visible || !items.length; if (box.hidden) return;
    box.replaceChildren(box.firstElementChild, ...items.map(x => el('button', {type: 'button', title: catName(x) + ' · ' + keyOf(x), onclick: async () => await pick(x)}, x.name)));
  }
  async function pick(x) {
    chosen = x; await paintParcel();
    $('items').querySelectorAll('.item').forEach(row => { const on = keyOf(view[+row.dataset.i]) === keyOf(x); row.classList.toggle('chosen', on); row.setAttribute('aria-selected', on); });
    await say($('status'), ''); $('quantity').focus(); $('quantity').select();
  }
  async function unpick() { chosen = null; await paintParcel(); $('items').querySelectorAll('.chosen').forEach(row => { row.classList.remove('chosen'); row.setAttribute('aria-selected', false); }); $('q').focus(); }
  async function paintParcel() {
    const renderPass = beginRender('mail-parcel');
    try {
      const parcel = $('parcel'); parcel.classList.toggle('empty', !chosen);
      if (!chosen) {
        parcel.replaceChildren(el('p', {}, t('console.mail.parcel.empty'), el('br'), el('span', {}, ...rendered(await rich('console.mail.parcel.search_hint', el('kbd', {}, '/')), renderPass))));
        parcel.onclick = () => $('q').focus(); return;
      }
      parcel.onclick = null;
      const d = rendered(await details(chosen), renderPass);
      parcel.replaceChildren(el('div', {class: 'p-name'}, chosen.name),
        el('div', {class: 'p-meta'}, el('span', {}, catName(chosen)), el('code', {}, rendered(await dynamicText('console.mail.parcel.meta', chosen.element_type, chosen.id), renderPass))),
        d ? el('p', {class: 'p-detail'}, d) : null,
        el('button', {type: 'button', class: 'x', 'aria-label': t('console.mail.parcel.remove'), title: t('console.mail.parcel.remove'), onclick: unpick}, '×'));

    } catch (error) { if (error !== staleRender) throw error; }
  }
  // Quantity suffixes: Latin k/w/m and the CJK numerals for thousand, ten thousand, million and hundred million.
  const units = {k: 1e3, w: 1e4, m: 1e6, '\u5343': 1e3, '\ucc9c': 1e3, '\u4e07': 1e4, '\u842c': 1e4, '\ub9cc': 1e4, '\u767e\u4e07': 1e6, '\u767e\u842c': 1e6, '\ubc31\ub9cc': 1e6, '\u4ebf': 1e8, '\u5104': 1e8, '\uc5b5': 1e8};
  const unitPattern = new RegExp('^(\\d+(?:\\.\\d+)?)(' + Object.keys(units).sort((a, b) => b.length - a.length).join('|') + ')?$');
  function quantity() { const m = $('quantity').value.replace(/[\s,\uff0c_]/g, '').toLowerCase().match(unitPattern); return m ? Math.round(parseFloat(m[1]) * (units[m[2]] || 1)) : NaN; }
  async function checkQuantity() {
    const n = quantity(), input = $('quantity'), hint = $('qty-hint'), typedValue = input.value, language = locale; let problem = '';
    delete hint.dataset.i18n;
    if (!Number.isInteger(n) || n < 1) problem = t('console.mail.quantity.positive'); else if (n > 2147483647) problem = await dynamicText('console.mail.quantity.too_large', fmt(2147483647));
    if (input.value !== typedValue || locale !== language) return await checkQuantity();
    input.setCustomValidity(problem);
    const typed = input.value.trim() !== '';
    hint.classList.toggle('err', !!problem && typed);
    hint.textContent = problem && typed ? problem : !problem && String(n) !== input.value.trim() ? '= ' + fmt(n) : t('console.mail.quantity.hint');
    return problem ? NaN : n;
  }
  $('quantity').oninput = checkQuantity;
  $('quantity').onkeydown = async event => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
    event.preventDefault(); const n = quantity() || 0, step = event.shiftKey ? 10 : 1;
    $('quantity').value = Math.max(1, n + (event.key === 'ArrowUp' ? step : -step)); await checkQuantity();
  };
  $('presets').onclick = async event => { const b = event.target.closest('[data-q]'); if (!b) return; const add = +b.dataset.q; $('quantity').value = event.shiftKey ? (quantity() || 0) + add : add; await checkQuantity(); $('quantity').focus(); };
  const paintPeek = () => { $('letter-peek').textContent = $('title').value || t('console.mail.letter.untitled'); };
  $('title').oninput = paintPeek;
  document.addEventListener('keydown', event => {
    if ($('ask').open || currentView !== 'mail' || $('tools').disabled) return;
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
    if (event.key === '/' && !typing && !event.ctrlKey && !event.metaKey && !event.altKey) { event.preventDefault(); $('q').focus(); $('q').select(); }
    else if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); $('form').requestSubmit(); }
  });
  async function logEntry(entry) {
    return el('li', {},
      el('span', {class: 'what'}, el('b', {}, entry.item.name), ' × ' + fmt(entry.count)),
      el('span', {class: 'meta'}, await dynamicText('console.mail.log.meta', entry.time.toLocaleTimeString(tag(), {hour: '2-digit', minute: '2-digit'}), playerLabel(entry.player))),
      el('button', {type: 'button', class: 'again', title: t('console.mail.log.again_title'), onclick: async () => { await pick(entry.item); $('quantity').value = entry.count; await checkQuantity(); $('quantity').select(); }}, t('console.mail.log.again')),
      el('button', {type: 'button', class: 'ident', title: t('console.mail.log.identity_title'), onclick: async () => await copy(entry.identity, t('console.mail.log.identity'))}, entry.identity));
  }
  async function paintLog() {
    const renderPass = beginRender('mail-log');
    try {
      $('log').replaceChildren(...rendered(await Promise.all(log.map(logEntry)), renderPass).reverse());
      $('log-empty').hidden = !!log.length; $('log-count').textContent = log.length ? rendered(await dynamicText('console.mail.log.count', fmt(log.length)), renderPass) : '';

    } catch (error) { if (error !== staleRender) throw error; }
  }
  function showRecipient(player) {
    const display = $('uid-display');
    display.textContent = player ? player.uid || '——' : '——'; display.classList.toggle('none', !player);
    $('slip-uid').textContent = player ? player.uid : '—';
    $('recipient-note').textContent = player ? (player.name || '') : t('console.mail.recipient.none');
  }
  $('uid-display').onclick = async () => { if (target && target.uid) await copy(target.uid, 'UID'); };
  function showInventory(value) {
    inventory = value;
    const on = value.unlimited, l = value.limits;
    $('unlimited-inventory').checked = on;
    $('unlimited-inventory').disabled = !can('players.inventory');
    $('unlimited-inventory').closest('label').title = can('players.inventory') ? '' : t('console.mail.inventory.no_permission');
    $('inventory-limits').replaceChildren(el('span'), el('span', {class: 'h'}, t('console.mail.inventory.items')), el('span', {class: 'h'}, t('console.mail.inventory.equipment')),
      el('span', {class: 'lab' + (on ? '' : ' on')}, t('console.mail.inventory.normal')), el('b', {}, fmt(l.items.default)), el('b', {}, fmt(l.equipment.default)),
      el('span', {class: 'lab' + (on ? ' on' : '')}, t('console.mail.inventory.unlimited')), el('b', {}, fmt(l.items.maximum)), el('b', {}, fmt(l.equipment.maximum)));
  }
  async function select(accountID) {
    target = state.players.find(p => p.account_id === accountID) || null;
    showRecipient(target); $('tools').disabled = true; await say($('status'), ''); await say($('inventory-status'), ''); waitingCatalog = false;
    await note($('lock-note'), Component.translatable(target ? 'console.mail.lock.loading_player' : 'console.mail.lock.no_player'));
    if (!target) return;
    try {
      const requested = target.account_id;
      const {value} = await api('players/' + encodeURIComponent(requested) + '/inventory');
      if (target && target.account_id === requested) {
        showInventory(value); $('tools').disabled = !loaded; waitingCatalog = !loaded;
        await note($('lock-note'), loaded ? '' : Component.translatable('console.mail.catalog_loading'));
      }
    } catch (error) { await note($('lock-note'), error); }
  }
  $('player').onchange = async () => await select($('player').value);
  $('form').onsubmit = async event => {
    event.preventDefault(); if (submitting || !target) return;
    if (!chosen) { await say($('status'), Component.translatable('console.mail.pick_first'), 'err'); const parcel = $('parcel'); parcel.classList.remove('shake'); void parcel.offsetWidth; parcel.classList.add('shake'); $('q').focus(); return; }
    const count = await checkQuantity();
    if (!$('title').checkValidity() || !$('body').checkValidity()) $('letter').open = true;
    if (!$('form').reportValidity()) return;
    submitting = true; const send = $('grant-submit'), label = send.firstElementChild, player = target, item = chosen; send.disabled = true; $('player').disabled = true;
    try {
      const {value} = await api('players/' + encodeURIComponent(player.account_id) + '/mail', {method: 'POST', body: {element_type: item.element_type, id: item.id, count, title: $('title').value, body: $('body').value}});
      await say($('status'), Component.translatable('console.mail.sent', String(player.uid)), 'ok');
      log.push({item, count, player, identity: value.identity, time: new Date()}); await paintLog(); remember(item);
      send.classList.add('done'); label.textContent = t('console.mail.send.done');
      setTimeout(() => { send.classList.remove('done'); label.textContent = t('console.mail.send'); }, 1400);
    } catch (error) { await say($('status'), error, 'err'); }
    finally { submitting = false; send.disabled = false; $('player').disabled = false; }
  };
  $('unlimited-inventory').onchange = async event => {
    const box = event.target, old = !box.checked; box.disabled = true; $('player').disabled = true;
    try {
      const {value} = await api('players/' + encodeURIComponent(target.account_id) + '/inventory', {method: 'PUT', body: {unlimited: box.checked}});
      showInventory(value); target.unlimited_inventory = value.unlimited;
      await say($('inventory-status'), Component.translatable('console.mail.inventory.saved', String(target.uid)), 'ok');
    } catch (error) { box.checked = old; await say($('inventory-status'), error, 'err'); }
    finally { box.disabled = !can('players.inventory'); $('player').disabled = false; }
  };
  async function loadCatalog() {
    try {
      all = (await api('catalog')).value.items;
      all.forEach(x => { x._h = searchText(x); });
      byKey = new Map(all.map(x => [keyOf(x), x])); buildCategories(); await render(); loaded = true;
      if (target && waitingCatalog) { waitingCatalog = false; await note($('lock-note'), ''); $('tools').disabled = false; }
    } catch (error) { $('items').replaceChildren(el('li', {class: 'empty'}, error.message)); }
  }
  async function relabelCatalog() {
    const renderPass = beginRender('mail-catalog');
    try { all.forEach(x => { x._h = searchText(x); }); buildCategories(); rendered(await render(), renderPass);
    } catch (error) { if (error !== staleRender) throw error; }
  }
  async function playerOptions() {
    const usable = state.players.filter(p => p.active);
    $('player-selector').hidden = usable.length < 2;
    const selected = $('player').value, language = locale;
    const options = await Promise.all(usable.map(async p => new Option(p.owner ? await dynamicText('console.account.as_owner', playerLabel(p)) : playerLabel(p), p.account_id)));
    if (locale !== language) return await playerOptions();
    $('player').replaceChildren(...options);
    if (selected) $('player').value = selected;
    return usable;
  }
  return {
    async open(accountID) {
      await paintParcel(); paintPeek(); await paintLog();
      const usable = await playerOptions();
      const self = state.session.player && state.session.player.account_id;
      const initial = accountID || (target && usable.some(p => p.account_id === target.account_id) ? target.account_id : null) || (usable.some(p => p.account_id === self) ? self : usable[0] && usable[0].account_id);
      if (initial) $('player').value = initial;
      if (loaded) await relabelCatalog(); else if (!all.length) await loadCatalog();
      await checkQuantity(); await select(initial || '');
    },
    async relabel() {
      await playerOptions(); showRecipient(target); await paintParcel(); paintPeek(); await paintLog(); await checkQuantity();
      if (inventory) showInventory(inventory);
      if (loaded) await relabelCatalog();
    },
  };
})();

/* ---------- Players ---------- */
const permissionLabel = (key, fallback) => tr('console.permission.' + key + '.label', fallback ?? key);
const roster = (() => {
  let catalog = null;
  const toggle = (checked, disabled, onchange, label) => el('label', {class: 'switch', title: label}, el('input', {type: 'checkbox', role: 'switch', checked, disabled, 'aria-label': label, onchange}), el('span', {class: 'track', 'aria-hidden': 'true'}));
  async function summary(p) {
    if (!p.administrator) return '';
    const keys = p.permissions || [];
    const basic = keys.filter(k => !k.startsWith('config.')).map(k => tr('console.permission.' + k + '.short', permissionLabel(k)));
    const sections = keys.filter(k => k.startsWith('config.') && k.endsWith('.read')).length;
    const writes = keys.filter(k => k.startsWith('config.') && k.endsWith('.write')).length;
    const configuration = sections ? writes ? await dynamicText('console.roster.summary.config_write', sections, writes) : await dynamicText('console.roster.summary.config_read', sections) : '';
    return [...basic, configuration].filter(Boolean).join(' · ') || t('console.roster.summary.none');
  }
  async function render() {
    const renderPass = beginRender('players');
    try {
      const owner = isOwner();
      $('roster-head-row').replaceChildren(...['console.roster.col.uid', 'console.roster.col.name', 'console.roster.col.role', owner ? 'console.roster.col.admin' : null, 'console.roster.col.unlimited', '']
        .filter(h => h !== null).map((h, i) => el('th', {class: i >= 3 && h ? 'c' : ''}, h ? t(h) : '')));
      const q = $('player-q').value.trim().toLowerCase();
      const rows = state.players.filter(p => !q || (p.uid + ' ' + p.name).toLowerCase().includes(q));
      $('roster').replaceChildren(...rendered(await Promise.all(rows.map(async p => {
        const adminCell = owner ? el('td', {class: 'c'}, p.owner ? el('span', {class: 'muted'}, '—') : toggle(p.administrator, !p.active || state.session.mode !== 'oauth', async event => await setAdministrator(p, event.target), t('console.role.admin'))) : null;
        return el('tr', {class: p.active ? '' : 'inactive'},
          el('td', {class: 'uid-cell'}, p.uid || '—'),
          el('td', {}, p.name || ''),
          el('td', {}, p.owner ? el('span', {class: 'pill owner'}, t('console.role.owner')) : null,
            p.administrator ? el('span', {class: 'pill member', title: owner ? await summary(p) : ''}, t('console.role.admin')) : null,
            p.active ? null : el('span', {class: 'pill'}, t('console.roster.inactive')),
            owner && p.administrator ? el('small', {class: 'perm-summary'}, await summary(p)) : null),
          adminCell,
          el('td', {class: 'c'}, toggle(p.unlimited_inventory, !p.active || !can('players.inventory'), async event => await setInventory(p, event.target), t('console.inventory.unlimited'))),
          el('td', {class: 'act'},
            owner && p.administrator ? el('button', {type: 'button', class: 'linkish', onclick: async () => await editPermissions(p)}, t('console.roster.permissions')) : null,
            p.active && can('mail.send') ? el('button', {type: 'button', class: 'linkish', onclick: () => { location.hash = '#mail'; setTimeout(async () => await mailTool.open(p.account_id)); }}, t('console.roster.mail')) : null));
      })), renderPass));
      if (!rows.length) $('roster').replaceChildren(el('tr', {}, el('td', {colspan: 6, class: 'muted'}, t(state.players.length ? 'console.roster.no_match' : 'console.roster.empty'))));

    } catch (error) { if (error !== staleRender) throw error; }
  }
  async function setAdministrator(player, box) {
    if (!box.checked) {
      if (!await ask({title: t('console.roster.revoke.title'), body: await dynamicText('console.roster.revoke.body', player.uid), ok: t('console.roster.revoke.ok'), danger: true})) { box.checked = true; return; }
    }
    box.disabled = true;
    try {
      const {value} = await api('players/' + encodeURIComponent(player.account_id) + '/administrator', {method: 'PUT', body: {administrator: box.checked}});
      player.administrator = value.administrator; player.permissions = value.permissions || [];
      await render();
      await toast(Component.translatable(box.checked ? 'console.roster.appointed' : 'console.roster.revoked', String(player.uid)));
      if (box.checked) await editPermissions(player);
    } catch (error) { box.checked = !box.checked; await say($('roster-status'), error, 'err'); }
    finally { box.disabled = false; }
  }
  async function editPermissions(player) {
    try { catalog = catalog || (await api('permissions')).value; } catch (error) { await toast(error); return; }
    const granted = new Set(player.permissions || []);
    const editor = el('div', {class: 'perm-editor'});
    const draw = async () => {
    const renderPass = beginRender(editor);
    try {
        const check = (key, label, help, onchange) => el('label', {class: 'perm'}, el('input', {type: 'checkbox', checked: granted.has(key), onchange}), el('span', {}, label, help ? el('small', {}, help) : null));
        const abilities = catalog.abilities.map(a => check(a.key, permissionLabel(a.key, a.label), tr('console.permission.' + a.key + '.help', a.help), async e => {
          if (e.target.checked) { granted.add(a.key); (a.requires || []).forEach(r => granted.add(r)); }
          else { granted.delete(a.key); catalog.abilities.filter(b => (b.requires || []).includes(a.key)).forEach(b => granted.delete(b.key)); }
          await draw();
        }));
        const documents = rendered(await Promise.all(catalog.configuration.map(async doc => {
          const key = (section, mode) => `config.${doc.document}.${section}.${mode}`;
          const docTitle = tr(`config.${doc.document}.title`, doc.title);
          const sectionTitle = s => tr(`config.${doc.document}.section.${s.id}.title`, s.title);
          const all = mode => doc.sections.every(s => granted.has(key(s.id, mode)));
          const setAll = async mode => { const on = !all(mode); doc.sections.forEach(s => { if (on) { granted.add(key(s.id, mode)); if (mode === 'write') granted.add(key(s.id, 'read')); } else { granted.delete(key(s.id, mode)); if (mode === 'read') granted.delete(key(s.id, 'write')); } }); await draw(); };
          return el('table', {class: 'perm-table'},
            el('thead', {}, el('tr', {}, el('th', {}, docTitle),
              el('th', {class: 'c'}, el('button', {type: 'button', class: 'linkish', title: t('console.permissions.toggle_all'), onclick: async () => await setAll('read')}, t('console.permissions.read'))),
              el('th', {class: 'c'}, el('button', {type: 'button', class: 'linkish', title: t('console.permissions.toggle_all'), onclick: async () => await setAll('write')}, t('console.permissions.write'))))),
            el('tbody', {}, await Promise.all(doc.sections.map(async s => el('tr', {}, el('td', {}, sectionTitle(s)),
              el('td', {class: 'c'}, el('input', {type: 'checkbox', 'aria-label': await dynamicText('console.permissions.read_aria', docTitle, sectionTitle(s)), checked: granted.has(key(s.id, 'read')), onchange: async e => { if (e.target.checked) granted.add(key(s.id, 'read')); else { granted.delete(key(s.id, 'read')); granted.delete(key(s.id, 'write')); } await draw(); }})),
              el('td', {class: 'c'}, el('input', {type: 'checkbox', 'aria-label': await dynamicText('console.permissions.write_aria', docTitle, sectionTitle(s)), checked: granted.has(key(s.id, 'write')), onchange: async e => { if (e.target.checked) { granted.add(key(s.id, 'write')); granted.add(key(s.id, 'read')); } else granted.delete(key(s.id, 'write')); await draw(); }})))))));
        })), renderPass);
        editor.replaceChildren(el('h4', {}, t('console.permissions.abilities')), el('div', {class: 'perm-list'}, abilities), el('h4', {}, t('console.permissions.configuration')), el('p', {class: 'muted'}, t('console.permissions.configuration_help')), ...documents,
          el('p', {class: 'muted'}, t('console.permissions.owner_only')));

    } catch (error) { if (error !== staleRender) throw error; }
  };
    await draw();
    if (!await ask({title: await dynamicText('console.permissions.title', player.uid), body: player.name || '', extra: editor, ok: t('console.permissions.save')})) return;
    try {
      const {value} = await api('players/' + encodeURIComponent(player.account_id) + '/administrator', {method: 'PUT', body: {administrator: true, permissions: [...granted]}});
      player.permissions = value.permissions; await render(); await toast(Component.translatable('console.roster.permissions_updated', String(player.uid)));
    } catch (error) { await say($('roster-status'), error, 'err'); }
  }
  async function setInventory(player, box) {
    box.disabled = true;
    try {
      const {value} = await api('players/' + encodeURIComponent(player.account_id) + '/inventory', {method: 'PUT', body: {unlimited: box.checked}});
      player.unlimited_inventory = value.unlimited; await toast(Component.translatable(value.unlimited ? 'console.roster.unlimited_on' : 'console.roster.unlimited_off', String(player.uid)));
    } catch (error) { box.checked = !box.checked; await say($('roster-status'), error, 'err'); }
    finally { box.disabled = false; }
  }
  $('player-q').oninput = render;
  return {async open() { await say($('roster-status'), ''); await loadPlayers(); await render(); }, relabel: render};
})();

/* ---------- Server configuration (generic editor) ---------- */
const config = (() => {
  let docs = [], current = '', drafts = {}, checks = {}, checkTimer, server = null;
  const doc = () => docs.find(d => d.name === current);
  const fields = d => d.schema.sections.flatMap(s => s.fields);
  const dirtyDoc = d => drafts[d.name] !== undefined && !same(drafts[d.name], d.view.value);
  // Schema text comes from the server in one language; translations are keyed by its structure.
  const valueKey = value => typeof value === 'string' ? value : JSON.stringify(value);
  const docTitle = d => tr(`config.${d.name}.title`, d.schema.title);
  const docSummary = d => tr(`config.${d.name}.summary`, d.schema.summary);
  const sectionText = (d, s, part) => tr(`config.${d.name}.section.${s.id}.${part}`, s[part]);
  const fieldText = (d, f, part) => tr(`config.${d.name}.field.${f.path}.${part}`, f[part]);
  const optionText = (d, f, o, part, kind = 'option') => tr(`config.${d.name}.field.${f.path}.${kind}.${valueKey(o.value)}.${part}`, o[part]);
  const lockReason = (d, lock) => tr(`config.${d.name}.lock.${lock.path}`, lock.reason);
  function getPath(object, path) { return path.split('.').reduce((value, key) => value == null ? undefined : value[key], object); }
  function setPath(object, path, value) {
    const keys = path.split('.'); let node = object;
    keys.slice(0, -1).forEach(key => { if (node[key] == null || typeof node[key] !== 'object') node[key] = {}; node = node[key]; });
    if (value === undefined) delete node[keys.at(-1)]; else node[keys.at(-1)] = value;
  }
  function holds(when, value) {
    if (!when) return true;
    const found = getPath(value, when.path);
    return when.in.some(candidate => same(found, candidate)) !== !!when.not;
  }
  // Maps a problem path to its field: list indexes are dropped and * matches map keys.
  function fieldFor(d, path) {
    const bare = path.replace(/\[\d+\]/g, '');
    const parts = bare.split('.');
    return fields(d).find(f => {
      const pattern = f.path.split('.');
      return pattern.length === parts.length && pattern.every((p, i) => p === '*' || p === parts[i]);
    }) || fields(d).filter(f => bare.startsWith(f.path + '.') || bare === f.path).sort((a, b) => b.path.length - a.path.length)[0];
  }
  // Known server validation messages with a dedicated explanation.
  const messages = {
    'must be a bare host name without scheme, path or port': 'bare_host',
    'must be host:port': 'host_port',
    'plaintext realtime listeners must bind loopback': 'plaintext_listen_loopback',
    'plaintext realtime public host must be loopback': 'plaintext_host_loopback',
    'is required unless plaintext loopback development is enabled': 'tls_required',
    'must be a nonempty path': 'nonempty_path',
    'is required by an active provider': 'required_by_provider',
    'is required by the Azure provider': 'required_by_azure',
    'must be a lowercase identifier': 'lowercase_identifier',
    'local mode must not configure OAuth': 'local_no_oauth',
    'is required in oauth mode': 'required_oauth',
    'oauth mode requires at least one provider': 'oauth_provider',
    'must be an absolute origin without path, query, fragment, or user info': 'absolute_origin',
    'must use HTTPS except on localhost': 'https_origin',
    'must be a positive Go duration': 'positive_duration',
    'official mode must use the versioned official resource origin': 'official_origin',
    'is required in server mode': 'required_server_mode',
    'must be an absolute HTTP(S) URL without credentials, query, or fragment': 'absolute_url',
    'must not have a trailing slash': 'no_trailing_slash',
    'must use HTTPS outside loopback development': 'https_url',
    'must be a list': 'list',
    'must be an object': 'object',
    'must be present': 'present',
    'a section outside your access is invalid': 'hidden_section',
  };
  async function describe(problem, field, d) {
    const m = problem.message || '';
    const item = (problem.path.match(/\[(\d+)\]$/) || [])[1];
    let text;
    if (messages[m]) text = t('config.problem.message.' + messages[m]);
    else if (problem.rule === 'enum' && field && field.options) text = await dynamicText('config.problem.enum_options', listJoin(field.options.map(o => optionText(d, field, o, 'label'))));
    else if (problem.rule === 'enum' && field && field.keys) text = t('config.problem.enum_key');
    else if (problem.rule === 'range' && field && field.min != null && field.max != null) text = await dynamicText('config.problem.range_between', fmt(field.min), fmt(field.max));
    else if (problem.rule === 'conflict' && m.startsWith('is fixed to')) { const lock = (d.view.locks || []).find(l => l.path === problem.path); text = lock ? await dynamicText('config.problem.locked_reason', lockReason(d, lock)) : t('config.problem.locked'); }
    else if (problem.rule === 'environment') { const env = (m.match(/environment variable (\S+)/) || [])[1] || ''; text = await dynamicText(m.includes('32-byte') ? 'config.problem.environment_key' : 'config.problem.environment', env); }
    else if (problem.rule === 'missing_file') text = await dynamicText('config.problem.missing_file', m.replace(/ is not readable by this server process$/, ''));
    else if (['decode', 'format', 'custom'].includes(problem.rule)) text = await dynamicText('config.problem.' + problem.rule, m);
    else if (lookup('config.problem.' + problem.rule) !== undefined) text = t('config.problem.' + problem.rule);
    else text = m;
    return item !== undefined ? await dynamicText('config.problem.item', +item + 1, text) : text;
  }
  async function render() {
    const renderPass = beginRender('config-form');
    try {
      const d = doc(); if (!d) return;
      const draft = drafts[d.name], readonly = !d.view.writable;
      renderNav();
      const form = $('config-form');
      const box = el('fieldset', {disabled: readonly});
      const summary = docSummary(d);
      box.append(el('section', {class: 'card doc-head'}, el('h2', {}, docTitle(d)), summary ? el('p', {}, summary) : null,
        el('div', {class: 'path'}, el('span', {}, d.view.path), d.view.modified ? el('span', {}, rendered(await dynamicText('console.config.modified', dateTime(d.view.modified)), renderPass)) : null)));
      for (const section of d.schema.sections) {
        const own = section.fields.filter(f => !f.path.includes('*'));
        if (own.every(f => f.advanced && f.path === 'schema_version')) continue;
        const help = sectionText(d, section, 'help');
        const card = el('fieldset', {class: 'card section', id: 'sec-' + section.id, disabled: section.read_only}, el('h3', {}, sectionText(d, section, 'title'), section.read_only ? el('span', {class: 'pill'}, t('console.config.read_only')) : null), help ? el('p', {class: 'help'}, help) : null);
        const basic = own.filter(f => !f.advanced), advanced = own.filter(f => f.advanced);
        card.append(...rendered(await Promise.all(basic.map(async f => await renderField(d, f, draft))), renderPass));
        if (advanced.length) {
          const details = el('details', {class: 'advanced'}, el('summary', {}, t('console.config.advanced')));
          details.append(...rendered(await Promise.all(advanced.map(async f => await renderField(d, f, draft))), renderPass));
          card.append(details);
        }
        box.append(card);
      }
      form.replaceChildren(box);
      rendered(await refresh(), renderPass);

    } catch (error) { if (error !== staleRender) throw error; }
  }
  async function renderField(d, field, draft) {
    const value = getPath(draft, field.path);
    const lock = (d.view.locks || []).find(l => l.path === field.path);
    const wrap = el('div', {class: 'cfield', 'data-path': field.path});
    wrap.append(el('div', {class: 'label'}, fieldText(d, field, 'label'), el('small', {}, field.path)));
    const control = el('div', {class: 'control'});
    const update = async next => { setPath(drafts[d.name], field.path, next); await refresh(); };
    control.append(await widget(field, value, update, lock, d));
    const help = fieldText(d, field, 'help');
    if (help) control.append(el('p', {class: 'help'}, help));
    control.append(el('div', {class: 'msgs'}));
    if (lock) control.append(el('p', {class: 'msg lock'}, await dynamicText('console.config.locked', lockReason(d, lock))));
    wrap.append(control);
    return wrap;
  }
  async function widget(field, value, update, lock, d) {
    const disabled = !!lock;
    const placeholder = fieldText(d, field, 'placeholder');
    if (field.kind === 'bool') {
      return el('label', {class: 'switch'}, el('input', {type: 'checkbox', role: 'switch', checked: !!value, disabled, onchange: async e => await update(e.target.checked)}), el('span', {class: 'track', 'aria-hidden': 'true'}), el('span', {class: 'switch-state'}, t(value ? 'console.config.on' : 'console.config.off')));
    }
    if (field.options && field.kind !== 'list' && !field.open_options) {
      const options = field.options.filter(o => o.value !== '' || value === '');
      const name = 'opt-' + field.path;
      const help = el('p', {class: 'option-help'});
      const showHelp = v => { const o = field.options.find(x => same(x.value, v)); help.textContent = o ? optionText(d, field, o, 'help') : ''; };
      showHelp(value);
      const group = el('div', {class: 'segmented', role: 'radiogroup', 'aria-label': fieldText(d, field, 'label')}, options.map(o => el('label', {},
        el('input', {type: 'radio', name, checked: same(o.value, value), disabled, onchange: async () => { await update(clone(o.value)); showHelp(o.value); }}), el('span', {}, optionText(d, field, o, 'label')))));
      const holder = el('div', {}, group, help);
      const extra = extras[field.path]; if (extra) holder.append(extra(d));
      return holder;
    }
    if (field.kind === 'integer') {
      const input = el('input', {inputmode: 'numeric', value: value ?? '', disabled, placeholder,
        oninput: async e => { const raw = e.target.value.trim().replace(/[,\uff0c_\s]/g, ''); await update(/^-?\d+$/.test(raw) ? Number(raw) : raw); }});
      return el('div', {class: 'unit-input'}, input, field.min != null ? el('span', {}, `${fmt(field.min)} – ${fmt(field.max)}`) : null);
    }
    if (field.kind === 'string') {
      const mono = ['path', 'env', 'url', 'listen', 'host', 'origin', 'duration', 'identifier'].includes(field.format);
      return el('div', {class: 'text-input' + (mono ? ' mono' : '')}, el('input', {value: value ?? '', disabled, placeholder, spellcheck: 'false', autocomplete: 'off', oninput: async e => await update(e.target.value)}));
    }
    if (field.kind === 'list' && field.options) return await chipList(field, value || [], update, disabled, d);
    if (field.kind === 'list') return rowList(field, value || [], update, disabled, placeholder);
    if (field.kind === 'map') return mapEditor(field, value || {}, update, disabled, d);
    return el('code', {}, JSON.stringify(value));
  }
  async function chipList(field, list, update, disabled, d) {
    const holder = el('div', {class: 'chips'});
    const draw = async () => {
    const renderPass = beginRender(holder);
    try {
        const custom = list.filter(v => !field.options.some(o => o.value === v));
        holder.replaceChildren(...field.options.map(o => el('button', {type: 'button', class: 'chip', 'aria-pressed': String(list.includes(o.value)), disabled, title: o.value,
          onclick: async () => { list = list.includes(o.value) ? list.filter(v => v !== o.value) : [...list, o.value]; await update(list); await draw(); }}, optionText(d, field, o, 'label'))),
          ...custom.map(v => el('button', {type: 'button', class: 'chip', 'aria-pressed': 'true', disabled, title: t('console.config.click_remove'), onclick: async () => { list = list.filter(x => x !== v); await update(list); await draw(); }}, v + ' ×')),
          field.open_options && !disabled ? el('input', {class: 'chip-add', placeholder: t('console.config.add_other'), spellcheck: 'false',
            onkeydown: async e => { if (e.key !== 'Enter') return; e.preventDefault(); const v = e.target.value.trim(); if (v && !list.includes(v)) { list = [...list, v]; await update(list); await draw(); } }}) : null,
          el('span', {class: 'muted'}, rendered(await dynamicText('console.config.selected', fmt(list.length)), renderPass)),
          !disabled ? el('button', {type: 'button', class: 'linkish', onclick: async () => { list = field.options.map(o => o.value); await update(list); await draw(); }}, t('console.config.select_all')) : null);

    } catch (error) { if (error !== staleRender) throw error; }
  };
    await draw();
    return holder;
  }
  function rowList(field, list, update, disabled, placeholder) {
    const holder = el('div', {class: 'rows'});
    const draw = () => {
      holder.replaceChildren(...list.map((v, i) => el('div', {class: 'row', 'data-item': i},
        el('input', {value: v, disabled, spellcheck: 'false', placeholder, oninput: async e => { list[i] = e.target.value; await update([...list]); }}),
        disabled ? null : el('button', {type: 'button', class: 'icon-btn', title: t('console.config.remove'), 'aria-label': t('console.config.remove'), onclick: async () => { list.splice(i, 1); await update([...list]); draw(); }}, '×'))),
        disabled ? null : el('button', {type: 'button', class: 'add', onclick: async () => { list.push(''); await update([...list]); draw(); holder.querySelector('.row:last-of-type input').focus(); }}, t('console.config.add')),
        !list.length ? el('span', {class: 'muted'}, t('console.config.empty')) : null);
    };
    draw();
    return holder;
  }
  function mapEditor(field, map, update, disabled, d) {
    const children = fields(d).filter(f => f.path.startsWith(field.path + '.*.'));
    const holder = el('div', {class: 'map-keys'});
    const draw = () => {
      holder.replaceChildren(...(field.keys || Object.keys(map).map(k => ({value: k, label: k}))).map(k => {
        const enabled = Object.hasOwn(map, k.value);
        const entry = el('div', {class: 'map-entry', 'data-key': k.value},
          el('label', {class: 'switch'}, el('input', {type: 'checkbox', role: 'switch', checked: enabled, disabled, onchange: async e => {
            // Re-enabling restores the value saved in the file so an accidental toggle loses nothing.
            if (e.target.checked) map[k.value] = clone(getPath(d.view.value, field.path + '.' + k.value)) || Object.fromEntries(children.map(c => [c.path.split('.').at(-1), '']));
            else delete map[k.value];
            await update({...map}); draw(); }}), el('span', {class: 'track', 'aria-hidden': 'true'}), field.keys ? optionText(d, field, k, 'label', 'key') : k.label));
        if (enabled) {
          const sub = el('div', {class: 'sub'});
          children.forEach(c => {
            const name = c.path.split('.').at(-1), path = field.path + '.' + k.value + '.' + name;
            sub.append(el('label', {}, fieldText(d, c, 'label')), el('div', {class: 'text-input mono', 'data-path': path}, el('input', {value: map[k.value][name] ?? '', disabled, spellcheck: 'false', autocomplete: 'off', placeholder: fieldText(d, c, 'placeholder'),
              oninput: async e => { map[k.value][name] = e.target.value; await update({...map}); }})), el('div', {class: 'msgs', 'data-msgs': path}));
          });
          entry.append(sub);
        }
        return entry;
      }));
    };
    draw();
    return holder;
  }
  // Field-specific hints that show a worked example from the current draft.
  const extras = {
    'purchases.diamond_recharge.currency': () => {
      const box = el('div', {class: 'example'});
      box.dataset.example = 'recharge';
      return box;
    },
  };
  async function paintExamples() {
    const renderPass = beginRender('config-example');
    try {
      const box = document.querySelector('[data-example=recharge]'); if (!box) return;
      const r = (drafts.game || {}).purchases?.diamond_recharge || {};
      const n = 100;
      const parts = r.currency === 'gold' ? rendered(await rich('console.config.example.gold', fmt(n), el('b', {}, fmt(n * (r.gold_per_paid_diamond || 0)))), renderPass)
        : r.currency === 'diamonds' ? rendered(await rich('console.config.example.diamonds', fmt(n), el('b', {}, fmt(n * (r.diamonds_per_paid_diamond || 0)))), renderPass)
        : r.currency === 'free' ? rendered(await rich('console.config.example.free', fmt(n), el('b', {}, t('console.config.example.free_value'))), renderPass)
        : rendered(await rich('console.config.example.ban', el('b', {}, t('console.config.example.ban_value'))), renderPass);
      box.replaceChildren(...parts);

    } catch (error) { if (error !== staleRender) throw error; }
  }
  async function refresh() {
    const d = doc(); if (!d) return;
    const draft = drafts[d.name];
    document.querySelectorAll('#config-form .cfield').forEach(node => {
      const field = fields(d).find(f => f.path === node.dataset.path);
      node.hidden = !holds(field.when, draft);
      node.classList.toggle('changed', !same(getPath(draft, field.path), getPath(d.view.value, field.path)));
    });
    document.querySelectorAll('#config-form .section').forEach(card => { card.hidden = [...card.querySelectorAll('.cfield')].every(n => n.hidden); });
    document.querySelectorAll('#config-form .switch-state').forEach(s => { s.textContent = t(s.parentNode.querySelector('input').checked ? 'console.config.on' : 'console.config.off'); });
    await paintExamples(); await renderBar(); renderNav();
    clearTimeout(checkTimer);
    checkTimer = setTimeout(check, 300);
  }
  async function check() {
    const d = doc(); if (!d) return;
    const name = d.name, draft = drafts[name];
    try {
      const {value} = await api('config/' + encodeURIComponent(name) + '/check', {method: 'POST', body: draft});
      if (!same(drafts[name], draft)) return;
      checks[name] = value; await paintMessages(); await renderBar(); renderNav(); await renderBanners();
    } catch (error) { await toast(error); }
  }
  async function paintMessages() {
    const renderPass = beginRender('config-messages');
    try {
      const d = doc(); if (!d) return;
      document.querySelectorAll('#config-form .msgs').forEach(n => n.replaceChildren());
      document.querySelectorAll('#config-form .cfield').forEach(n => n.classList.remove('invalid'));
      const result = checks[d.name] || {problems: [], advice: []};
      const place = async (problem, tone) => {
        const field = fieldFor(d, problem.path);
        const target = document.querySelector(`#config-form [data-msgs="${CSS.escape(problem.path)}"]`) || (field && document.querySelector(`#config-form .cfield[data-path="${CSS.escape(field.path)}"] .msgs`));
        if (!target) return false;
        target.append(el('p', {class: 'msg ' + tone}, rendered(await describe(problem, field, d), renderPass)));
        if (tone === 'err') target.closest('.cfield')?.classList.add('invalid');
        const advanced = target.closest('details.advanced'); if (advanced && tone === 'err') advanced.open = true;
        return true;
      };
      rendered(await Promise.all(result.problems || [].map(async p => await place(p, 'err'))), renderPass);
      rendered(await Promise.all(result.advice || [].map(async p => await place(p, 'warn'))), renderPass);

    } catch (error) { if (error !== staleRender) throw error; }
  }
  async function renderBanners() {
    const renderPass = beginRender('config-banners');
    try {
      const d = doc(); if (!d) return;
      const banners = [];
      const pending = docs.filter(x => x.view.pending);
      if (pending.length) banners.push(el('div', {class: 'banner pending'}, el('div', {}, el('b', {}, t('console.config.banner.pending.title')),
        el('p', {}, rendered(await dynamicText('console.config.banner.pending.body', listJoin(pending.map(docTitle))), renderPass)))));
      if (!d.view.writable) banners.push(el('div', {class: 'banner readonly'}, el('div', {}, el('b', {}, t('console.config.read_only')), el('p', {}, t('console.config.banner.readonly.body')))));
      if (d.view.problems && d.view.problems.length) banners.push(el('div', {class: 'banner broken'}, el('div', {}, el('b', {}, t('console.config.banner.broken.title')),
        el('p', {}, t('console.config.banner.broken.body')),
        el('ul', {}, rendered(await Promise.all(d.view.problems.map(async p => { const text = await describe(p, fieldFor(d, p.path || ''), d); return el('li', {}, p.path ? await dynamicText('console.config.problem.at', p.path, text) : text); })), renderPass)))));
      const docProblems = ((checks[d.name] || {}).problems || []).filter(p => !p.path);
      if (docProblems.length) banners.push(el('div', {class: 'banner broken'}, el('div', {}, el('b', {}, t('console.config.banner.unchecked')), el('ul', {}, rendered(await Promise.all(docProblems.map(async p => el('li', {}, await describe(p, null, d)))), renderPass)))));
      $('config-banners').replaceChildren(...banners);

    } catch (error) { if (error !== staleRender) throw error; }
  }
  function renderNav() {
    const nav = $('doc-nav');
    const items = [];
    docs.forEach(d => {
      const changed = dirtyDoc(d);
      items.push(el('button', {type: 'button', class: 'doc', 'aria-current': String(d.name === current), onclick: async () => { current = d.name; await render(); await renderBanners(); window.scrollTo({top: 0}); }},
        docTitle(d), changed ? el('span', {class: 'badge'}, t('console.config.badge.unsaved')) : d.view.pending ? el('span', {class: 'badge'}, t('console.config.badge.pending')) : !d.view.writable ? el('span', {class: 'badge'}, t('console.config.read_only')) : null));
      if (d.name === current) {
        const bad = new Set(((checks[d.name] || {}).problems || []).map(p => { const f = fieldFor(d, p.path); return f && d.schema.sections.find(s => s.fields.includes(f))?.id; }));
        items.push(el('ul', {class: 'toc'}, d.schema.sections.filter(s => document.getElementById('sec-' + s.id) && !document.getElementById('sec-' + s.id).hidden).map(s =>
          el('li', {}, el('a', {href: '#config', class: bad.has(s.id) ? 'bad' : '', onclick: e => { e.preventDefault(); document.getElementById('sec-' + s.id).scrollIntoView({behavior: 'smooth', block: 'start'}); }}, sectionText(d, s, 'title'))))));
      }
    });
    if (isOwner()) items.push(el('div', {class: 'sep'}), el('button', {type: 'button', class: 'doc', onclick: () => $('overview').scrollIntoView({behavior: 'smooth'})}, t('console.overview.title')));
    nav.replaceChildren(...items);
  }
  function changes(d) {
    const out = [];
    const walk = (a, b, path) => {
      if (same(a, b)) return;
      const objectish = v => v && typeof v === 'object' && !Array.isArray(v);
      if (objectish(a) && objectish(b)) { new Set([...Object.keys(a), ...Object.keys(b)]).forEach(k => walk(a[k], b[k], path ? path + '.' + k : k)); return; }
      out.push({path, from: a, to: b});
    };
    walk(d.view.value, drafts[d.name], '');
    return out;
  }
  async function renderBar() {
    const renderPass = beginRender('config-savebar');
    try {
      const dirty = docs.filter(dirtyDoc);
      $('savebar').hidden = currentView !== 'config' || !dirty.length;
      if (!dirty.length) return;
      const problems = dirty.reduce((n, d) => n + ((checks[d.name] || {}).problems || []).length, 0);
      const count = dirty.reduce((n, d) => n + changes(d).length, 0);
      const text = $('savebar-text');
      text.textContent = problems ? rendered(await dynamicText('console.config.bar.problems', fmt(problems)), renderPass) : dirty.length > 1 ? rendered(await dynamicText('console.config.bar.changes_files', fmt(count), fmt(dirty.length)), renderPass) : rendered(await dynamicText('console.config.bar.changes', fmt(count)), renderPass);
      text.classList.toggle('bad', !!problems);
      $('save').disabled = !!problems;

    } catch (error) { if (error !== staleRender) throw error; }
  }
  const show = v => v === undefined ? t('console.config.unset') : JSON.stringify(v);
  $('show-diff').onclick = () => {
    const list = el('ul', {class: 'diff'});
    docs.filter(dirtyDoc).forEach(d => changes(d).forEach(c => {
      const f = fieldFor(d, c.path);
      list.append(el('li', {}, docTitle(d) + ' · ' + (f ? fieldText(d, f, 'label') : c.path), el('small', {}, c.path), el('div', {}, el('span', {class: 'from'}, show(c.from)), ' → ', el('span', {class: 'to'}, show(c.to)))));
    }));
    ask({title: t('console.config.diff.title'), body: t('console.config.diff.body'), extra: list, ok: t('console.config.diff.ok')});
  };
  $('discard').onclick = async () => {
    if (!await ask({title: t('console.config.discard.title'), body: t('console.config.discard.body'), ok: t('console.config.discard.ok'), danger: true})) return;
    docs.forEach(d => { drafts[d.name] = clone(d.view.value); }); checks = {}; await render(); await renderBanners();
  };
  async function save() {
    const dirty = docs.filter(dirtyDoc);
    if (!dirty.length || $('save').disabled) return;
    const ok = await ask({title: t('console.config.save.title'), body: await dynamicText('console.config.save.body', listJoin(dirty.map(d => d.file_name))), ok: t('console.config.save.ok')});
    if (!ok) return;
    $('save').disabled = true;
    for (const d of dirty) {
      try {
        const {value} = await api('config/' + encodeURIComponent(d.name), {method: 'PUT', headers: {'If-Match': d.view.revision}, body: JSON.stringify(drafts[d.name], null, 2)});
        d.view = value; drafts[d.name] = clone(value.value); checks[d.name] = {problems: [], advice: value.advice || []};
        await toast(Component.translatable('console.config.saved', docTitle(d)));
      } catch (error) {
        if (error.status === 409) {
          const reload = await ask({title: t('console.config.conflict.title'), body: await dynamicText('console.config.conflict.body', d.file_name), ok: t('console.config.conflict.ok'), danger: true});
          if (reload) { d.view = error.value.view; drafts[d.name] = clone(d.view.value); }
        } else if (error.status === 422) {
          checks[d.name] = {problems: error.value.problems || [], advice: []}; current = d.name; await toast(Component.translatable('console.config.fix_needed'));
        } else await toast(Component.translatable('console.config.save_failed', docTitle(d), error.text));
      }
    }
    await render(); await renderBanners(); await renderOverview();
  }
  $('save').onclick = save;
  document.addEventListener('keydown', async event => {
    if (currentView === 'config' && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') { event.preventDefault(); await save(); }
  });
  function paintOverview() {
    $('overview').hidden = !server;
    if (!server) { $('overview').replaceChildren(); return; }
    const rows = [['server_version', server.server_version], ['game_version', server.game_version], ['game_data_version', server.game_data_version], ['bundle_version', server.bundle_version],
      ['auth_mode', server.auth_mode && tr('console.overview.auth.' + server.auth_mode, server.auth_mode)], ['listen', server.listen], ['started_at', server.started_at && dateTime(server.started_at)],
      ['data_dir', server.data_dir], ['mail_spool', server.mail_spool], ['dev_tools', server.dev_tools]];
    $('overview').replaceChildren(el('h3', {}, t('console.overview.title')), el('dl', {}, rows.filter(r => r[1]).flatMap(([k, v]) => [el('dt', {}, t('console.overview.' + k)), el('dd', {}, /[\\/]/.test(v) ? el('code', {}, v) : v)])));
  }
  async function renderOverview() {
    server = null;
    if (isOwner()) { try { server = (await api('server')).value; } catch { server = null; } }
    paintOverview();
  }
  return {
    async open() {
      try {
        if (!docs.length) {
          docs = (await api('config/')).value.documents;
          docs.forEach(d => { if (drafts[d.name] === undefined) drafts[d.name] = clone(d.view.value); });
          current = current || (docs[0] && docs[0].name);
        }
        await render(); await renderBanners(); await renderOverview();
      } catch (error) { $('config-form').replaceChildren(el('p', {class: 'status err'}, error.message)); }
    },
    async relabel() { if (!docs.length) return; await render(); await paintMessages(); await renderBanners(); paintOverview(); },
    dirty: () => docs.some(dirtyDoc),
    reset() { docs = []; drafts = {}; checks = {}; current = ''; },
  };
})();

/* ---------- Updates ---------- */
const updatesView = (() => {
  let timer = null, status = null, checkRequested = false;
  const when = value => value ? dateTime(value) : '';
  const named = (group, value) => tr(`console.updates.${group}.${value}`, value);
  // Release notes arrive as {locale: text}; older servers send one string.
  const localized = value => value == null || typeof value === 'string' ? value || '' : value[locale] || value.en_us || Object.values(value).find(Boolean) || '';
  async function render() {
    const renderPass = beginRender('server-updates');
    try {
      const s = status;
      if (!s) return;
      const schedules = s.schedules;
      const component = s.available ? 'server' : schedules && schedules.available ? 'schedules' : 'server';
      const scheduleBranch = component === 'schedules';
      const source = scheduleBranch ? schedules.available : s.available;
      const a = scheduleBranch ? {...source, version: source.component.id, notes: source.component.notes} : source;
      const current = scheduleBranch ? schedules.current_id || t('console.updates.schedules.bundled') : s.current_version;
      const lastResult = scheduleBranch ? schedules && schedules.last_result : s.last_result;
      const facts = [['current_version', current], ['method', named('method', s.method)], ['auto_check', t(s.check_enabled ? 'console.updates.enabled' : 'console.updates.disabled')],
        ['last_check', when(s.last_check) || t('console.updates.never_checked')], ['check_error', s.last_error]];
      if (schedules) {
        facts.push(['game_version', schedules.game_version], ['game_data_version', a && a.game_data_version || schedules.game_data_version]);
      }
      if (lastResult) {
        const r = lastResult, outcome = named(scheduleBranch ? 'schedule_outcome' : 'outcome', r.outcome);
        facts.push(['last_switch', r.message ? rendered(await dynamicText('console.updates.last_switch_message', r.from, r.to, outcome, r.message), renderPass) : rendered(await dynamicText('console.updates.last_switch_value', r.from, r.to, outcome), renderPass)]);
      }
      $('update-facts').replaceChildren(...facts.filter(f => f[1]).flatMap(([k, v]) => [el('dt', {}, t(k === 'game_version' ? 'console.updates.schedules.game' : k === 'game_data_version' ? 'console.updates.schedules.gamedata' : 'console.updates.fact.' + k)), el('dd', {}, v)]));
      if (!a) {
        $('update-available').replaceChildren(el('p', {class: 'muted'}, s.last_error ? '' : t(scheduleBranch ? 'console.updates.schedules.up_to_date' : 'console.updates.up_to_date')));
      } else {
        const download = a.downloaded ? t('console.updates.downloaded') : a.download_error ? rendered(await dynamicText('console.updates.download_failed', a.download_error), renderPass) : t('console.updates.not_downloaded');
        const migration = a.migration || {}, migrationNote = localized(migration.note), notes = localized(a.notes);
        $('update-available').replaceChildren(...[
          el('h3', {}, rendered(await dynamicText('console.updates.available', a.version), renderPass)),
          el('p', {class: 'muted'}, [a.published_at && rendered(await dynamicText('console.updates.published', when(a.published_at)), renderPass), migration.kind && named('migration', migration.kind), download].filter(Boolean).join(' · ')),
          migrationNote ? el('p', {class: migration.kind === 'irreversible' ? 'status err' : 'status'}, migrationNote) : null,
          a.game_data_version ? el('p', {class: 'muted'}, t('console.updates.schedules.gamedata') + ': ' + a.game_data_version) : null,
          notes ? el('pre', {class: 'notes'}, notes) : null,
          a.incompatible ? el('p', {class: 'status err'}, a.incompatible) : null].filter(Boolean));
      }
      const actions = [];
      const m = s.maintenance;
      const delayInput = $('update-delay'), confirmInput = $('update-confirm');
      if (m) {
        const phase = named('phase', m.phase);
        actions.push(el('p', {}, m.at ? rendered(await dynamicText('console.updates.maintenance_at', phase, m.version, when(m.at), m.requested_by), renderPass) : rendered(await dynamicText('console.updates.maintenance', phase, m.version, m.requested_by), renderPass)));
        if (m.phase !== 'closing') actions.push(el('button', {type: 'button', class: 'btn', onclick: cancel}, t('console.updates.cancel')));
      } else if (a && !a.incompatible && s.method !== 'none') {
        const delay = el('input', {type: 'number', id: 'update-delay', min: '0', max: '1440', value: delayInput ? delayInput.value : String(s.countdown_minutes ?? 10)});
        const confirm = a.migration && a.migration.kind === 'irreversible' ? el('label', {}, el('input', {type: 'checkbox', id: 'update-confirm', checked: !!(confirmInput && confirmInput.checked)}), ' ' + t('console.updates.confirm_irreversible')) : null;
        actions.push(el('label', {}, t('console.updates.delay') + ' ', delay), confirm, el('button', {type: 'button', class: 'btn seal', onclick: apply}, t('console.updates.apply')));
      }
      $('update-actions').replaceChildren(...actions.filter(Boolean));
      $('update-check').disabled = !!s.checking;
      if (s.checking) {
        checkRequested = true;
        await say($('update-status'), Component.translatable('console.updates.checking'));
      } else if (s.last_error) {
        checkRequested = false;
        await say($('update-status'), s.last_error, 'err');
      } else if (checkRequested) {
        checkRequested = false;
        await say($('update-status'), Component.translatable('console.updates.checked'), 'ok');
      }
    } catch (error) { if (error !== staleRender) throw error; }
  }
  async function load() {
    try { status = (await api('updates')).value; await render(); }
    catch (error) { await say($('update-status'), error, 'err'); }
  }
  async function apply() {
    const selectedComponent = status.available ? 'server' : status.schedules && status.schedules.available ? 'schedules' : 'server';
    const minutes = Number($('update-delay').value || 0);
    const confirmIrreversible = !!($('update-confirm') && $('update-confirm').checked);
    const target = selectedComponent === 'schedules' ? status.schedules.available.component.id : status.available.version;
    const body = selectedComponent === 'schedules' ? await dynamicText('console.updates.schedules.confirm', fmt(minutes), target) : minutes ? await dynamicText('console.updates.apply.body_countdown', fmt(minutes), target) : await dynamicText('console.updates.apply.body_now', target);
    if (!await ask({title: t('console.updates.apply.title'), body, ok: t('console.updates.apply')})) return;
    try {
      status = (await api('updates/apply', {method: 'POST', body: {component: selectedComponent, delay_minutes: minutes, confirm_irreversible: confirmIrreversible}})).value;
      await render(); await say($('update-status'), Component.translatable('console.updates.scheduled'), 'ok');
    } catch (error) { await say($('update-status'), error, 'err'); }
  }
  async function cancel() {
    try { status = (await api('updates/cancel', {method: 'POST'})).value; await render(); await say($('update-status'), Component.translatable('console.updates.cancelled'), 'ok'); }
    catch (error) { await say($('update-status'), error, 'err'); }
  }
  $('update-check').onclick = async () => {
    try { checkRequested = true; await api('updates/check', {method: 'POST'}); await load(); }
    catch (error) { checkRequested = false; await say($('update-status'), error, 'err'); }
  };
  return {
    async open() { await load(); clearInterval(timer); timer = setInterval(load, 5000); },
    close() { clearInterval(timer); timer = null; },
    relabel: render,
  };
})();

loadTranslations().then(async () => { await applyLocale(detectLocale()); await boot(); });

window.addEventListener('unhandledrejection', event => {
  console.error(event.reason);
  const box = $('toast'); box.textContent = String(event.reason && event.reason.message || event.reason);
  box.classList.add('show'); clearTimeout(toastTimer); toastTimer = setTimeout(() => box.classList.remove('show'), 5200);
});
