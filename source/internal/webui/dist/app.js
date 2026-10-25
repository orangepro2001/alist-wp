/**
 * app.js — baidupcs-web SPA(W4 骨架)。
 * 原生 ES Module + pv.js(极小响应式助手, 无构建)。hash 路由 7 页:
 *   #/files #/transfers #/share #/offline #/recycle #/settings
 * 公共: 顶栏(配额条 + 账号切换器占位) + fetch 封装(中文 toast); 无口令登录, 访问控制交给 tailscale serve。
 * W7 范围: 分享(创建/列表/取消/FR-28 转存)、离线任务、回收站三页接通; FR-40 直连下载面板 W8。
 */

/** api fetch 封装: JSON + 统一错误体(§4)。路径一律相对(api/...), 由 index.html 的 <base> 决定前缀。 */
async function api(path, opt = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    ...opt,
  });
  if (res.status === 409) {
    let b409 = null;
    try { b409 = await res.json(); } catch { /* ignore */ }
    const code = b409 && b409.code;
    if (code === 'BAIDU_NOT_LINKED') {
      // 未绑定百度账号: 留在当前页(顶栏提示"未绑定百度账号"), 只抛业务文案, 不跳页。
      throw new Error(b409.message || '尚未登录任何百度账号, 请点击右上角账号选择器旁的登录入口');
    }
    // 其他 409 保留业务错误文案, 不跳页
    throw new Error(b409 && b409.message ? b409.message : '请求冲突');
  }
  let body = null;
  try { body = await res.json(); } catch { /* 非JSON(如SSE) */ }
  if (!res.ok) {
    const msg = body && body.message ? body.message : `请求失败(HTTP ${res.status})`;
    throw new Error(msg);
  }
  return body;
}

const fmtSize = (n) => {
  if (n == null) return '-';
  if (n < 1024) return n + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n, i = -1;
  do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
  return v.toFixed(1) + ' ' + units[i];
};
const fmtTime = (ts) => ts ? new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false }) : '-';

/** hash 路由表: pattern → 渲染器(返回挂载所需 data)。 */
const routes = {
  '/files': renderFiles,
  '/transfers': renderTransfers,
  '/share': renderShare,
  '/offline': renderOffline,
  '/recycle': renderRecycle,
  '/settings': renderSettings,
};

function navigate() {
  const hash = location.hash || '#/files';
  const path = hash.replace(/^#/, '').split('?')[0] || '/files';
  const view = routes[path] || routes['/files'];
  view();
}

/* ---------------- 文件页 ---------------- */
function renderFiles() {
  main.innerHTML = '';
  const el = document.createElement('section');
  el.className = 'files-page';
  el.innerHTML = `
    <div class="toolbar">
      <div class="crumbs" id="crumbs"></div>
      <form id="search-form" class="search">
        <input type="search" name="kw" placeholder="搜索当前网盘(文件名)">
        <button type="submit">搜索</button>
        <button type="button" id="search-clear" hidden>退出搜索</button>
      </form>
    </div>
    <div class="listbar">
      <span id="files-count" class="muted"></span>
      <span class="spacer"></span>
      <label>排序
        <select id="sort-sel">
          <option value="name">名称</option>
          <option value="time">修改时间</option>
          <option value="size">大小</option>
        </select>
      </label>
      <label>方向
        <select id="order-sel">
          <option value="asc">升序</option>
          <option value="desc">降序</option>
        </select>
      </label>
      <button id="btn-newdir">新建文件夹</button>
      <button id="btn-upload">上传</button>
      <button id="btn-refresh">刷新</button>
    </div>
    <div id="files-body"></div>`;
  main.appendChild(el);

  const state = { dir: '/', sort: 'name', order: 'asc', page: 1, searching: false };

  const body = el.querySelector('#files-body');
  const crumbs = el.querySelector('#crumbs');

  async function load() {
    body.innerHTML = '<p class="muted">加载中…</p>';
    try {
      let data;
      if (state.searching) {
        data = await api('api/search?keyword=' + encodeURIComponent(state.keyword) + '&dir=/');
        body.innerHTML = buildTable(data.list, true);
      } else {
        const qs = new URLSearchParams({ dir: state.dir, sort: state.sort, order: state.order, page: state.page, pageSize: 100 });
        data = await api('api/files?' + qs);
        body.innerHTML = buildTable(data.list, false);
      }
      el.querySelector('#files-count').textContent =
        (state.searching ? `搜索到 ${data.total} 项` : `${data.dir} · 共 ${data.total} 项`);
      renderCrumbs();
      bindRows();
    } catch (ex) {
      body.innerHTML = `<p class="err">${ex.message}</p>`;
    }
  }

  function buildTable(list, isSearch) {
    if (!list || !list.length) return '<p class="muted empty">空目录</p>';
    const rows = list.map((f) => `
      <tr data-path="${f.path}" data-dir="${f.isDir}">
        <td class="icon">${f.isDir ? '📁' : '📄'}</td>
        <td class="name${f.isDir ? ' clickable' : ''}" title="${f.path}">${f.name}</td>
        <td class="num">${f.isDir ? '-' : fmtSize(f.size)}</td>
        <td class="num">${fmtTime(f.mtime)}</td>
        <td class="ops">
          ${f.isDir ? '' : '<button data-act="dl">下载</button>'}
          <button data-act="meta">属性</button>
          <button data-act="mv">移动</button>
          <button data-act="cp">复制</button>
          <button data-act="rm">删除</button>
        </td>
      </tr>`).join('');
    return `<table class="files-table">
      <thead><tr><th></th><th>名称</th><th>大小</th><th>修改时间</th><th>操作</th></tr></thead>
      <tbody>${rows}</tbody></table>`;
  }

  function renderCrumbs() {
    if (state.searching) { crumbs.innerHTML = `<b>搜索: ${state.keyword}</b>`; return; }
    const parts = state.dir.split('/').filter(Boolean);
    let html = `<a href="#/files" data-crumb="/">根目录</a>`;
    let acc = '';
    for (const p of parts) {
      acc += '/' + p;
      html += ` / <a href="#/files" data-crumb="${acc}">${p}</a>`;
    }
    crumbs.innerHTML = html;
    crumbs.querySelectorAll('a').forEach((a) =>
      a.addEventListener('click', (e) => { e.preventDefault(); go(a.dataset.crumb); }));
  }

  function bindRows() {
    body.querySelectorAll('tr[data-path]').forEach((tr) => {
      const p = tr.dataset.path;
      const isDir = tr.dataset.dir === 'true';
      tr.querySelector('.name')?.addEventListener('click', () => { if (isDir) go(p); });
      tr.querySelectorAll('button[data-act]').forEach((btn) =>
        btn.addEventListener('click', () => rowAction(btn.dataset.act, p, isDir)));
    });
  }

  async function rowAction(act, p, isDir) {
    try {
      if (act === 'meta') {
        const d = await api('api/meta?path=' + encodeURIComponent(p));
        const m = d.meta;
        alert(`${m.name}\n路径: ${m.path}\n大小: ${fmtSize(m.size)}\n类型: ${m.isDir ? '目录' : '文件'}\n修改: ${fmtTime(m.mtime)}`);
      } else if (act === 'dl') {
      // W5: /api/dl 签名 token 直链(30min), <a> 触发浏览器原生下载
      const d = await api('api/dl/url?path=' + encodeURIComponent(p));
      const a = document.createElement('a');
      a.href = d.url.replace(/^\//, '');
      a.download = '';
      document.body.appendChild(a);
      a.click();
      a.remove();
      toast('已开始下载');
      return;
    } else if (act === 'rm') {
        if (!confirm('确定删除 ' + p + ' ?')) return;
        await api('api/files/rm', { method: 'POST', body: JSON.stringify({ paths: [p] }) });
        toast('已删除');
      } else if (act === 'cp' || act === 'mv') {
        const to = prompt(act === 'cp' ? '复制到目录:' : '移动到目录:', state.dir);
        if (!to) return;
        await api('api/files/' + act, { method: 'POST', body: JSON.stringify({ paths: [p], to }) });
        toast(act === 'cp' ? '已复制' : '已移动');
      }
      load(); refreshTopbar();
    } catch (ex) { toast(ex.message, true); }
  }

  function go(dir) {
    state.searching = false;
    el.querySelector('#search-clear').hidden = true;
    state.dir = dir || '/';
    load();
  }

  el.querySelector('#sort-sel').addEventListener('change', (e) => { state.sort = e.target.value; load(); });
  el.querySelector('#order-sel').addEventListener('change', (e) => { state.order = e.target.value; load(); });
  el.querySelector('#btn-refresh').addEventListener('click', load);
  el.querySelector('#btn-newdir').addEventListener('click', async () => {
    const name = prompt('新文件夹名称(将创建于 ' + state.dir + '):');
    if (!name) return;
    try {
      await api('api/files/mkdir', { method: 'POST', body: JSON.stringify({ dir: state.dir.replace(/\/$/, '') + '/' + name }) });
      toast('已创建'); load();
    } catch (ex) { toast(ex.message, true); }
  });
  el.querySelector('#search-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const kw = e.target.kw.value.trim();
    if (!kw) return;
    state.searching = true; state.keyword = kw;
    el.querySelector('#search-clear').hidden = false;
    load();
  });
  el.querySelector('#search-clear').addEventListener('click', () => go(state.dir));

  // W6: 拖拽上传(FR-15): 浮层 + 三段式 /api/upload(init→chunk→complete); 目录拖拽支持 webkitRelativePath。
  const uploader = makeUploader(() => { load(); refreshTopbar(); });
  el.querySelector('#btn-upload')?.addEventListener('click', () => {
    const inp = document.createElement('input');
    inp.type = 'file'; inp.multiple = true;
    inp.onchange = () => uploader.uploadFiles(inp.files, state.dir);
    inp.click();
  });
  const overlay = document.createElement('div');
  overlay.className = 'drop-overlay'; overlay.hidden = true;
  overlay.innerHTML = '<div class="drop-box">松开鼠标上传到当前目录</div>';
  el.appendChild(overlay);
  let dragDepth = 0;
  el.addEventListener('dragenter', (e) => { e.preventDefault(); dragDepth++; overlay.hidden = false; });
  el.addEventListener('dragleave', (e) => { e.preventDefault(); if (--dragDepth <= 0) { dragDepth = 0; overlay.hidden = true; } });
  el.addEventListener('dragover', (e) => e.preventDefault());
  el.addEventListener('drop', (e) => {
    e.preventDefault(); dragDepth = 0; overlay.hidden = true;
    uploader.uploadFiles(e.dataTransfer.files, state.dir, e.dataTransfer.items);
  });
  window.addEventListener('beforeunload', () => uploader.abortAll());

  load();
}

/* ---------------- W6: 三段式上传客户端(FR-15/16) ---------------- */
/** makeUploader: 8MB 分片上传(并发 2), 完成后回调刷新; 返回 {uploadFiles, abortAll}。 */
function makeUploader(onDone) {
  const CHUNK = 8 * 1024 * 1024;
  const active = new Map(); // sessionId → abort controller

  async function uploadOne(file, dir, relPath) {
    const init = await api('api/upload/init', {
      method: 'POST',
      body: JSON.stringify({
        filename: file.name, rel_path: relPath || '', target_dir: dir,
        size: file.size, policy: '', no_rapid: false,
      }),
    });
    const sid = init.task_id, chunkSize = init.chunk_size || CHUNK;
    const ctrl = new AbortController();
    active.set(sid, ctrl);
    try {
      for (let seq = 0; seq * chunkSize < Math.max(file.size, 1); seq++) {
        const blob = file.slice(seq * chunkSize, (seq + 1) * chunkSize);
        // 分片体: 原始 body + X-Chunk-Seq(服务端 multipart/raw 均支持)
        const res = await fetch(`api/upload/${sid}?seq=${seq}`, {
          method: 'POST', body: blob, credentials: 'same-origin', signal: ctrl.signal,
          headers: { 'X-Chunk-Seq': String(seq), 'Content-Type': 'application/octet-stream' },
        });
        if (!res.ok) {
          const b = await res.json().catch(() => null);
          throw new Error(b && b.message ? b.message : `分片上传失败(HTTP ${res.status})`);
        }
      }
      const done = await api(`api/upload/${sid}/complete`, { method: 'POST' });
      toast(`「${file.name}」已提交传输${done.task_id ? '' : ''}`);
    } catch (ex) {
      if (ex.name === 'AbortError') { api(`api/upload/${sid}/abort`, { method: 'POST' }).catch(() => {}); return; }
      toast(`「${file.name}」上传失败: ${ex.message}`, true);
      api(`api/upload/${sid}/abort`, { method: 'POST' }).catch(() => {});
      throw ex;
    } finally {
      active.delete(sid);
    }
  }

  async function uploadFiles(fileList, dir, items) {
    const files = Array.from(fileList || []);
    if (!files.length) return;
    // webkitRelativePath(目录拖拽): 取第一级目录名作 rel_path 前缀
    const relOf = new Map();
    if (items && items.length && items[0].webkitGetAsEntry) {
      for (let i = 0; i < items.length; i++) {
        const entry = items[i].webkitGetAsEntry && items[i].webkitGetAsEntry();
        if (entry && entry.isDirectory) {
          const base = entry.name + '/';
          const stack = [entry];
          while (stack.length) {
            const e = stack.pop();
            const reader = e.createReader();
            const entries = await new Promise((res) => reader.readEntries(res, () => res([])));
            for (const c of entries) {
              if (c.isFile) relOf.set(c.fullPath || c.name, base + c.name);
              else stack.push(c);
            }
          }
        }
      }
    }
    let chain = Promise.resolve();
    for (const f of files) {
      chain = chain.then(() => uploadOne(f, dir, relOf.get(f.webkitRelativePath || '') || f.webkitRelativePath || ''));
    }
    try { await chain; onDone && onDone(); } catch { onDone && onDone(); }
  }

  function abortAll() {
    for (const [sid, ctrl] of active) { ctrl.abort(); active.delete(sid); }
  }
  return { uploadFiles, abortAll };
}

/* ---------------- 传输页(W5): 任务表 + SSE 实时进度 ---------------- */
const stateCN = { queued: '排队中', running: '进行中', done: '已完成', failed: '失败', cancelled: '已取消' };

function renderTransfers() {
  main.innerHTML = '';
  const el = document.createElement('section');
  el.className = 'transfers-page';
  el.innerHTML = `
    <div class="listbar">
      <span id="tf-count" class="muted"></span>
      <span class="spacer"></span>
      <label><input type="checkbox" id="tf-live" checked> SSE 实时</label>
      <button id="btn-tf-refresh">刷新</button>
    </div>
    <div id="tf-body"></div>`;
  main.appendChild(el);
  const body = el.querySelector('#tf-body');
  let es = null;

  function rowsHTML(list) {
    if (!list.length) return '<p class="muted empty">暂无传输任务。在文件页点击文件即可下载。</p>';
    return `<table class="files-table"><thead><tr>
      <th>文件</th><th>状态</th><th>进度</th><th>速度</th><th>大小</th><th>操作</th>
    </tr></thead><tbody>${list.map((t) => {
      const pct = t.total_size > 0 ? Math.min(100, Math.round((t.done_size / t.total_size) * 100)) : 0;
      return `<tr data-id="${t.id}">
        <td class="name" title="${t.path}">${t.name}</td>
        <td class="state-${t.state}">${stateCN[t.state] || t.state}${t.error ? ' · ' + t.error : ''}</td>
        <td class="num">${pct}%</td>
        <td class="num">${t.state === 'running' ? fmtSize(t.speed_bps) + '/s' : '-'}</td>
        <td class="num">${t.done_size > 0 ? fmtSize(t.done_size) + ' / ' + (fmtSize(t.total_size) || '?') : '-'}</td>
        <td class="ops">
          ${t.state === 'queued' || t.state === 'running' ? `<button data-act="cancel">取消</button>` : ''}
          ${t.state === 'done' || t.state === 'failed' || t.state === 'cancelled' ? `<button data-act="purge">清除</button>` : ''}
        </td></tr>`;
    }).join('')}</tbody></table>`;
  }

  async function load() {
    try {
      const d = await api('api/tasks');
      el.querySelector('#tf-count').textContent = `共 ${d.total} 个任务`;
      body.innerHTML = rowsHTML(d.list);
      bindOps();
    } catch (ex) { body.innerHTML = `<p class="err">${ex.message}</p>`; }
  }

  function bindOps() {
    body.querySelectorAll('button[data-act]').forEach((btn) =>
      btn.addEventListener('click', async () => {
        const id = btn.closest('tr').dataset.id;
        try {
          await api(`api/tasks/${id}/${btn.dataset.act}`, { method: 'POST' });
          load();
        } catch (ex) { toast(ex.message, true); }
      }));
  }

  function connectSSE() {
    if (es) { es.close(); es = null; }
    if (!el.querySelector('#tf-live').checked) return;
    es = new EventSource('api/events');
    es.addEventListener('task', () => { /* 进度节流刷新(≤500ms) */ load(); });
    es.onerror = () => { /* EventSource 自动重连 */ };
  }

  el.querySelector('#btn-tf-refresh').addEventListener('click', load);
  el.querySelector('#tf-live').addEventListener('change', connectSSE);
  window.addEventListener('hashchange', () => {
    if (!(location.hash || '').startsWith('#/transfers') && es) { es.close(); es = null; }
  }, { once: true });

  load();
  connectSSE();
}

/* ---------------- 分享页(W7): 创建分享 + 分享列表/取消 + FR-28 转存框 ---------------- */
function renderShare() {
  main.innerHTML = '';
  const el = document.createElement('section');
  el.className = 'share-page';
  el.innerHTML = `
    <div class="card share-transfer-card">
      <h1>转存分享链接</h1>
      <p class="muted">支持完整链接 / 短链 / ?pwd= / 整段口令文本; 转存到当前目录「/」并保留文件(与上游一致)。</p>
      <form id="transfer-form">
        <input type="text" name="link" placeholder="粘贴分享链接或含提取码的口令文本" required>
        <input type="text" name="passcode" placeholder="提取码(文本已含时可留空)" maxlength="4">
        <button type="submit" class="primary">转存</button>
      </form>
    </div>
    <div class="card" id="direct-dl-card">
      <h1>分享直连下载</h1>
      <p class="muted">粘贴分享链接浏览内容并直接下载。实现机制为<b>临时转存</b>: 会短暂占用网盘配额, 下载完成后自动删除临时目录, 不在网盘留存。</p>
      <form id="dl-preview-form">
        <input type="text" name="link" placeholder="粘贴分享链接或口令文本" required>
        <input type="text" name="passcode" placeholder="提取码(文本已含时可留空)" maxlength="4">
        <button type="submit" class="primary">浏览并直连下载</button>
      </form>
      <div id="dl-preview-body" hidden></div>
      <div id="dl-direct-links"></div>
    </div>
    <div class="card">
      <h1>我的分享</h1>
      <div class="listbar">
        <span id="share-count" class="muted"></span>
        <span class="spacer"></span>
        <button id="btn-newshare">创建分享</button>
        <button id="btn-share-refresh">刷新</button>
      </div>
      <div id="share-body"></div>
    </div>`;
  main.appendChild(el);
  const body = el.querySelector('#share-body');

  async function load() {
    body.innerHTML = '<p class="muted">加载中…</p>';
    try {
      const d = await api('api/share/list?page=1');
      el.querySelector('#share-count').textContent = `共 ${d.total} 条`;
      if (!d.list.length) { body.innerHTML = '<p class="muted empty">暂无分享记录。</p>'; return; }
      body.innerHTML = `<table class="files-table"><thead><tr>
        <th><input type="checkbox" id="share-all"></th><th>分享链接</th><th>路径</th><th>状态</th><th>有效期至</th><th>浏览</th><th>操作</th>
      </tr></thead><tbody>${d.list.map((s) => {
        const link = s.shortlink || '';
        let valid = '永久';
        if (s.expire_type === -1) valid = '<span class="err">已过期</span>';
        else if (s.expire_time > 0) valid = fmtTime(Math.floor(Date.now() / 1000) + s.expire_time);
        return `<tr data-id="${s.share_id}">
          <td><input type="checkbox" class="share-ck" value="${s.share_id}"></td>
          <td class="name"><a href="${link}" target="_blank" rel="noopener">${link || '-'}</a></td>
          <td class="name" title="${s.typical_path}">${s.typical_path}</td>
          <td class="num">${s.status === 0 ? '有效' : s.status}</td>
          <td class="num">${valid}</td>
          <td class="num">${s.view_count}</td>
          <td class="ops"><button data-act="copy">复制链接</button><button data-act="cancel">取消</button></td>
        </tr>`;
      }).join('')}</tbody></table>`;
      body.querySelector('#share-all').addEventListener('change', (e) =>
        body.querySelectorAll('.share-ck').forEach((ck) => { ck.checked = e.target.checked; }));
      body.querySelectorAll('button[data-act]').forEach((btn) =>
        btn.addEventListener('click', async () => {
          const id = Number(btn.closest('tr').dataset.id);
          try {
            if (btn.dataset.act === 'copy') {
              const link = btn.closest('tr').querySelector('a').href;
              await navigator.clipboard.writeText(link).catch(() => {});
              toast('链接已复制');
            } else {
              if (!confirm('确定取消该分享? 取消后链接立即失效。')) return;
              await api('api/share/cancel', { method: 'POST', body: JSON.stringify({ share_ids: [id] }) });
              toast('已取消分享'); load();
            }
          } catch (ex) { toast(ex.message, true); }
        }));
    } catch (ex) { body.innerHTML = `<p class="err">${ex.message}</p>`; }
  }

  el.querySelector('#btn-share-refresh').addEventListener('click', load);
  el.querySelector('#btn-newshare').addEventListener('click', () => {
    const paths = prompt('要分享的网盘路径(多个用英文逗号分隔, 如 /a.txt,/b):');
    if (!paths) return;
    const password = prompt('提取码(4 位字母数字, 留空则自动随机):', '') || '';
    const period = Number(prompt('有效期天数: 1 / 7 / 30 / 0(永久)', '7') || '7');
    api('api/share/set', { method: 'POST', body: JSON.stringify({
      paths: paths.split(',').map((s) => s.trim()).filter(Boolean), password, period }) })
      .then((d) => {
        const full = d.password ? `${d.link}?pwd=${d.password}` : d.link;
        navigator.clipboard.writeText(full).catch(() => {});
        alert(`分享创建成功(share_id=${d.share_id})\n链接: ${full}\n已复制到剪贴板`);
        load();
      })
      .catch((ex) => toast(ex.message, true));
  });

  el.querySelector('#transfer-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      await api('api/transfer', { method: 'POST', body: JSON.stringify({
        link: e.target.link.value.trim(), passcode: e.target.passcode.value.trim() }) });
      toast('转存成功, 文件已保存到 /');
      e.target.reset();
    } catch (ex) { toast(ex.message, true); }
  });

  // W8: FR-40 直连下载面板(浏览→勾选→启动→状态轮询)
  const dlBody = el.querySelector('#dl-preview-body');
  el.querySelector('#dl-preview-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const d = await api('api/share/preview', { method: 'POST', body: JSON.stringify({
        link: e.target.link.value.trim(), passcode: e.target.passcode.value.trim() }) });
      window.__dlJob = { jobId: d.job_id, link: e.target.link.value.trim(), passcode: e.target.passcode.value.trim() };
      const items = d.items || [];
      window.__dlItems = items;
      dlBody.hidden = false;
      dlBody.innerHTML = items.length
        ? `<div class="listbar"><span class="muted">分享内容(${items.length} 项, 短暂占用配额)</span>
           <span class="spacer"></span><button id="btn-dl-start" class="primary">下载勾选项</button></div>
           <table class="files-table"><tbody>${items.map((it) => `
             <tr><td><input type="checkbox" class="dl-ck" value="${it.fs_id}" checked></td>
             <td class="name">${it.filename || it.fs_id}</td><td class="num">${fmtSize(it.size)}</td></tr>`).join('')}</tbody></table>`
        : `<p class="muted">分享内容为空或无文件清单(share_id=${d.share_id}), 可直接尝试下载全部。</p>
           <button id="btn-dl-start" class="primary">下载全部</button>`;
      dlBody.querySelector('#btn-dl-start')?.addEventListener('click', async () => {
        const fsids = Array.from(dlBody.querySelectorAll('.dl-ck:checked')).map((ck) => ck.value);
        try {
          await api('api/share/dl', { method: 'POST', body: JSON.stringify({
            job_id: window.__dlJob.jobId, fs_ids: fsids.length ? fsids : items.map((it) => it.fs_id) }) });
          toast('直连下载已启动: 短暂占用网盘配额, 完成后自动清理临时目录');
          pollDL();
        } catch (ex) { toast(ex.message, true); }
      });
    } catch (ex) { toast(ex.message, true); }
  });

  let dlTimer = null;
  async function pollDL() {
    try {
      const d = await api('api/share/dl');
      const active = (d.list || []).filter((j) => !['done', 'failed'].includes(j.phase));
      // B5: 作业 downloading 时对每个文件签发签名直链, 浏览器 <a> 直下真实内容。
      // path 取 /api/share/dl 响应的 files[](转存后网盘路径; preview 阶段 path 尚不存在)。
      const dlArea = document.querySelector('#dl-direct-links');
      if (dlArea) {
        const rows = (d.list || []).filter((j) => j.phase === 'downloading' || j.phase === 'cleaning');
        dlArea.innerHTML = rows.length
          ? `<p class="muted">转存完成, 点击下载(服务器清理临时目录前有效):</p><ul>${
              rows.map((j) => (j.files || []).map((f) =>
                `<li><a href="#" data-job="${j.id}" data-path="${encodeURIComponent(f.path)}" class="dl-file-link">${f.filename || f.path}</a></li>`
              ).join('')).join('')}</ul>`
          : '';
        dlArea.querySelectorAll('a.dl-file-link').forEach((a) => {
          a.addEventListener('click', async (e) => {
            e.preventDefault();
            try {
              // path 来自服务端作业清单(HandleListDL files), /api/share/dl/url 校验 path 在作业清单内
              const r = await api(`api/share/dl/url?job_id=${encodeURIComponent(a.dataset.job)}&path=${a.dataset.path}`);
              window.location.href = r.url.replace(/^\//, '');
            } catch (ex) { toast(ex.message, true); }
          });
        });
      }
      if (active.length) {
        clearTimeout(dlTimer);
        dlTimer = setTimeout(pollDL, 1500);
      }
    } catch { /* 静默 */ }
  }

  load();
}

/* ---------------- 离线下载页(W7): 任务列表 + 新建磁力/HTTP 任务 ---------------- */
const offlineStateCN = { 0: '成功', 1: '进行中', 2: '系统错误', 3: '资源不存在', 4: '超时', 5: '失败', 6: '空间不足', 7: '已取消' };

function renderOffline() {
  main.innerHTML = '';
  const el = document.createElement('section');
  el.className = 'offline-page';
  el.innerHTML = `
    <div class="card">
      <h1>新建离线任务</h1>
      <p class="muted">支持 http/https/ftp/电驴/磁力链, 每行一个地址; 保存路径默认根目录。</p>
      <form id="offline-form">
        <textarea name="urls" rows="3" placeholder="magnet:?xt=urn:btih:...&#10;http://example.com/file.zip" required></textarea>
        <input type="text" name="path" placeholder="保存路径(默认 /)">
        <button type="submit" class="primary">添加任务</button>
      </form>
    </div>
    <div class="card">
      <h1>离线任务列表</h1>
      <div class="listbar">
        <span id="offline-count" class="muted"></span>
        <span class="spacer"></span>
        <label><input type="checkbox" id="offline-auto" checked> 每 5 秒自动刷新</label>
        <button id="btn-offline-refresh">刷新</button>
      </div>
      <div id="offline-body"></div>
    </div>`;
  main.appendChild(el);
  const body = el.querySelector('#offline-body');
  let timer = null;

  async function load() {
    try {
      const d = await api('api/offline/list');
      el.querySelector('#offline-count').textContent = `共 ${d.total} 个任务`;
      if (!d.list.length) { body.innerHTML = '<p class="muted empty">暂无离线任务。</p>'; return; }
      body.innerHTML = `<table class="files-table"><thead><tr>
        <th>任务</th><th>状态</th><th>进度</th><th>大小</th><th>保存路径</th><th>操作</th>
      </tr></thead><tbody>${d.list.map((t) => `
        <tr data-id="${t.task_id}">
          <td class="name" title="${t.source_url || ''}">${t.task_name || t.task_id}</td>
          <td class="num">${offlineStateCN[t.status] || t.status_text || t.status}</td>
          <td class="num">${t.status === 1 ? (t.progress || 0) + '%' : '-'}</td>
          <td class="num">${t.file_size ? fmtSize(t.finished_size) + ' / ' + fmtSize(t.file_size) : '-'}</td>
          <td class="name" title="${t.save_path}">${t.save_path}</td>
          <td class="ops">
            ${t.status === 1 ? '<button data-act="cancel">取消</button>' : ''}
            <button data-act="delete">删除</button>
          </td>
        </tr>`).join('')}</tbody></table>`;
      body.querySelectorAll('button[data-act]').forEach((btn) =>
        btn.addEventListener('click', async () => {
          const id = Number(btn.closest('tr').dataset.id);
          const act = btn.dataset.act;
          try {
            if (act === 'cancel') {
              await api('api/offline/cancel', { method: 'POST', body: JSON.stringify({ task_id: id }) });
              toast('已取消');
            } else {
              if (!confirm('确定删除该任务记录?')) return;
              await api('api/offline/delete', { method: 'POST', body: JSON.stringify({ task_id: id }) });
              toast('已删除');
            }
            load();
          } catch (ex) { toast(ex.message, true); }
        }));
    } catch (ex) { body.innerHTML = `<p class="err">${ex.message}</p>`; }
  }

  function setAuto(on) {
    clearInterval(timer);
    if (on) timer = setInterval(load, 5000);
  }
  el.querySelector('#offline-auto').addEventListener('change', (e) => setAuto(e.target.checked));
  el.querySelector('#btn-offline-refresh').addEventListener('click', load);
  window.addEventListener('hashchange', () => {
    if (!(location.hash || '').startsWith('#/offline')) setAuto(false);
  }, { once: true });

  el.querySelector('#offline-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const urls = e.target.urls.value.split('\n').map((s) => s.trim()).filter(Boolean);
    if (!urls.length) return;
    try {
      const d = await api('api/offline/add', { method: 'POST', body: JSON.stringify({
        urls, path: e.target.path.value.trim() }) });
      const failMsg = d.failed.length ? `\n失败 ${d.failed.length} 条:\n` + d.failed.map((f) => `${f.url}: ${f.message}`).join('\n') : '';
      toast(`已添加 ${d.added.length} 条离线任务${d.failed.length ? ', 部分失败' : ''}${d.failed.length ? '' : ''}`);
      if (d.failed.length) alert(failMsg);
      e.target.reset(); load();
    } catch (ex) { toast(ex.message, true); }
  });

  load();
  setAuto(true);
}

/* ---------------- 回收站页(W7): 列表 + 还原/删除/清空(高危二次确认) ---------------- */
function renderRecycle() {
  main.innerHTML = '';
  const el = document.createElement('section');
  el.className = 'recycle-page';
  el.innerHTML = `
    <div class="card">
      <h1>回收站</h1>
      <div class="listbar">
        <span id="recycle-count" class="muted"></span>
        <span class="spacer"></span>
        <button id="btn-restore">还原选中</button>
        <button id="btn-delete">删除选中</button>
        <input type="text" id="clean-confirm" placeholder="输入「清空」以启用" size="14">
        <button id="btn-clean" disabled>清空回收站</button>
        <button id="btn-recycle-refresh">刷新</button>
      </div>
      <div id="recycle-body"></div>
    </div>`;
  main.appendChild(el);
  const body = el.querySelector('#recycle-body');
  const cleanInput = el.querySelector('#clean-confirm');
  const cleanBtn = el.querySelector('#btn-clean');
  cleanInput.addEventListener('input', () => { cleanBtn.disabled = cleanInput.value !== '清空'; });

  function checkedIDs() {
    return Array.from(body.querySelectorAll('.recycle-ck:checked')).map((ck) => Number(ck.value));
  }

  async function load() {
    body.innerHTML = '<p class="muted">加载中…</p>';
    try {
      const d = await api('api/recycle/list?page=1');
      el.querySelector('#recycle-count').textContent = `共 ${d.total} 项`;
      if (!d.list.length) { body.innerHTML = '<p class="muted empty">回收站为空。</p>'; return; }
      body.innerHTML = `<table class="files-table"><thead><tr>
        <th><input type="checkbox" id="recycle-all"></th><th>名称</th><th>原路径</th><th>大小</th><th>剩余保留天数</th><th>删除时间</th>
      </tr></thead><tbody>${d.list.map((f) => `
        <tr>
          <td><input type="checkbox" class="recycle-ck" value="${f.fs_id}"></td>
          <td class="name">${f.isDir ? '📁' : '📄'} ${f.name}</td>
          <td class="name" title="${f.path}">${f.path}</td>
          <td class="num">${f.isDir ? '-' : fmtSize(f.size)}</td>
          <td class="num">${f.left_time}</td>
          <td class="num">${fmtTime(f.mtime)}</td>
        </tr>`).join('')}</tbody></table>`;
      body.querySelector('#recycle-all').addEventListener('change', (e) =>
        body.querySelectorAll('.recycle-ck').forEach((ck) => { ck.checked = e.target.checked; }));
    } catch (ex) { body.innerHTML = `<p class="err">${ex.message}</p>`; }
  }

  async function doRestore() {
    const ids = checkedIDs();
    if (!ids.length) { toast('请先勾选要还原的文件', true); return; }
    if (!confirm(`确定还原选中的 ${ids.length} 项到原位置?`)) return;
    try {
      await api('api/recycle/restore', { method: 'POST', body: JSON.stringify({ fs_ids: ids }) });
      toast('已还原'); load();
    } catch (ex) { toast(ex.message, true); }
  }
  async function doDelete() {
    const ids = checkedIDs();
    if (!ids.length) { toast('请先勾选要删除的文件', true); return; }
    if (!confirm(`确定从回收站删除选中的 ${ids.length} 项? 此操作不可恢复!`)) return;
    try {
      await api('api/recycle/delete', { method: 'POST', body: JSON.stringify({ fs_ids: ids }) });
      toast('已删除'); load();
    } catch (ex) { toast(ex.message, true); }
  }

  el.querySelector('#btn-restore').addEventListener('click', doRestore);
  el.querySelector('#btn-delete').addEventListener('click', doDelete);
  cleanBtn.addEventListener('click', async () => {
    if (cleanInput.value !== '清空') return;
    if (!confirm('即将清空整个回收站, 百度侧不可恢复, 确定继续?')) return;
    try {
      await api('api/recycle/delete', { method: 'POST', body: JSON.stringify({ all: true }) });
      toast('回收站已清空'); cleanInput.value = ''; cleanBtn.disabled = true; load();
    } catch (ex) { toast(ex.message, true); }
  });
  el.querySelector('#btn-recycle-refresh').addEventListener('click', load);

  load();
}

/* ---------------- 设置页(W9: FR-34 白名单配置 + FR-35 工具 + FR-36 状态) ---------------- */
function renderSettings() {
  main.innerHTML = '';
  const el = document.createElement('section');
  el.className = 'card';
  el.innerHTML = `
    <h1>设置</h1>
    <h2>服务状态</h2>
    <pre id="status-box" class="muted">加载中…</pre>

    <h2>传输参数(白名单可写项)</h2>
    <form id="cfg-form"></form>
    <p class="muted">保存即生效(对后续任务), 已持久化到服务端 config.json。</p>

    <h2>风控敏感只读项(不可修改, 改动会导致百度接口 403)</h2>
    <details><summary>展开只读区</summary><pre id="ro-box" class="muted"></pre></details>

    <h2>通配符测试(match, 只读调试)</h2>
    <form id="match-form">
      <input name="pattern" placeholder="/我的资源/*.mp4" required style="min-width:300px">
      <button type="submit" class="primary">测试</button>
    </form>
    <pre id="match-out" class="muted"></pre>

    <h2>文件加解密(tool, 上游同款 aes-ctr/cfb/ofb)</h2>
    <form id="cipher-form">
      <select name="mode"><option value="encrypt">加密</option><option value="decrypt">解密</option></select>
      <select name="method">
        ${['aes-128-ctr','aes-192-ctr','aes-256-ctr','aes-128-cfb','aes-192-cfb','aes-256-cfb','aes-128-ofb','aes-192-ofb','aes-256-ofb']
          .map((m) => `<option>${m}</option>`).join('')}
      </select>
      <input name="key" placeholder="密钥" required>
      <input name="path" placeholder="服务器本地文件绝对路径" required style="min-width:280px">
      <button type="submit" class="primary">执行</button>
    </form>
    <pre id="cipher-out" class="muted"></pre>
  `;
  main.appendChild(el);

  // --- FR-36 状态(版本/上游基线/运行时长/临时目录占用) ---
  const fmtDur = (s) => {
    const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
    return (d ? d + '天' : '') + (h || d ? h + '小时' : '') + m + '分钟';
  };
  api('api/status').then((st) => {
    el.querySelector('#status-box').textContent =
      `版本: ${st.version}  (上游: ${st.upstream})\n运行时长: ${fmtDur(st.uptime_second)}` +
      `\n临时目录: ${st.tmp_dir}  占用: ${fmtSize(st.tmp_usage)}${st.tmp_usage_err ? '(' + st.tmp_usage_err + ')' : ''}` +
      `\n磁盘剩余: ${fmtSize(st.disk_free)}`;
  }).catch((ex) => { el.querySelector('#status-box').textContent = '状态获取失败: ' + ex.message; });

  // --- FR-34 白名单表单 + 只读区 ---
  const CFG_LABELS = {
    max_download_rate: '下载限速(B/s, 0=不限)', max_upload_rate: '上传限速(B/s, 0=不限)',
    max_parallel: '下载分片并发', max_download_load: '同时下载文件数',
    max_upload_load: '同时上传文件数', max_upload_parallel: '上传单文件分片并发',
    u_policy: '同名策略(skip/overwrite/rsync)', tmp_dir: '上传临时目录(绝对路径)',
    user_agent: 'user_agent(高级)',
  };
  api('api/config').then((cfg) => {
    const form = el.querySelector('#cfg-form');
    form.innerHTML = Object.entries(cfg.writable).map(([k, v]) => `
      <label style="display:block;margin:6px 0">${CFG_LABELS[k] || k}
        <input name="${k}" value="${String(v).replace(/"/g, '&quot;')}" style="min-width:260px"></label>`).join('')
      + '<button type="submit" class="primary">保存配置</button>';
    el.querySelector('#ro-box').textContent = Object.entries(cfg.readonly)
      .map(([k, v]) => k + ' = ' + v).join('\n');
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      try {
        for (const input of form.querySelectorAll('input[name]')) {
          const v = input.value.trim();
          if (v === String(cfg.writable[input.name])) continue; // 未变更跳过
          const num = Number(v);
          await api('api/config/set', {
            method: 'POST',
            body: JSON.stringify({ key: input.name, value: v !== '' && !isNaN(num) && /^-?\d+(\.\d+)?$/.test(v) ? num : v }),
          });
        }
        toast('配置已保存');
      } catch (ex) { toast(ex.message, true); }
    });
  }).catch((ex) => toast('配置读取失败: ' + ex.message, true));

  // --- FR-35 match ---
  el.querySelector('#match-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const r = await api('api/match', { method: 'POST', body: JSON.stringify({ pattern: e.target.pattern.value }) });
      el.querySelector('#match-out').textContent = r.list.length ? r.list.join('\n') : '(无匹配)';
    } catch (ex) { el.querySelector('#match-out').textContent = '失败: ' + ex.message; }
  });

  // --- FR-35 tool 加解密 ---
  el.querySelector('#cipher-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const r = await api('api/tool/cipher', {
        method: 'POST',
        body: JSON.stringify({
          mode: e.target.mode.value, method: e.target.method.value,
          key: e.target.key.value, path: e.target.path.value,
        }),
      });
      el.querySelector('#cipher-out').textContent = `完成: ${r.output} (${fmtSize(r.output_size)})`;
    } catch (ex) { el.querySelector('#cipher-out').textContent = '失败: ' + ex.message; }
  });
}

/* ---------------- 占位页(W9 设置页扩展等未接入模块预留) ---------------- */
function renderPlaceholder(title, desc) {
  return () => {
    main.innerHTML = '';
    const el = document.createElement('section');
    el.className = 'card';
    el.innerHTML = `<h1>${title}</h1><p class="muted">${desc}</p><p class="empty">此模块在后续波次接入。</p>`;
    main.appendChild(el);
  };
}

/* ---------------- 顶栏: 配额 + 账号 ---------------- */
const topbar = document.getElementById('topbar');
const main = document.getElementById('main');

async function refreshTopbar() {
  try {
    const q = await api('api/quota');
    const pct = q.quota > 0 ? Math.min(100, Math.round((q.used / q.quota) * 100)) : 0;
    topbar.querySelector('#quota-bar').innerHTML = `
      <div class="quota-track"><div class="quota-fill" style="width:${pct}%"></div></div>
      <span class="quota-text">${fmtSize(q.used)} / ${fmtSize(q.quota)} (${pct}%)</span>`;
  } catch { /* 未绑定百度账号时静默, 顶栏另有提示 */ }
  try {
    const a = await api('api/accounts');
    topbar.querySelector('#account-sel').innerHTML = a.accounts.length
      ? a.accounts.map((x) => `<option value="${x.uid}" ${x.active ? 'selected' : ''}>${x.name}</option>`).join('')
      : '<option>未绑定百度账号</option>';
  } catch { /* 同上 */ }
}

topbar.querySelector('#account-sel').addEventListener('change', async (e) => {
  try {
    await api(`api/accounts/${e.target.value}/switch`, { method: 'POST' });
    toast('账号已切换');
    if ((location.hash || '#/files').startsWith('#/files')) navigate(); else refreshTopbar();
  } catch (ex) { toast(ex.message, true); }
});

/* ---------------- 绑定百度账号(FR-01): BDUSS+STOKEN 或整串 Cookie ---------------- */
topbar.querySelector('#btn-add-account').addEventListener('click', () => openAccountDialog());

function openAccountDialog() {
  let ov = document.getElementById('acc-overlay');
  if (ov) { ov.remove(); }
  ov = document.createElement('div');
  ov.id = 'acc-overlay';
  ov.className = 'modal-overlay';
  ov.innerHTML = `
  <div class="card modal-card">
    <h2>绑定百度账号</h2>
    <p class="muted">两种方式任选其一。凭证仅保存在本服务器(config 0600), 不会出现在浏览器。</p>
    <p class="muted">获取方法: 电脑浏览器登录 pan.baidu.com → F12 → 应用/Application → Cookie →
      复制 <b>BDUSS</b> 与 <b>STOKEN</b>(分享/秒传需要); 或直接复制整串 Cookie。</p>
    <label>方式一: 整串 Cookie <input id="acc-cookie" type="password" placeholder="BDUSS=xxx; STOKEN=xxx; ..." autocomplete="off"></label>
    <label>方式二: BDUSS <input id="acc-bduss" type="password" autocomplete="off"></label>
    <label>STOKEN <input id="acc-stoken" type="password" autocomplete="off"></label>
    <p id="acc-err" class="err" hidden></p>
    <div class="modal-actions">
      <button id="acc-cancel">取消</button>
      <button id="acc-save" class="primary">绑定</button>
    </div>
  </div>`;
  document.body.appendChild(ov);
  ov.querySelector('#acc-cancel').addEventListener('click', () => ov.remove());
  ov.addEventListener('click', (e) => { if (e.target === ov) ov.remove(); });
  ov.querySelector('#acc-save').addEventListener('click', async () => {
    const errEl = ov.querySelector('#acc-err');
    errEl.hidden = true;
    const cookie = ov.querySelector('#acc-cookie').value.trim();
    const bduss = ov.querySelector('#acc-bduss').value.trim();
    const stoken = ov.querySelector('#acc-stoken').value.trim();
    let body = null;
    if (cookie) body = { cookie };
    else if (bduss) body = { bduss, stoken };
    if (!body) { errEl.textContent = '请填写整串 Cookie 或 BDUSS(方式二须同时填 STOKEN)'; errEl.hidden = false; return; }
    if (bduss && !stoken) { errEl.textContent = 'BDUSS 方式 STOKEN 必填(分享与秒传需要)'; errEl.hidden = false; return; }
    try {
      const r = await api('api/accounts/add', { method: 'POST', body: JSON.stringify(body) });
      ov.remove();
      toast('已绑定账号 ' + (r.name || r.uid || ''));
      await refreshTopbar();
      if ((location.hash || '#/files').startsWith('#/files')) navigate();
    } catch (ex) { errEl.textContent = ex.message; errEl.hidden = false; }
  });
}

/* ---------------- toast ---------------- */
let toastTimer = null;
function toast(msg, isErr = false) {
  let t = document.getElementById('toast');
  if (!t) {
    t = document.createElement('div');
    t.id = 'toast';
    document.body.appendChild(t);
  }
  t.textContent = msg;
  t.className = isErr ? 'err' : 'ok';
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 2600);
}

/* ---------------- 启动 ---------------- */
window.addEventListener('hashchange', navigate);
navigate();
refreshTopbar();
