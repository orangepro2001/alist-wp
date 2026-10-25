/* bind-baidu.js — 注入到 OpenList 页面(设置 customize_body)的"绑定百度账号"入口。
 *
 * 做的事: 页面左下角一个"绑定百度账号"按钮, 点开弹窗填整串 Cookie(或 BDUSS+STOKEN),
 * 然后用 OpenList 自己的管理 API 把 Cookie 写进指向 baidupcs-web 的 WebDAV 存储的"密码"栏
 * (baidupcs-web 把该密码当百度 Cookie 用), 并立刻列一次根目录验证。
 * 全程同源(OpenList 自己的 /api), 不需要额外的路径或端口; 不是管理员时弹窗里会要管理员密码。
 * __DAV_ADDRESS__ 由 install.sh 替换为 baidupcs-web 的 WebDAV 地址。
 */
(function () {
  if (window.__bpwBind) return;
  window.__bpwBind = true;

  var BASE = (window.OPENLIST_CONFIG && window.OPENLIST_CONFIG.base_path) || '';
  var DAV = '__DAV_ADDRESS__'.replace(/\/+$/, '');
  var adminToken = null;

  function el(tag, attrs, kids) {
    var n = document.createElement(tag);
    Object.keys(attrs || {}).forEach(function (k) {
      if (k === 'style') n.style.cssText = attrs[k];
      else if (k === 'text') n.textContent = attrs[k];
      else n.setAttribute(k, attrs[k]);
    });
    (kids || []).forEach(function (c) { n.appendChild(c); });
    return n;
  }

  async function api(path, body, token) {
    var h = { 'Content-Type': 'application/json' };
    if (token) h.Authorization = token;
    var res = await fetch(BASE + path, { method: body === undefined ? 'GET' : 'POST', headers: h, body: body === undefined ? undefined : JSON.stringify(body) });
    var j = await res.json();
    if (j.code !== 200) throw new Error(j.message || ('HTTP ' + res.status));
    return j.data;
  }

  async function ensureAdmin(password) {
    var t = adminToken || localStorage.getItem('token');
    if (t) {
      try {
        var me = await api('/api/me', undefined, t);
        if (me && me.role === 2) { adminToken = t; return t; }
      } catch (e) { /* token 失效, 往下用密码登录 */ }
    }
    if (!password) throw new Error('需要管理员密码');
    var r = await api('/api/auth/login', { username: 'admin', password: password });
    adminToken = r.token;
    return adminToken;
  }

  function isOurs(s) {
    if (s.driver !== 'WebDav') return false;
    try { return String(JSON.parse(s.addition).address || '').replace(/\/+$/, '') === DAV; } catch (e) { return false; }
  }

  function friendly(msg) {
    if (/409/.test(msg)) return 'Cookie 无效: 最常见原因是缺少 BDUSS(HttpOnly, document.cookie 复制不到)或已过期, 请按上面的方法重新复制';
    if (/401/.test(msg)) return '没有读到 Cookie(密码栏为空)';
    return msg;
  }

  function openDialog() {
    if (document.getElementById('bpw-bind-mask')) return;
    var inp = 'width:100%;box-sizing:border-box;padding:9px 11px;border:1px solid #e3e5e8;border-radius:8px;font-size:14px;background:#fff;color:#222;margin:6px 0 12px;';
    var lab = 'display:block;font-size:13px;color:#666;';
    var cookie = el('input', { type: 'password', autocomplete: 'off', placeholder: 'BDUSS=xxx; STOKEN=xxx; ...', style: inp });
    var bduss = el('input', { type: 'password', autocomplete: 'off', style: inp });
    var stoken = el('input', { type: 'password', autocomplete: 'off', style: inp });
    var target = el('select', { style: inp });
    var mount = el('input', { type: 'text', value: '/百度网盘', style: inp });
    var mountRow = el('div', {}, [el('label', { style: lab, text: '新账号的挂载路径' }), mount]);
    var adminPw = el('input', { type: 'password', autocomplete: 'off', placeholder: 'OpenList 管理员密码', style: inp });
    var adminRow = el('div', { style: 'display:none' }, [el('label', { style: lab, text: '管理员(admin)密码 — 用来保存设置' }), adminPw]);
    var msg = el('div', { style: 'min-height:20px;font-size:13px;margin:2px 0 10px;white-space:pre-wrap;' });
    var cancel = el('button', { type: 'button', text: '取消', style: 'padding:8px 14px;border:1px solid #e3e5e8;border-radius:8px;background:#fff;cursor:pointer;font-size:14px;' });
    var ok = el('button', { type: 'button', text: '绑定', style: 'padding:8px 16px;border:0;border-radius:8px;background:#0af;color:#fff;cursor:pointer;font-size:14px;' });
    var card = el('div', { style: 'background:#fff;color:#222;width:min(520px,calc(100vw - 32px));max-height:calc(100vh - 32px);overflow:auto;border-radius:12px;padding:24px;box-shadow:0 10px 40px rgba(0,0,0,.25);font-family:system-ui,sans-serif;' }, [
      el('h3', { text: '绑定百度账号', style: 'margin:0 0 14px;font-size:18px;' }),
      el('p', { style: 'margin:0 0 10px;font-size:14px;color:#666;line-height:1.6;', text: '两种方式任选其一。凭证保存在 OpenList 对应存储的"密码"栏(服务器数据库), 不会显示在页面上。' }),
      el('p', { style: 'margin:0 0 14px;font-size:14px;color:#666;line-height:1.6;', text: '获取方法: 电脑浏览器登录 pan.baidu.com → F12 → 应用/Application → Cookie → 手动复制 BDUSS 与 STOKEN 的值填方式二; 或 F12 → 网络/Network → 点任一 pan.baidu.com 请求 → 请求标头里复制整行 Cookie 填方式一。注意: BDUSS 是 HttpOnly, 在控制台用 document.cookie 复制不到, 缺它一定会失败。' }),
      el('label', { style: lab, text: '方式一: 整串 Cookie' }), cookie,
      el('label', { style: lab, text: '方式二: BDUSS' }), bduss,
      el('label', { style: lab, text: 'STOKEN' }), stoken,
      el('label', { style: lab, text: '绑定到哪个百度存储' }), target, mountRow, adminRow, msg,
      el('div', { style: 'display:flex;justify-content:flex-end;gap:8px;' }, [cancel, ok]),
    ]);
    var mask = el('div', { id: 'bpw-bind-mask', style: 'position:fixed;inset:0;z-index:100000;background:rgba(0,0,0,.45);display:flex;align-items:center;justify-content:center;' }, [card]);
    document.body.appendChild(mask);
    function close() { mask.remove(); }
    function say(t, bad) { msg.textContent = t; msg.style.color = bad ? '#d33' : '#1a7f37'; }
    cancel.onclick = close;
    mask.addEventListener('mousedown', function (e) { if (e.target === mask) close(); });

    var stores = [];
    function syncMount() { mountRow.style.display = target.value === '__new__' ? '' : 'none'; }
    target.onchange = syncMount;
    (async function init() {
      try {
        var tok = adminToken || localStorage.getItem('token');
        if (!tok) throw new Error('no token');
        await ensureAdmin('');
        stores = (await api('/api/admin/storage/list', undefined, adminToken)).content.filter(isOurs);
      } catch (e) {
        adminRow.style.display = '';
      }
      fillTargets();
    })();
    function fillTargets() {
      target.textContent = '';
      stores.forEach(function (s) { target.appendChild(el('option', { value: String(s.id), text: '更新 ' + s.mount_path })); });
      target.appendChild(el('option', { value: '__new__', text: '新增一个百度账号(新建挂载点)' }));
      if (!stores.length) target.value = '__new__';
      syncMount();
    }

    ok.onclick = async function () {
      var ck = cookie.value.trim();
      if (!ck && bduss.value.trim()) ck = 'BDUSS=' + bduss.value.trim() + (stoken.value.trim() ? '; STOKEN=' + stoken.value.trim() : '');
      if (!/(^|;\s*)BDUSS=/i.test(ck)) { say('Cookie 里没有 BDUSS。请填方式二(BDUSS+STOKEN), 或从 Network 请求头复制完整 Cookie', true); return; }
      ok.disabled = true; say('保存中…');
      try {
        var tok = await ensureAdmin(adminPw.value);
        adminRow.style.display = 'none';
        if (!stores.length && target.value !== '__new__') target.value = '__new__';
        var all = (await api('/api/admin/storage/list', undefined, tok)).content;
        stores = all.filter(isOurs);
        var mountPath;
        if (target.value === '__new__') {
          mountPath = '/' + (mount.value.trim().replace(/^\/+/, '') || '百度网盘');
          if (all.some(function (s) { return s.mount_path === mountPath; })) throw new Error('挂载路径 ' + mountPath + ' 已存在, 换一个');
          await api('/api/admin/storage/create', {
            mount_path: mountPath, order: 0, remark: 'baidupcs-web WebDAV', driver: 'WebDav', cache_expiration: 30,
            web_proxy: true, webdav_policy: 'native_proxy', enable_sign: false,
            addition: JSON.stringify({ vendor: 'other', address: DAV, username: 'baidu', password: ck, root_folder_path: '/', tls_insecure_skip_verify: false }),
          }, tok);
        } else {
          var s = stores.filter(function (x) { return String(x.id) === target.value; })[0];
          if (!s) throw new Error('所选存储不存在');
          var add = JSON.parse(s.addition);
          add.username = add.username || 'baidu';
          add.password = ck;
          s.addition = JSON.stringify(add);
          mountPath = s.mount_path;
          await api('/api/admin/storage/update', s, tok);
        }
        say('已保存, 正在验证…');
        try {
          var r = await api('/api/fs/list', { path: mountPath, refresh: true, page: 1, per_page: 0 }, tok);
          say('绑定成功: ' + mountPath + ' 读取到 ' + ((r.content || []).length) + ' 项。页面即将刷新…');
          setTimeout(function () { location.reload(); }, 1500);
        } catch (e) {
          say('已保存, 但读取网盘失败: ' + friendly(String(e.message || e)), true);
        }
      } catch (e) {
        say(friendly(String(e.message || e)), true);
      }
      ok.disabled = false;
    };
  }

  function addButton() {
    if (document.getElementById('bpw-bind-btn')) return;
    var b = el('button', { id: 'bpw-bind-btn', type: 'button', text: '绑定百度账号', title: '填写百度网盘 Cookie',
      style: 'position:fixed;left:16px;bottom:16px;z-index:99999;padding:8px 14px;border:0;border-radius:18px;background:#0af;color:#fff;font-size:13px;cursor:pointer;box-shadow:0 2px 10px rgba(0,0,0,.25);opacity:.9;' });
    b.onclick = openDialog;
    document.body.appendChild(b);
  }
  if (document.body) addButton(); else document.addEventListener('DOMContentLoaded', addButton);
  // SPA 若重建 body 内容, 按钮丢了就补回
  new MutationObserver(function () { if (document.body && !document.getElementById('bpw-bind-btn')) addButton(); })
    .observe(document.documentElement, { childList: true, subtree: true });
})();
