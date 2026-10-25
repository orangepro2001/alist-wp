/**
 * pv.js — 极小响应式助手(替代 petite-vue 的本地副本)。
 *
 * 上游模块缓存无 petite-vue 发行文件且无 Node/网络拉取, 按任务书预案自写 ≤200 行替代。
 * 覆盖本项目所需的 petite-vue 特性子集:
 *   - createApp({ data, methods }) → mount(el): 声明式挂载
 *   - 指令: q-if / q-show / q-text / q-html / q-model / q-on:{event}(支持 @缩写)
 *           q-bind:{attr}(支持 :缩写) / q-effect
 *   - 插值: {{ expr }}(textContent 级)
 *   - 手动触发: app.update() 或 ctx.$update()(无依赖追踪的显式刷新模型, 数据量级 <1000 行)
 *
 * 用法:
 *   const app = pv.createApp({ data: () => ({ n: 1 }), methods: { inc(s){ s.n++ } } });
 *   window.app = app.mount('#app');
 */
(function (global) {
  'use strict';

  var DIRECTIVE = /^q-(if|show|text|html|model|effect)$|^q-on:(.+)$|^q-bind:(.+)$/;
  var SHORT_ON = /^@(.+)$/;
  var SHORT_BIND = /^:(.+)$/;

  /** compile 扫描 el 子树, 收集绑定节点。 */
  function collect(root, ctx) {
    var nodes = [];
    var walk = function (el) {
      var attrs = [].slice.call(el.attributes || []);
      for (var i = 0; i < attrs.length; i++) {
        var name = attrs[i].name;
        var value = attrs[i].value;
        var m = DIRECTIVE.exec(name) || (SHORT_ON.exec(name) && ['x', 'on:' + SHORT_ON.exec(name)[1]]) ||
                (SHORT_BIND.exec(name) && ['x', 'bind:' + SHORT_BIND.exec(name)[1]]);
        if (!m) continue;
        var kind = m[1] || (m[2] ? 'on:' + m[2] : 'bind:' + m[3]);
        nodes.push({ el: el, kind: kind, expr: value });
      }
      // 插值文本
      if (el.children.length === 0 && /\{\{/.test(el.textContent)) {
        nodes.push({ el: el, kind: 'interp', expr: el.textContent });
      }
      [].slice.call(el.children).forEach(walk);
    };
    walk(root);
    return nodes;
  }

  /** evalExpr 在 ctx 作用域内求值表达式( new Function 级, 仅渲染内嵌可信模板)。 */
  function evalExpr(ctx, expr) {
    try {
      var names = Object.keys(ctx);
      var fn = new Function(names.join(','), '"use strict";return (' + expr + ');');
      return fn.apply(null, names.map(function (k) { return ctx[k]; }));
    } catch (e) {
      return undefined;
    }
  }

  function createApp(opt) {
    var state = opt.data();
    var methods = opt.methods || {};
    // methods 可见全部 state; state 变化经 $update 手动触发重渲。
    var ctx = Object.create(state);
    Object.keys(methods).forEach(function (k) {
      ctx[k] = methods[k].bind(null, ctx);
    });
    ctx.$update = function () { /* 在 mount 时填充 */ };

    var bindings = [];

    function apply(b) {
      var el = b.el;
      switch (b.kind) {
        case 'if': {
          var show = !!evalExpr(ctx, b.expr);
          if (!show && el.style.display !== 'none') el.style.display = 'none';
          if (show && el.style.display === 'none') el.style.display = '';
          break;
        }
        case 'show':
          el.style.display = evalExpr(ctx, b.expr) ? '' : 'none';
          break;
        case 'text':
          el.textContent = evalExpr(ctx, b.expr) == null ? '' : String(evalExpr(ctx, b.expr));
          break;
        case 'html':
          el.innerHTML = evalExpr(ctx, b.expr) || '';
          break;
        case 'model': {
          var val = evalExpr(ctx, b.expr);
          if (el.type === 'checkbox') el.checked = !!val;
          else if (el.value !== val && document.activeElement !== el) el.value = val == null ? '' : val;
          break;
        }
        case 'effect':
          evalExpr(ctx, b.expr);
          break;
        default:
          if (b.kind.indexOf('on:') === 0) {
            // 事件绑定只在 mount 时挂一次
          } else if (b.kind.indexOf('bind:') === 0) {
            var attr = b.kind.slice(5);
            var v = evalExpr(ctx, b.expr);
            if (v === false || v == null) el.removeAttribute(attr);
            else el.setAttribute(attr, v === true ? '' : String(v));
          } else if (b.kind === 'interp') {
            el.textContent = String(b.expr).replace(/\{\{(.+?)\}\}/g, function (_, ex) {
              var v = evalExpr(ctx, ex.trim());
              return v == null ? '' : String(v);
            });
          }
      }
    }

    return {
      state: state,
      mount: function (selector) {
        var root = document.querySelector(selector);
        if (!root) throw new Error('pv: mount target ' + selector + ' not found');
        bindings = collect(root, ctx);
        bindings.forEach(function (b) {
          if (b.kind.indexOf('on:') === 0) {
            var evt = b.kind.slice(3);
            var expr = b.expr;
            b.el.addEventListener(evt, function (event) {
              ctx.$event = event;
              // 支持 "handler" / "handler(arg)" / 内联赋值 "s.n = 1"
              var m = /^([\w$]+)\s*\((.*)\)$/.exec(expr.trim());
              if (m && typeof ctx[m[1]] === 'function') {
                var args = m[2] ? m[2].split(',').map(function (a) { return evalExpr(ctx, a.trim()); }) : [];
                ctx[m[1]].apply(null, args);
              } else if (typeof ctx[expr.trim()] === 'function') {
                ctx[expr.trim()]();
              } else {
                evalExpr(ctx, expr); // 内联表达式(赋值等)
              }
              delete ctx.$event;
              render();
            });
          } else if (b.kind === 'model') {
            var expr2 = b.expr;
            b.el.addEventListener('input', function () {
              var v = b.el.type === 'checkbox' ? b.el.checked : b.el.value;
              setPath(ctx, expr2, v);
              render();
            });
            b.el.addEventListener('change', function () {
              var v2 = b.el.type === 'checkbox' ? b.el.checked : b.el.value;
              setPath(ctx, expr2, v2);
              render();
            });
          }
          apply(b);
        });
        var api = this;
        ctx.$update = function () { render(); };
        function render() { bindings.forEach(apply); }
        api.update = render;
        return api;
      },
      update: function () {},
    };
  }

  /** setPath 支持一层属性与嵌套 "a.b.c" 写入(q-model 用)。 */
  function setPath(obj, expr, value) {
    var keys = expr.split('.');
    var cur = obj;
    for (var i = 0; i < keys.length - 1; i++) {
      cur = cur[keys[i].trim()];
      if (cur == null) return;
    }
    var last = keys[keys.length - 1].trim();
    // 常见转换: 数字字符串自动转 number
    if (typeof value === 'string' && value !== '' && !isNaN(value)) value = +value;
    cur[last] = value;
  }

  global.pv = { createApp: createApp };
})(window);
