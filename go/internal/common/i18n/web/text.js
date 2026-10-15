'use strict';
(() => {
  let lookup = key => key, locale = () => 'en_us', formatURL = '';
  const cache = new Map(), pending = new Map();
  const CACHE_LIMIT = 2048, BATCH_LIMIT = 128;
  let scheduled = false;
  async function flush() {
    scheduled = false;
    const groups = new Map();
    for (const request of pending.values()) {
      if (request.started) continue;
      request.started = true;
      const group = groups.get(request.locale) || [];
      group.push(request); groups.set(request.locale, group);
    }
    await Promise.all([...groups].flatMap(([language, requests]) => {
      const batches = [];
      for (let start = 0; start < requests.length; start += BATCH_LIMIT) {
        const batch = requests.slice(start, start + BATCH_LIMIT);
        batches.push((async () => {
          try {
            const response = await fetch(formatURL, {
              method: 'POST', credentials: 'same-origin', signal: AbortSignal.timeout(15000), headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({locale: language, messages: batch.map(r => ({key: r.key, args: r.args}))}),
            });
            if (!response.ok) throw new Error('Text formatting failed (HTTP ' + response.status + ')');
            const value = await response.json();
            if (!Array.isArray(value.texts) || value.texts.length !== batch.length || value.texts.some(text => typeof text !== 'string')) throw new Error('Invalid text formatting response');
            batch.forEach((request, index) => {
              cache.set(request.id, value.texts[index]);
              while (cache.size > CACHE_LIMIT) cache.delete(cache.keys().next().value);
              request.resolve(value.texts[index]);
            });
          } catch (error) { batch.forEach(request => request.reject(error)); }
          finally { batch.forEach(request => pending.delete(request.id)); }
        })());
      }
      return batches;
    }));
  }
  function requestFormattedText(language, key, args) {
    const id = JSON.stringify([language, key, args]);
    if (cache.has(id)) { const text = cache.get(id); cache.delete(id); cache.set(id, text); return Promise.resolve(text); }
    if (pending.has(id)) return pending.get(id).promise;
    const request = {id, locale: language, key, args};
    request.promise = new Promise((resolve, reject) => { request.resolve = resolve; request.reject = reject; });
    pending.set(id, request);
    if (!scheduled) { scheduled = true; queueMicrotask(flush); }
    return request.promise;
  }
  class Component {
    #content; #args;
    constructor(content, args) { this.#content = content; this.#args = args; }
    static configure(options) { lookup = options.lookup; locale = options.locale; formatURL = options.formatURL; cache.clear(); }
    static literal(text) { if (text == null) throw new TypeError('text is required'); return new Component(String(text), null); }
    static translatable(key, ...args) { if (key == null) throw new TypeError('key is required'); return new Component(String(key), args); }
    async getString() {
      if (this.#args === null) return this.#content;
      if (!this.#args.length) return lookup(this.#content) ?? this.#content;
      for (;;) {
        const language = locale();
        const args = await Promise.all(this.#args.map(value => value && typeof value.getString === 'function' ? value.getString() : value));
        const text = await requestFormattedText(language, this.#content, args);
        if (language === locale()) return text;
      }
    }
  }
  window.BD2Text = {Component};
})();
