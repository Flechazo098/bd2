'use strict';
const $ = id => document.getElementById(id);
const state = {session: null, players: [], view: ''};

/* ---------- 通用 ---------- */
const errorText = {
  unauthorized: '登录已过期，请重新登录。', forbidden: '没有权限执行这个操作。', csrf: '页面已过期，请刷新后重试。',
  rate_limited: '尝试太频繁，请稍后再试。', expired: '登录已过期，请重新开始。', rejected: '登录被拒绝或已取消。',
  not_granted: '这个游戏账号还没有管理台权限，请联系服主在“玩家”页授权。', not_found: '玩家不存在或已停用。',
  invalid_item: '这件物品不在可寄送目录中。', invalid_count: '数量需在 1 到 2,147,483,647 之间。', invalid_text: '邮件标题和正文都不能为空。',
  read_only: '该文件对服务端只读。', unavailable: '服务暂不可用，请稍后再试。', invalid: '请求无效。',
  unsupported: '这台服务器使用本地认证，请用服务端启动时打印的一次性链接登录。',
};
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
  if (response.status === 401 && path !== 'session') { boot(); }
  if (!response.ok && response.status !== 202) {
    const error = new Error((value && errorText[value.code]) || (value && value.error) || '请求失败（' + response.status + '）');
    error.status = response.status; error.code = value && value.code; error.value = value;
    throw error;
  }
  return {status: response.status, value};
}
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
function say(node, text, tone = '') { node.textContent = text; node.className = 'status' + (tone ? ' ' + tone : ''); }
let toastTimer;
function toast(text) { const t = $('toast'); t.textContent = text; t.classList.add('show'); clearTimeout(toastTimer); toastTimer = setTimeout(() => t.classList.remove('show'), 1800); }
async function copy(text, label) { try { await navigator.clipboard.writeText(text); toast(label + '已复制'); } catch { toast('复制失败，请手动选择'); } }
function ask({title, body, extra = null, ok = '确定', danger = false}) {
  const dialog = $('ask');
  $('ask-title').textContent = title; $('ask-body').textContent = body;
  $('ask-extra').replaceChildren(...(extra ? [extra] : []));
  $('ask-ok').textContent = ok; $('ask-ok').classList.toggle('seal', true);
  return new Promise(resolve => {
    const done = answer => { $('ask-ok').onclick = $('ask-cancel').onclick = dialog.oncancel = null; dialog.close(); resolve(answer); };
    $('ask-ok').onclick = () => done(true); $('ask-cancel').onclick = () => done(false);
    dialog.oncancel = event => { event.preventDefault(); done(false); };
    dialog.showModal(); (danger ? $('ask-cancel') : $('ask-ok')).focus();
  });
}
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const fmt = n => Number(n).toLocaleString('zh-CN');
const playerLabel = p => p ? (p.uid ? 'UID ' + p.uid : '') + (p.name ? ' · ' + p.name : '') : '';

/* ---------- 登录与路由 ---------- */
let loginTimer;
async function boot() {
  clearTimeout(loginTimer);
  try {
    state.session = (await api('session')).value;
  } catch (error) {
    showOnly('sign-in'); say($('sign-in-status'), error.message, 'err'); return;
  }
  if (!state.session.signed_in) return showSignIn();
  $('tabs').hidden = false; $('account').hidden = false;
  const owner = state.session.role === 'owner';
  document.querySelectorAll('[data-owner]').forEach(a => { a.hidden = !owner; });
  const player = state.session.player;
  $('account-name').textContent = player ? playerLabel(player) + (owner ? '（服主）' : '') : '服主';
  await loadPlayers();
  route();
}
function showOnly(view) {
  document.querySelectorAll('.view').forEach(v => { v.hidden = v.id !== 'view-' + view; });
  document.querySelectorAll('#tabs a').forEach(a => { if (a.dataset.view === view) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
}
function showSignIn() {
  $('tabs').hidden = true; $('account').hidden = true; showOnly('sign-in');
  const providers = state.session.providers || [];
  if (state.session.mode !== 'oauth' || !providers.length) {
    $('sign-in-lead').textContent = '这台服务器使用本地认证。请打开服务端启动时在控制台打印的一次性登录链接。';
    $('providers').replaceChildren(); return;
  }
  $('sign-in-lead').textContent = '使用你的游戏账号登录。服主账号拥有全部权限，其他玩家需要服主授权。';
  const names = {discord: 'Discord', google: 'Google'};
  $('providers').replaceChildren(...providers.map(p => el('button', {type: 'button', class: 'btn' + (p === providers[0] ? ' seal' : ''), onclick: () => startLogin(p)}, '使用 ' + (names[p] || p) + ' 登录')));
}
async function startLogin(provider) {
  const popup = window.open('about:blank', 'bd2-console-login');
  say($('sign-in-status'), '正在打开登录页面……');
  try {
    const {value} = await api('login/start', {method: 'POST', body: {provider}});
    if (popup) { popup.opener = null; popup.location.href = value.start_url; say($('sign-in-status'), '请在新窗口完成登录，完成后这里会自动进入。'); }
    else { $('sign-in-status').replaceChildren('浏览器拦截了新窗口，请点击：', el('a', {href: value.start_url, target: '_blank', rel: 'noopener'}, '打开登录页面')); }
    pollLogin(value.poll_interval_ms || 2000);
  } catch (error) { if (popup) popup.close(); say($('sign-in-status'), error.message, 'err'); }
}
async function pollLogin(interval) {
  try {
    const result = await api('login/poll', {method: 'POST'});
    if (result.status === 202) { loginTimer = setTimeout(() => pollLogin(interval), interval); return; }
    say($('sign-in-status'), '登录成功。', 'ok'); boot();
  } catch (error) { say($('sign-in-status'), error.message, 'err'); }
}
$('logout').onclick = async () => {
  if (config.dirty() && !await ask({title: '放弃未保存的配置？', body: '退出会丢弃尚未保存的配置改动。', ok: '退出', danger: true})) return;
  config.reset();
  try { await api('logout', {method: 'POST'}); } catch {}
  state.session = null; boot();
};
let currentView = '';
async function route() {
  if (!state.session || !state.session.signed_in) return;
  let view = (location.hash || '#mail').slice(1);
  if (!['mail', 'players', 'config'].includes(view) || (view !== 'mail' && state.session.role !== 'owner')) view = 'mail';
  if (currentView === 'config' && view !== 'config' && config.dirty()) {
    if (!await ask({title: '离开前保存配置？', body: '有尚未保存的配置改动。离开后改动会保留在本页，直到刷新或退出。', ok: '仍然离开'})) { history.replaceState(null, '', '#config'); return; }
  }
  currentView = view; showOnly(view);
  if (view === 'mail') mailTool.open();
  if (view === 'players') roster.open();
  if (view === 'config') config.open();
}
window.addEventListener('hashchange', route);
window.addEventListener('beforeunload', event => { if (config.dirty()) { event.preventDefault(); event.returnValue = ''; } });

async function loadPlayers() {
  try { state.players = (await api('players')).value.players; } catch (error) { state.players = []; toast(error.message); }
}

/* ---------- 邮件发放 ---------- */
const categoryNames = {currency: '货币（直接入账）', resource: '资源', food: '料理', cooking: '烹饪配方', random_box: '随机箱', quest_item: '任务物品', use_item: '使用物品', collection: '收藏品', my_room: '我的房间物品', instant_use: '即时使用物品'};
const mailTool = (() => {
  let all = [], cats = [], category = '', view = [], cursor = -1, chosen = null, byKey = new Map(), target = null, loaded = false, submitting = false, logCount = 0;
  const keyOf = x => x.element_type + ':' + x.id;
  const catName = x => categoryNames[x.category] || x.category;
  function details(x) {
    if (x.category === 'currency') return '邮件领取后直接叠加到账户余额，不生成背包物品。';
    if (!x.source_boxes) return '';
    const parts = ['直接发放此物品（无需开箱）'];
    if (x.original_name) parts.push('原始资源名：' + x.original_name);
    if (x.box_name) parts.push('确定性箱名称：' + x.box_name);
    if (x.product_name) parts.push('商品名：' + x.product_name);
    const boxes = x.source_boxes.slice(0, 4).join('、') + (x.source_boxes.length > 4 ? ' 等 ' + x.source_boxes.length + ' 个' : '');
    parts.push('固定箱映射：' + boxes);
    return parts.join('；');
  }
  function highlight(text, term) {
    if (!term) return esc(text);
    const at = text.toLowerCase().indexOf(term);
    if (at < 0) return esc(text);
    return esc(text.slice(0, at)) + '<mark>' + esc(text.slice(at, at + term.length)) + '</mark>' + esc(text.slice(at + term.length));
  }
  const subline = x => x.aliases && x.aliases.length ? '又名 ' + x.aliases.join('、') : details(x);
  function buildCategories() {
    const counts = new Map();
    all.forEach(x => counts.set(x.category, (counts.get(x.category) || 0) + 1));
    cats = [['', '全部', all.length], ...[...counts].map(([key, n]) => [key, categoryNames[key] || key, n])];
    $('cats').innerHTML = cats.map(([value, label, n], i) => `<button type="button" class="chip" data-i="${i}" aria-pressed="${value === category}">${esc(label)}<small>${n}</small></button>`).join('');
  }
  $('cats').onclick = event => {
    const chip = event.target.closest('.chip'); if (!chip) return;
    category = cats[+chip.dataset.i][0];
    $('cats').querySelectorAll('.chip').forEach(c => c.setAttribute('aria-pressed', c === chip));
    render(); $('q').focus();
  };
  function render() {
    const terms = $('q').value.toLowerCase().trim().split(/\s+/).filter(Boolean);
    const matches = all.filter(x => (!category || x.category === category) && terms.every(t => x._h.includes(t)));
    view = matches.slice(0, 500); cursor = terms.length && view.length ? 0 : -1;
    $('count').textContent = all.length ? `${matches.length === all.length ? '共' : '匹配 ' + matches.length + ' /'} ${all.length} 项${matches.length > view.length ? ' · 显示前 ' + view.length + ' 项' : ''}` : '';
    const term = terms.reduce((a, b) => b.length > a.length ? b : a, ''), picked = chosen && keyOf(chosen);
    $('items').innerHTML = view.map((x, i) => {
      const sub = subline(x);
      return `<li role="option" id="opt-${i}" data-i="${i}" class="item${keyOf(x) === picked ? ' chosen' : ''}" aria-selected="${keyOf(x) === picked}"><span class="n">${highlight(x.name, term)}</span><span class="c">${esc(catName(x))}</span><span class="i"><i>${x.element_type}·</i>${highlight(String(x.id), term)}</span>${sub ? `<span class="d" title="${esc(details(x) || sub)}">${highlight(sub, term)}</span>` : ''}</li>`;
    }).join('') || `<li class="empty">没有匹配的物品${category ? `<br><small>当前只看「${esc(categoryNames[category] || category)}」，可以切回「全部」试试</small>` : ''}</li>`;
    paintCursor(true); renderRecent(!terms.length);
  }
  function paintCursor(scroll) {
    const old = $('items').querySelector('.active'); if (old) old.classList.remove('active');
    const row = $('opt-' + cursor);
    if (row) { row.classList.add('active'); if (scroll) row.scrollIntoView({block: 'nearest'}); }
    $('q').setAttribute('aria-activedescendant', row ? row.id : '');
  }
  $('items').onmousemove = event => { const row = event.target.closest('[data-i]'); if (row && +row.dataset.i !== cursor) { cursor = +row.dataset.i; paintCursor(false); } };
  $('items').onclick = event => { const row = event.target.closest('[data-i]'); if (row) pick(view[+row.dataset.i]); };
  $('q').oninput = render;
  $('q').onkeydown = event => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); if (!view.length) return; cursor = (cursor + (event.key === 'ArrowDown' ? 1 : -1) + view.length) % view.length; paintCursor(true); }
    else if (event.key === 'Enter') { event.preventDefault(); const x = view[Math.max(cursor, 0)]; if (x) pick(x); }
    else if (event.key === 'Escape') { event.preventDefault(); if ($('q').value) { $('q').value = ''; render(); } else $('q').blur(); }
  };
  const recentKey = 'bd2-console-recent';
  function recentList() { try { return JSON.parse(localStorage.getItem(recentKey)) || []; } catch { return []; } }
  function remember(x) { try { localStorage.setItem(recentKey, JSON.stringify([keyOf(x), ...recentList().filter(k => k !== keyOf(x))].slice(0, 8))); } catch {} }
  function renderRecent(visible) {
    const items = recentList().map(k => byKey.get(k)).filter(Boolean), box = $('recent');
    box.hidden = !visible || !items.length; if (box.hidden) return;
    box.replaceChildren(box.firstElementChild, ...items.map(x => el('button', {type: 'button', title: catName(x) + ' · ' + keyOf(x), onclick: () => pick(x)}, x.name)));
  }
  function pick(x) {
    chosen = x; paintParcel();
    $('items').querySelectorAll('.item').forEach(row => { const on = keyOf(view[+row.dataset.i]) === keyOf(x); row.classList.toggle('chosen', on); row.setAttribute('aria-selected', on); });
    say($('status'), ''); $('quantity').focus(); $('quantity').select();
  }
  function unpick() { chosen = null; paintParcel(); $('items').querySelectorAll('.chosen').forEach(row => { row.classList.remove('chosen'); row.setAttribute('aria-selected', false); }); $('q').focus(); }
  function paintParcel() {
    const parcel = $('parcel'); parcel.classList.toggle('empty', !chosen);
    if (!chosen) { parcel.innerHTML = '<p>从目录中挑一件物品放进来<br><span>或按 <kbd>/</kbd> 直接搜索</span></p>'; parcel.onclick = () => $('q').focus(); return; }
    parcel.onclick = null;
    const d = details(chosen);
    parcel.innerHTML = `<div class="p-name">${esc(chosen.name)}</div><div class="p-meta"><span>${esc(catName(chosen))}</span><code>类型 ${chosen.element_type} · ID ${chosen.id}</code></div>${d ? `<p class="p-detail">${esc(d)}</p>` : ''}<button type="button" class="x" aria-label="移出包裹单" title="移出包裹单">×</button>`;
    parcel.querySelector('.x').onclick = unpick;
  }
  const units = {k: 1e3, '千': 1e3, w: 1e4, '万': 1e4, m: 1e6, '百万': 1e6, '亿': 1e8};
  function quantity() { const m = $('quantity').value.replace(/[\s,，_]/g, '').toLowerCase().match(/^(\d+(?:\.\d+)?)(k|千|w|万|m|百万|亿)?$/); return m ? Math.round(parseFloat(m[1]) * (units[m[2]] || 1)) : NaN; }
  function checkQuantity() {
    const n = quantity(), input = $('quantity'), hint = $('qty-hint'); let problem = '';
    if (!Number.isInteger(n) || n < 1) problem = '请输入正整数'; else if (n > 2147483647) problem = '不能超过 2,147,483,647';
    input.setCustomValidity(problem);
    const typed = input.value.trim() !== '';
    hint.classList.toggle('err', !!problem && typed);
    hint.textContent = problem && typed ? problem : !problem && String(n) !== input.value.trim() ? '= ' + fmt(n) : '支持 5k、2w、3万';
    return problem ? NaN : n;
  }
  $('quantity').oninput = checkQuantity;
  $('quantity').onkeydown = event => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
    event.preventDefault(); const n = quantity() || 0, step = event.shiftKey ? 10 : 1;
    $('quantity').value = Math.max(1, n + (event.key === 'ArrowUp' ? step : -step)); checkQuantity();
  };
  $('presets').onclick = event => { const b = event.target.closest('[data-q]'); if (!b) return; const add = +b.dataset.q; $('quantity').value = event.shiftKey ? (quantity() || 0) + add : add; checkQuantity(); $('quantity').focus(); };
  $('title').oninput = () => { $('letter-peek').textContent = $('title').value || '（无标题）'; };
  document.addEventListener('keydown', event => {
    if ($('ask').open || currentView !== 'mail' || $('tools').disabled) return;
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
    if (event.key === '/' && !typing && !event.ctrlKey && !event.metaKey && !event.altKey) { event.preventDefault(); $('q').focus(); $('q').select(); }
    else if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); $('form').requestSubmit(); }
  });
  function addLog(entry) {
    const li = el('li', {},
      el('span', {class: 'what'}, el('b', {}, entry.item.name), ' × ' + fmt(entry.count)),
      el('span', {class: 'meta'}, entry.time + ' · 寄往 ' + playerLabel(entry.player)),
      el('button', {type: 'button', class: 'again', title: '把这件物品和数量重新放进包裹单', onclick: () => { pick(entry.item); $('quantity').value = entry.count; checkQuantity(); $('quantity').select(); }}, '再来一份'),
      el('button', {type: 'button', class: 'ident', title: '点击复制发放标识', onclick: () => copy(entry.identity, '发放标识')}, entry.identity));
    $('log').prepend(li); $('log-empty').hidden = true; $('log-count').textContent = ++logCount + ' 件';
  }
  function showRecipient(player) {
    const display = $('uid-display');
    display.textContent = player ? player.uid || '——' : '——'; display.classList.toggle('none', !player);
    $('slip-uid').textContent = player ? player.uid : '—';
    $('recipient-note').textContent = player ? (player.name || '') + (state.session.role === 'owner' ? '' : '（只能给自己寄件）') : '还没有可以寄件的玩家。玩家首次登录游戏后会出现在这里。';
  }
  $('uid-display').onclick = () => { if (target && target.uid) copy(target.uid, 'UID '); };
  function showInventory(value) {
    const on = value.unlimited, l = value.limits;
    $('unlimited-inventory').checked = on;
    $('inventory-limits').innerHTML = `<span></span><span class="h">道具</span><span class="h">装备</span><span class="lab${on ? '' : ' on'}">普通容量</span><b>${l.items.default}</b><b>${l.equipment.default}</b><span class="lab${on ? ' on' : ''}">无限模式</span><b>${l.items.maximum}</b><b>${l.equipment.maximum}</b>`;
  }
  async function select(accountID) {
    target = state.players.find(p => p.account_id === accountID) || null;
    showRecipient(target); $('tools').disabled = true; say($('status'), ''); say($('inventory-status'), '');
    $('lock-note').textContent = target ? '正在读取玩家设置……' : '没有可以寄件的玩家';
    if (!target) return;
    try {
      const requested = target.account_id;
      const {value} = await api('players/' + encodeURIComponent(requested) + '/inventory');
      if (target && target.account_id === requested) { showInventory(value); $('tools').disabled = !loaded; $('lock-note').textContent = loaded ? '' : '正在读取物品目录……'; }
    } catch (error) { $('lock-note').textContent = error.message; }
  }
  $('player').onchange = () => select($('player').value);
  $('form').onsubmit = async event => {
    event.preventDefault(); if (submitting || !target) return;
    if (!chosen) { say($('status'), '请先在目录中挑选要寄出的物品。', 'err'); const parcel = $('parcel'); parcel.classList.remove('shake'); void parcel.offsetWidth; parcel.classList.add('shake'); $('q').focus(); return; }
    const count = checkQuantity();
    if (!$('title').checkValidity() || !$('body').checkValidity()) $('letter').open = true;
    if (!$('form').reportValidity()) return;
    submitting = true; const send = $('grant-submit'), player = target, item = chosen; send.disabled = true; $('player').disabled = true;
    try {
      const {value} = await api('players/' + encodeURIComponent(player.account_id) + '/mail', {method: 'POST', body: {element_type: item.element_type, id: item.id, count, title: $('title').value, body: $('body').value}});
      say($('status'), `已寄往 UID ${player.uid}。玩家刷新游戏邮箱即可领取，无需重启服务端。`, 'ok');
      addLog({item, count, player, identity: value.identity, time: new Date().toLocaleTimeString('zh-CN', {hour: '2-digit', minute: '2-digit'})}); remember(item);
      send.classList.add('done'); send.firstChild.textContent = '已寄出';
      setTimeout(() => { send.classList.remove('done'); send.firstChild.textContent = '寄出'; }, 1400);
    } catch (error) { say($('status'), error.message, 'err'); }
    finally { submitting = false; send.disabled = false; $('player').disabled = false; }
  };
  $('unlimited-inventory').onchange = async event => {
    const box = event.target, old = !box.checked; box.disabled = true; $('player').disabled = true;
    try {
      const {value} = await api('players/' + encodeURIComponent(target.account_id) + '/inventory', {method: 'PUT', body: {unlimited: box.checked}});
      showInventory(value); target.unlimited_inventory = value.unlimited;
      say($('inventory-status'), `已保存 UID ${target.uid} 的设置。玩家重新登录游戏后生效。`, 'ok');
    } catch (error) { box.checked = old; say($('inventory-status'), error.message, 'err'); }
    finally { box.disabled = false; $('player').disabled = false; }
  };
  async function loadCatalog() {
    try {
      all = (await api('catalog')).value.items;
      all.forEach(x => { x._h = [x.id, x.element_type, keyOf(x), x.name, catName(x), ...(x.aliases || []), details(x)].join(' ').toLowerCase(); });
      byKey = new Map(all.map(x => [keyOf(x), x])); buildCategories(); render(); loaded = true;
      if (target && $('lock-note').textContent === '正在读取物品目录……') { $('tools').disabled = false; }
    } catch (error) { $('items').innerHTML = `<li class="empty">${esc(error.message)}</li>`; }
  }
  return {
    open(accountID) {
      paintParcel();
      const owner = state.session.role === 'owner';
      const usable = state.players.filter(p => p.active);
      $('player-selector').hidden = !owner || usable.length < 2;
      $('player').replaceChildren(...usable.map(p => new Option(playerLabel(p) + (p.owner ? '（服主）' : ''), p.account_id)));
      const self = state.session.player && state.session.player.account_id;
      const initial = accountID || (target && usable.some(p => p.account_id === target.account_id) ? target.account_id : null) || (usable.some(p => p.account_id === self) ? self : usable[0] && usable[0].account_id);
      if (initial) $('player').value = initial;
      if (!loaded && !all.length) loadCatalog();
      select(initial || '');
    },
  };
})();

/* ---------- 玩家 ---------- */
const roster = (() => {
  function render() {
    const q = $('player-q').value.trim().toLowerCase();
    const rows = state.players.filter(p => !q || (p.uid + ' ' + p.name).toLowerCase().includes(q));
    $('roster').replaceChildren(...rows.map(p => {
      const toggle = (checked, disabled, onchange, label) => el('label', {class: 'switch', title: label}, el('input', {type: 'checkbox', role: 'switch', checked, disabled, 'aria-label': label, onchange}), el('span', {class: 'track', 'aria-hidden': 'true'}));
      const memberSwitch = p.owner ? el('span', {class: 'muted'}, '—') : toggle(p.member, !p.active || state.session.mode !== 'oauth', event => setMember(p, event.target), '管理台成员');
      return el('tr', {class: p.active ? '' : 'inactive'},
        el('td', {class: 'uid-cell'}, p.uid || '—'),
        el('td', {}, p.name || ''),
        el('td', {}, p.owner ? el('span', {class: 'pill owner'}, '服主') : null, p.member ? el('span', {class: 'pill member'}, '成员') : null, p.active ? null : el('span', {class: 'pill'}, '已停用')),
        el('td', {class: 'c'}, memberSwitch),
        el('td', {class: 'c'}, toggle(p.unlimited_inventory, !p.active, event => setInventory(p, event.target), '无限背包容量')),
        el('td', {class: 'act'}, p.active ? el('button', {type: 'button', class: 'linkish', onclick: () => { location.hash = '#mail'; setTimeout(() => mailTool.open(p.account_id)); }}, '给 TA 寄件 →') : null));
    }));
    if (!rows.length) $('roster').replaceChildren(el('tr', {}, el('td', {colspan: 6, class: 'muted'}, state.players.length ? '没有匹配的玩家' : '还没有玩家。玩家首次登录游戏后会出现在这里。')));
  }
  async function setMember(player, box) {
    if (!box.checked && !await ask({title: '撤销管理台权限？', body: `UID ${player.uid} 将立即退出管理台，之后需要重新授权才能登录。`, ok: '撤销', danger: true})) { box.checked = true; return; }
    box.disabled = true;
    try {
      await api('players/' + encodeURIComponent(player.account_id) + '/member', {method: 'PUT', body: {member: box.checked}});
      player.member = box.checked; render();
      toast(box.checked ? `已授权 UID ${player.uid}，对方可用游戏账号登录管理台` : `已撤销 UID ${player.uid} 的权限`);
    } catch (error) { box.checked = !box.checked; say($('roster-status'), error.message, 'err'); }
    finally { box.disabled = false; }
  }
  async function setInventory(player, box) {
    box.disabled = true;
    try {
      const {value} = await api('players/' + encodeURIComponent(player.account_id) + '/inventory', {method: 'PUT', body: {unlimited: box.checked}});
      player.unlimited_inventory = value.unlimited; toast(`UID ${player.uid} ${value.unlimited ? '已开启' : '已关闭'}无限背包，重新登录游戏后生效`);
    } catch (error) { box.checked = !box.checked; say($('roster-status'), error.message, 'err'); }
    finally { box.disabled = false; }
  }
  $('player-q').oninput = render;
  return {async open() { say($('roster-status'), ''); await loadPlayers(); render(); }};
})();

/* ---------- 服务器配置（通用编辑器） ---------- */
const config = (() => {
  let docs = [], current = '', drafts = {}, checks = {}, checkTimer, server = null;
  const doc = () => docs.find(d => d.name === current);
  const fields = d => d.schema.sections.flatMap(s => s.fields);
  const dirtyDoc = d => drafts[d.name] !== undefined && !same(drafts[d.name], d.view.value);
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
  // 把问题路径对应到字段：去掉列表下标，按 * 匹配映射键。
  function fieldFor(d, path) {
    const bare = path.replace(/\[\d+\]/g, '');
    const parts = bare.split('.');
    return fields(d).find(f => {
      const pattern = f.path.split('.');
      return pattern.length === parts.length && pattern.every((p, i) => p === '*' || p === parts[i]);
    }) || fields(d).filter(f => bare.startsWith(f.path + '.') || bare === f.path).sort((a, b) => b.path.length - a.path.length)[0];
  }
  const phrases = {
    'must be a bare host name without scheme, path or port': '只填主机名，不带协议、路径或端口',
    'must be host:port': '格式应为 主机:端口，例如 0.0.0.0:8444',
    'plaintext realtime listeners must bind loopback': '明文模式只能监听回环地址',
    'plaintext realtime public host must be loopback': '明文模式的公开地址必须是回环 IP',
    'is required unless plaintext loopback development is enabled': '未启用本机明文模式时必填',
    'must be a nonempty path': '路径不能为空',
    'is required by an active provider': '启用第三方审核时必填',
    'is required by the Azure provider': 'Azure 需要填写这一项',
    'must be a lowercase identifier': '只能使用小写字母、数字和下划线，并以字母开头',
    'local mode must not configure OAuth': '本地模式下不能配置这一项',
    'is required in oauth mode': 'OAuth 模式下必填',
    'oauth mode requires at least one provider': '至少启用一个登录提供方',
    'must be an absolute origin without path, query, fragment, or user info': '应为不带路径的根地址，例如 https://game.example.com',
    'must use HTTPS except on localhost': '除 localhost 外必须使用 HTTPS',
    'must be a positive Go duration': '应为正的时长，例如 15m、720h',
    'official mode must use the versioned official resource origin': '官方模式只能使用官方地址',
    'is required in server mode': '自建资源源模式下必填',
    'must be an absolute HTTP(S) URL without credentials, query, or fragment': '应为完整的 HTTP(S) 地址，不带账号、查询参数或锚点',
    'must not have a trailing slash': '末尾不能带斜杠',
    'must use HTTPS outside loopback development': '除回环地址外必须使用 HTTPS',
    'is required': '必填', 'must be present': '文件中必须写明这一项',
  };
  function describe(problem, field, d) {
    const m = problem.message || '';
    const item = (problem.path.match(/\[(\d+)\]$/) || [])[1];
    const prefix = item !== undefined ? `第 ${+item + 1} 项：` : '';
    let text;
    if (problem.rule === 'enum' && field && field.options) text = '只能选择：' + field.options.map(o => o.label).join('、');
    else if (problem.rule === 'enum' && field && field.keys) text = '不支持这一项';
    else if (problem.rule === 'range' && field) text = `需为 ${fmt(field.min)} 到 ${fmt(field.max)} 之间的整数`;
    else if (problem.rule === 'unique') text = '有重复项';
    else if (problem.rule === 'conflict' && m.startsWith('is fixed to')) { const lock = (d.view.locks || []).find(l => l.path === problem.path); text = '已锁定' + (lock ? '：' + lock.reason : ''); }
    else if (problem.rule === 'environment') { const env = (m.match(/environment variable (\S+)/) || [])[1]; text = m.includes('32-byte') ? `当前服务端进程中环境变量 ${env} 不是有效的 32 字节 base64 密钥，重启后服务端会拒绝启动。` : `当前服务端进程没有设置环境变量 ${env}。保存后重启前，请先在服务端环境中设置它。`; }
    else if (problem.rule === 'missing_file') text = '服务端当前读不到这个文件：' + m.replace(/ is not readable by this server process$/, '');
    else if (problem.rule === 'decode') text = '格式错误：' + m;
    else text = phrases[m] || m;
    return prefix + text;
  }
  function render() {
    const d = doc(); if (!d) return;
    const draft = drafts[d.name], readonly = !d.view.writable;
    renderNav();
    const form = $('config-form');
    const box = el('fieldset', {disabled: readonly});
    box.append(el('section', {class: 'card doc-head'}, el('h2', {}, d.schema.title), d.schema.summary ? el('p', {}, d.schema.summary) : null,
      el('div', {class: 'path'}, el('span', {}, d.view.path), d.view.modified ? el('span', {}, '修改于 ' + new Date(d.view.modified).toLocaleString('zh-CN')) : null)));
    for (const section of d.schema.sections) {
      const own = section.fields.filter(f => !f.path.includes('*'));
      if (own.every(f => f.advanced && f.path === 'schema_version')) continue;
      const card = el('section', {class: 'card section', id: 'sec-' + section.id}, el('h3', {}, section.title), section.help ? el('p', {class: 'help'}, section.help) : null);
      const basic = own.filter(f => !f.advanced), advanced = own.filter(f => f.advanced);
      basic.forEach(f => card.append(renderField(d, f, draft)));
      if (advanced.length) {
        const details = el('details', {class: 'advanced'}, el('summary', {}, '高级选项'));
        advanced.forEach(f => details.append(renderField(d, f, draft)));
        card.append(details);
      }
      box.append(card);
    }
    form.replaceChildren(box);
    refresh();
  }
  function renderField(d, field, draft) {
    const value = getPath(draft, field.path);
    const lock = (d.view.locks || []).find(l => l.path === field.path);
    const wrap = el('div', {class: 'cfield', 'data-path': field.path});
    wrap.append(el('div', {class: 'label'}, field.label, el('small', {}, field.path)));
    const control = el('div', {class: 'control'});
    const update = next => { setPath(drafts[d.name], field.path, next); refresh(); };
    control.append(widget(field, value, update, lock, d));
    if (field.help) control.append(el('p', {class: 'help'}, field.help));
    control.append(el('div', {class: 'msgs'}));
    if (lock) control.append(el('p', {class: 'msg lock'}, '🔒 已锁定：' + lock.reason));
    wrap.append(control);
    return wrap;
  }
  function widget(field, value, update, lock, d) {
    const disabled = !!lock;
    if (field.kind === 'bool') {
      return el('label', {class: 'switch'}, el('input', {type: 'checkbox', role: 'switch', checked: !!value, disabled, onchange: e => update(e.target.checked)}), el('span', {class: 'track', 'aria-hidden': 'true'}), value ? '已开启' : '已关闭');
    }
    if (field.options && field.kind !== 'list' && !field.open_options) {
      const options = field.options.filter(o => o.value !== '' || value === '');
      const name = 'opt-' + field.path;
      const help = el('p', {class: 'option-help'});
      const showHelp = v => { const o = field.options.find(x => same(x.value, v)); help.textContent = o && o.help ? o.help : ''; };
      showHelp(value);
      const group = el('div', {class: 'segmented', role: 'radiogroup', 'aria-label': field.label}, options.map(o => el('label', {},
        el('input', {type: 'radio', name, checked: same(o.value, value), disabled, onchange: () => { update(clone(o.value)); showHelp(o.value); }}), el('span', {}, o.label))));
      const holder = el('div', {}, group, help);
      const extra = extras[field.path]; if (extra) holder.append(extra(d));
      return holder;
    }
    if (field.kind === 'integer') {
      const input = el('input', {inputmode: 'numeric', value: value ?? '', disabled, placeholder: field.placeholder || '',
        oninput: e => { const raw = e.target.value.trim().replace(/[,，_\s]/g, ''); update(/^-?\d+$/.test(raw) ? Number(raw) : raw); }});
      return el('div', {class: 'unit-input'}, input, field.min != null ? el('span', {}, `${fmt(field.min)} – ${fmt(field.max)}`) : null);
    }
    if (field.kind === 'string') {
      const mono = ['path', 'env', 'url', 'listen', 'host', 'origin', 'duration', 'identifier'].includes(field.format);
      return el('div', {class: 'text-input' + (mono ? ' mono' : '')}, el('input', {value: value ?? '', disabled, placeholder: field.placeholder || '', spellcheck: 'false', autocomplete: 'off', oninput: e => update(e.target.value)}));
    }
    if (field.kind === 'list' && field.options) return chipList(field, value || [], update, disabled);
    if (field.kind === 'list') return rowList(field, value || [], update, disabled);
    if (field.kind === 'map') return mapEditor(field, value || {}, update, disabled, d);
    return el('code', {}, JSON.stringify(value));
  }
  function chipList(field, list, update, disabled) {
    const holder = el('div', {class: 'chips'});
    const draw = () => {
      const custom = list.filter(v => !field.options.some(o => o.value === v));
      holder.replaceChildren(...field.options.map(o => el('button', {type: 'button', class: 'chip', 'aria-pressed': String(list.includes(o.value)), disabled, title: o.value,
        onclick: () => { list = list.includes(o.value) ? list.filter(v => v !== o.value) : [...list, o.value]; update(list); draw(); }}, o.label)),
        ...custom.map(v => el('button', {type: 'button', class: 'chip', 'aria-pressed': 'true', disabled, title: '点击移除', onclick: () => { list = list.filter(x => x !== v); update(list); draw(); }}, v + ' ×')),
        field.open_options && !disabled ? el('input', {class: 'chip-add', placeholder: '其他范围，回车添加', spellcheck: 'false',
          onkeydown: e => { if (e.key !== 'Enter') return; e.preventDefault(); const v = e.target.value.trim(); if (v && !list.includes(v)) { list = [...list, v]; update(list); draw(); } }}) : null,
        el('span', {class: 'muted'}, `已选 ${list.length} 项`),
        !disabled ? el('button', {type: 'button', class: 'linkish', onclick: () => { list = field.options.map(o => o.value); update(list); draw(); }}, '全选') : null);
    };
    draw();
    return holder;
  }
  function rowList(field, list, update, disabled) {
    const holder = el('div', {class: 'rows'});
    const draw = () => {
      holder.replaceChildren(...list.map((v, i) => el('div', {class: 'row', 'data-item': i},
        el('input', {value: v, disabled, spellcheck: 'false', placeholder: field.placeholder || '', oninput: e => { list[i] = e.target.value; update([...list]); }}),
        disabled ? null : el('button', {type: 'button', class: 'icon-btn', title: '移除', 'aria-label': '移除', onclick: () => { list.splice(i, 1); update([...list]); draw(); }}, '×'))),
        disabled ? null : el('button', {type: 'button', class: 'add', onclick: () => { list.push(''); update([...list]); draw(); holder.querySelector('.row:last-of-type input').focus(); }}, '+ 添加'),
        !list.length ? el('span', {class: 'muted'}, '（空）') : null);
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
          el('label', {class: 'switch'}, el('input', {type: 'checkbox', role: 'switch', checked: enabled, disabled, onchange: e => {
            // 重新启用时恢复文件中已保存的值，避免误关后要重新填写。
            if (e.target.checked) map[k.value] = clone(getPath(d.view.value, field.path + '.' + k.value)) || Object.fromEntries(children.map(c => [c.path.split('.').at(-1), '']));
            else delete map[k.value];
            update({...map}); draw(); }}), el('span', {class: 'track', 'aria-hidden': 'true'}), k.label));
        if (enabled) {
          const sub = el('div', {class: 'sub'});
          children.forEach(c => {
            const name = c.path.split('.').at(-1), path = field.path + '.' + k.value + '.' + name;
            sub.append(el('label', {}, c.label), el('div', {class: 'text-input mono', 'data-path': path}, el('input', {value: map[k.value][name] ?? '', disabled, spellcheck: 'false', autocomplete: 'off',
              oninput: e => { map[k.value][name] = e.target.value; update({...map}); }})), el('div', {class: 'msgs', 'data-msgs': path}));
          });
          entry.append(sub);
        }
        return entry;
      }));
    };
    draw();
    return holder;
  }
  // 字段专属的小提示：根据当前草稿实时给出例子。
  const extras = {
    'purchases.diamond_recharge.currency': d => {
      const box = el('div', {class: 'example'});
      box.dataset.example = 'recharge';
      return box;
    },
  };
  function paintExamples() {
    const box = document.querySelector('[data-example=recharge]'); if (!box) return;
    const r = (drafts.game || {}).purchases?.diamond_recharge || {};
    const n = 100;
    const text = r.currency === 'gold' ? ['充值 100 付费钻石档位：消耗 ', fmt(n * (r.gold_per_paid_diamond || 0)), ' 金币']
      : r.currency === 'diamonds' ? ['充值 100 付费钻石档位：消耗 ', fmt(n * (r.diamonds_per_paid_diamond || 0)), ' 普通钻石']
      : r.currency === 'free' ? ['充值 100 付费钻石档位：', '免费', '']
      : ['付费钻石充值：', '已禁止', ''];
    box.replaceChildren(text[0], el('b', {}, text[1]), text[2]);
  }
  function refresh() {
    const d = doc(); if (!d) return;
    const draft = drafts[d.name];
    document.querySelectorAll('#config-form .cfield').forEach(node => {
      const field = fields(d).find(f => f.path === node.dataset.path);
      node.hidden = !holds(field.when, draft);
      node.classList.toggle('changed', !same(getPath(draft, field.path), getPath(d.view.value, field.path)));
    });
    document.querySelectorAll('#config-form .section').forEach(card => { card.hidden = [...card.querySelectorAll('.cfield')].every(n => n.hidden); });
    document.querySelectorAll('#config-form .switch').forEach(s => { const input = s.querySelector('input'); const last = s.lastChild; if (last && last.nodeType === 3 && (last.textContent === '已开启' || last.textContent === '已关闭')) last.textContent = input.checked ? '已开启' : '已关闭'; });
    paintExamples(); renderBar(); renderNav();
    clearTimeout(checkTimer);
    checkTimer = setTimeout(check, 300);
  }
  async function check() {
    const d = doc(); if (!d) return;
    const name = d.name, draft = drafts[name];
    try {
      const {value} = await api('config/' + encodeURIComponent(name) + '/check', {method: 'POST', body: draft});
      if (!same(drafts[name], draft)) return;
      checks[name] = value; paintMessages(); renderBar(); renderNav(); renderBanners();
    } catch (error) { toast(error.message); }
  }
  function paintMessages() {
    const d = doc(); if (!d) return;
    document.querySelectorAll('#config-form .msgs').forEach(n => n.replaceChildren());
    document.querySelectorAll('#config-form .cfield').forEach(n => n.classList.remove('invalid'));
    const result = checks[d.name] || {problems: [], advice: []};
    const place = (problem, tone) => {
      const field = fieldFor(d, problem.path);
      const target = document.querySelector(`#config-form [data-msgs="${CSS.escape(problem.path)}"]`) || (field && document.querySelector(`#config-form .cfield[data-path="${CSS.escape(field.path)}"] .msgs`));
      if (!target) return false;
      target.append(el('p', {class: 'msg ' + tone}, describe(problem, field, d)));
      if (tone === 'err') target.closest('.cfield')?.classList.add('invalid');
      const advanced = target.closest('details.advanced'); if (advanced && tone === 'err') advanced.open = true;
      return true;
    };
    result.problems.forEach(p => place(p, 'err'));
    result.advice.forEach(p => place(p, 'warn'));
  }
  function renderBanners() {
    const d = doc(); if (!d) return;
    const banners = [];
    const pending = docs.filter(x => x.view.pending);
    if (pending.length) banners.push(el('div', {class: 'banner pending'}, el('div', {}, el('b', {}, '已保存，等待重启生效'),
      el('p', {}, pending.map(x => x.schema.title).join('、') + ' 的改动已经写入文件，但当前运行的服务端还在使用旧配置。重启服务端进程后生效。'))));
    if (!d.view.writable) banners.push(el('div', {class: 'banner readonly'}, el('div', {}, el('b', {}, '只读'), el('p', {}, '服务端没有这个文件的写权限（例如部署方以只读方式挂载），这里只能查看。'))));
    if (d.view.problems && d.view.problems.length) banners.push(el('div', {class: 'banner broken'}, el('div', {}, el('b', {}, '磁盘上的文件当前无效'),
      el('p', {}, '文件在管理台之外被改成了服务端无法加载的内容，下次重启会失败。修正下面的问题后保存即可。'),
      el('ul', {}, d.view.problems.map(p => el('li', {}, (p.path ? p.path + '：' : '') + describe(p, fieldFor(d, p.path || ''), d)))))));
    const docProblems = ((checks[d.name] || {}).problems || []).filter(p => !p.path);
    if (docProblems.length) banners.push(el('div', {class: 'banner broken'}, el('div', {}, el('b', {}, '无法校验'), el('ul', {}, docProblems.map(p => el('li', {}, describe(p, null, d)))))));
    $('config-banners').replaceChildren(...banners);
  }
  function renderNav() {
    const nav = $('doc-nav');
    const items = [];
    docs.forEach(d => {
      const changed = dirtyDoc(d);
      items.push(el('button', {type: 'button', class: 'doc', 'aria-current': String(d.name === current), onclick: () => { current = d.name; render(); renderBanners(); window.scrollTo({top: 0}); }},
        d.schema.title, changed ? el('span', {class: 'badge'}, '未保存') : d.view.pending ? el('span', {class: 'badge'}, '待重启') : !d.view.writable ? el('span', {class: 'badge'}, '只读') : null));
      if (d.name === current) {
        const bad = new Set(((checks[d.name] || {}).problems || []).map(p => { const f = fieldFor(d, p.path); return f && d.schema.sections.find(s => s.fields.includes(f))?.id; }));
        items.push(el('ul', {class: 'toc'}, d.schema.sections.filter(s => document.getElementById('sec-' + s.id) && !document.getElementById('sec-' + s.id).hidden).map(s =>
          el('li', {}, el('a', {href: '#config', class: bad.has(s.id) ? 'bad' : '', onclick: e => { e.preventDefault(); document.getElementById('sec-' + s.id).scrollIntoView({behavior: 'smooth', block: 'start'}); }}, s.title)))));
      }
    });
    items.push(el('div', {class: 'sep'}), el('button', {type: 'button', class: 'doc', onclick: () => $('overview').scrollIntoView({behavior: 'smooth'})}, '服务器概览'));
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
  function renderBar() {
    const dirty = docs.filter(dirtyDoc);
    $('savebar').hidden = currentView !== 'config' || !dirty.length;
    if (!dirty.length) return;
    const problems = dirty.reduce((n, d) => n + ((checks[d.name] || {}).problems || []).length, 0);
    const count = dirty.reduce((n, d) => n + changes(d).length, 0);
    const text = $('savebar-text');
    text.textContent = problems ? `${problems} 处需要修正` : `${count} 处改动未保存` + (dirty.length > 1 ? `（${dirty.length} 个文件）` : '');
    text.classList.toggle('bad', !!problems);
    $('save').disabled = !!problems;
  }
  const show = v => v === undefined ? '（未设置）' : JSON.stringify(v);
  $('show-diff').onclick = () => {
    const list = el('ul', {class: 'diff'});
    docs.filter(dirtyDoc).forEach(d => changes(d).forEach(c => {
      const f = fieldFor(d, c.path);
      list.append(el('li', {}, d.schema.title + ' · ' + (f ? f.label : c.path), el('small', {}, c.path), el('div', {}, el('span', {class: 'from'}, show(c.from)), ' → ', el('span', {class: 'to'}, show(c.to)))));
    }));
    ask({title: '尚未保存的改动', body: '保存后需要重启服务端才会生效。', extra: list, ok: '知道了'});
  };
  $('discard').onclick = async () => {
    if (!await ask({title: '放弃全部改动？', body: '未保存的配置改动会被丢弃。', ok: '放弃', danger: true})) return;
    docs.forEach(d => { drafts[d.name] = clone(d.view.value); }); checks = {}; render(); renderBanners();
  };
  async function save() {
    const dirty = docs.filter(dirtyDoc);
    if (!dirty.length || $('save').disabled) return;
    const ok = await ask({title: '保存配置？', body: `将写入 ${dirty.map(d => d.file_name).join('、')}。改动在服务端重启后生效，重启前请确认配置无误。`, ok: '保存'});
    if (!ok) return;
    $('save').disabled = true;
    for (const d of dirty) {
      try {
        const {value} = await api('config/' + encodeURIComponent(d.name), {method: 'PUT', headers: {'If-Match': d.view.revision}, body: JSON.stringify(drafts[d.name], null, 2)});
        d.view = value; drafts[d.name] = clone(value.value); checks[d.name] = {problems: [], advice: value.advice || []};
        toast(d.schema.title + ' 已保存，重启服务端后生效');
      } catch (error) {
        if (error.status === 409) {
          const reload = await ask({title: '文件已被修改', body: `${d.file_name} 在你编辑期间被其他人或其他程序修改了。载入最新版本会丢弃你在这个文件上的改动。`, ok: '载入最新版本', danger: true});
          if (reload) { d.view = error.value.view; drafts[d.name] = clone(d.view.value); }
        } else if (error.status === 422) {
          checks[d.name] = {problems: error.value.problems || [], advice: []}; current = d.name; toast('有需要修正的地方');
        } else toast(d.schema.title + '：' + error.message);
      }
    }
    render(); renderBanners(); renderOverview();
  }
  $('save').onclick = save;
  document.addEventListener('keydown', event => {
    if (currentView === 'config' && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') { event.preventDefault(); save(); }
  });
  async function renderOverview() {
    try { server = (await api('server')).value; } catch { server = null; }
    if (!server) { $('overview').replaceChildren(); return; }
    const rows = [['服务端版本', server.server_version], ['游戏版本', server.game_version], ['GameData', server.game_data_version], ['资源包', server.bundle_version],
      ['认证模式', server.auth_mode === 'oauth' ? 'OAuth 联机' : '本地单人'], ['监听地址', server.listen], ['启动时间', server.started_at && new Date(server.started_at).toLocaleString('zh-CN')],
      ['数据目录', server.data_dir], ['邮件队列', server.mail_spool], ['开发覆盖', server.dev_tools]];
    $('overview').replaceChildren(el('h3', {}, '服务器概览'), el('dl', {}, rows.filter(r => r[1]).flatMap(([k, v]) => [el('dt', {}, k), el('dd', {}, /[\\/]/.test(v) ? el('code', {}, v) : v)])));
  }
  return {
    async open() {
      try {
        if (!docs.length) {
          docs = (await api('config/')).value.documents;
          docs.forEach(d => { if (drafts[d.name] === undefined) drafts[d.name] = clone(d.view.value); });
          current = current || (docs[0] && docs[0].name);
        }
        render(); renderBanners(); renderOverview();
      } catch (error) { $('config-form').replaceChildren(el('p', {class: 'status err'}, error.message)); }
    },
    dirty: () => docs.some(dirtyDoc),
    reset() { docs = []; drafts = {}; checks = {}; current = ''; },
  };
})();

boot();
