# 开发日志:baidupcs-web

## 2026-10-24

### 进度
- 阶段 1 需求 approved(40 FR + FR-40 分享直连下载)
- 阶段 2 设计 approved(docs/design/baidupcs-web.md,9 Wave)
- **W1 完成**验收:build/vet/test exit=0,TestNewServer PASS
- **W2 完成**验收:20 用例全 PASS(双跑无 flaky),qjfoidnh import 边界干净

### W2 排障记录(重要,后续 Wave 引用)
通道:kimi-coder 基础设施故障(Model not found),全部改走 general 通道,验收标准不变。

**Flaky 根因(双重)**:
1. **方法提升覆写失效**:`stubEngine{pcsEngine}` 覆写 `active()` 无效——`e.Quota` 定义在
   内层 `pcsEngine`,内部 `e.active()` 接收者是 `*pcsEngine`,外层覆写不生效 →
   现造 pcs 打向真实 pcs.baidu.com → 上游 client Timeout=50s → 报"网络错误"。
   **修复:桩 pcs 直接注入 pool(map[uid]*pcs),让真实 active() 命中**(pcs_test.go newStubEngine)。
2. **错误响应字段名**:`PCSErrInfo.ErrCode` 的 json tag 是 `error_code`,百度实际惯例是 `errno`。
   桩只回 `errno` 时上游把错误当成功(remotecode=0)。**修复:桩响应双字段 errno+error_code 都带**。
   (真实百度服务端响应能被上游正确解析——上游 CLI 生产可用即为证明,故此修复仅涉及测试桩。)
3. TestListNetError 指向 127.0.0.1:1 挂满 50s:测试内收紧 `pcs.GetClient().Timeout=1s` +
   `SetResponseHeaderTimeout(500ms)` 解决。

### 进度(续)
- **W3 完成**:session 12 用例 PASS;真机冒烟 401/200/Setup/Cookie 属性全对
- **W4 完成**:api 7 用例;真机冒烟 401/html/Content-Type/AUTH_REQUIRED 语义正确;前端 SPA 壳+自写 pv.js(~190 行)替代 petite-vue
- **W5 完成**:xfer 21 用例;冒烟 RANGE_OK/DISPOSITION_UTF8_OK/NO_DISK_GROWTH/SMOKE_DL_ALL_OK
- 通道事故记录:W4 与 W5 首跑各遇一次**空返回截断**(无【END】且工作区零落盘),用 task_id 恢复会话续跑成功;W4 重试后正常,W5 重试后正常。subagent 声称完成必须核对落盘+复跑验收

### 下一步
(已全部完成,见下)

## 2026-10-25 交付记录

### W6~W9 完成(全部验收命令由编排者复跑,exit=0)
- W6 上传:26 用例;RAPID_OR_UPLOAD_OK/TMP_DELETED_10S/ORPHAN_PURGED
- W7 分享/离线/回收站:TestTransferParse 11 用例;转存时序 1:1 上游
- W8 FR-40:sharetmp 13 用例;PREVIEW_OK/TMP_DIR_GONE_10S/QUOTA_PRECHECK_OK/BAD_PASSCODE_OK/TRANSFER_FORBIDDEN_OK
- W9 交付:双平台产物(amd64 13.3MB/arm64 12.4MB),--version 注入,smoke_all 10 标记(RSS 空载 ~16MB),TODO(W) 残留=0

### 阶段 4 评审(评审门触发:改动≥3 文件+并发)
- 初审:11 BLOCKER + 7 NOTE,结论不通过(评审员分 2 段送达+确认无 PART3)
- 编排者逐条验证:11 条全部属实(-race 实证 xfer DATA RACE;active() 空凭证;/api/status 白名单违 401 标准等)
- 修复:11 条全修;复审 1:9 解除,B3(转存失败不回删)/B5(前端拿不到字节路径)未解除→再修
- 复审 2(第 2/2 次,配额用尽):**BLOCKER=0,结论:通过**
- 7 条 NOTE 记录在案不强制修(取消链路触达引擎/nil Engine panic/下载 token 指纹空/toInt64 截断/Cookie UID 高位/config Sync 错路径关闭/响应提交后写 JSON)

### 最终状态
9 包 build/vet/test 全绿(含 -race);5 个冒烟脚本全过;双平台产物在 bin/。可交付。
后续使用:bin/baidupcs-web-linux-amd64 -listen :8080 -password <初始口令>,浏览器开 http://<host>:8080。

## 2026-10-25 用户实战 Bug 修复(2 例)

### 例1: "-password 登录不上"(hash 与输入不匹配)
- 现象: config.json 里的口令哈希与 -password 输入不匹配(bcrypt 实测)
- 根因: -password 仅在口令未初始化时生效(安全语义), 已初始化后被静默忽略; 用户此前某次启动写过不同口令
- 修复: main.go 启动时若口令已初始化且传了 -password → 打印明确提示; TrimSpace 防御 shell 引号/换行

### 例2: "登录成功后页面闪烁回登录页"(重要, 语义级)
- 现象: 服务端日志 POST /api/login 200 → GET /api/quota 401 + /api/files 401 → 前端踢回登录页 → 死循环; 同秒 GET /api/accounts 200
- 根因: **两个"未登录"共用 401**。百度账号未绑定(引擎 ErrAuth"尚未登录任何百度账号")被 errors.go statusOf 映射成 401, 前端 401 一律跳登录页。Web 会话本身完全正常(accounts 200 为证)
- 修复: ①errors.go: 引擎 ErrAuth → 409 "BAIDU_NOT_LINKED"(Web 会话 401 语义保留在 middleware); ②app.js: 401 才跳登录页, 409 BAIDU_NOT_LINKED 只抛业务文案留在当前页(顶栏本就有"未绑定百度账号"提示)
- 验证: 无百度账号时 quota/files=409(带中文提示), 无会话访问仍 401; 全量测试 9 包 ok; smoke_all 全绿; 产物已重编译
- 教训: 错误码要区分"谁的会话"; 测试桩全绿掩盖了真实使用路径(无百度账号首启场景无 e2e 覆盖)

### 例3: 前端缺"绑定百度账号"入口(用户问"账号在哪登录")
- 现象: 后端 POST /api/accounts/add(Cookie 或 BDUSS+STOKEN)自 W4 起就存在, 但前端从未做入口; 顶栏只有账号切换下拉(空列表时无意义)
- 修复: 顶栏新增"+ 账号"按钮 → 绑定弹窗(整串 Cookie 或 BDUSS+STOKEN 二选一, 含获取指引; 密码型输入防偷窥); 绑定成功自动刷新顶栏与文件页; style.css 增弹窗样式
- 验证: 页面含 btn-add-account; 假凭证 add→列表出现→DELETE 清除(通路的增删全链路 curl 实测); 全量测试 9 包 ok; 产物重编译

### 2026-10-25 追加: 用户反馈三项
1. **后台进程**: 编排者曾代起服务被用户指出, 已全部 kill 并承诺不再代启(服务生命周期归用户)
2. **扫码登录调研结论**: 上游 Baidu-Login v1.4.1 与 BaiduPCS-Go 全源码无二维码登录实现(全文零命中), 属需逆向百度 Passport 二维码协议的范围外项, 不做; **账密登录可行**(上游 BaiduClient.BaiduLogin 支持用户名/手机号/邮箱+密码, 含验证码/短信二次验证分支), 待用户确认后排期
3. **改口令入口**: 前端设置页本就有"修改访问口令"表单(W3 的 POST /api/password), 但位置太靠下; 已移至设置页顶部第一个区块; 全链路实测: 改口令 200 → 新口令登录 200 → 旧口令 401

## 2026-10-25 例4: "文件加载不出来/网络错误" = IPv6 死等(重要环境修复)
- 用户真实凭证无问题; 用户机器有公网 IPv6 地址但到百度 v6 实际不通(ping6/tcp6 死等), DNS 把 v6 排前
- 上游 requester/dial.go resolveTCPHost 只取 addrs[0](=v6) → 每次百度调用死等到超时 → 前端"网络错误"
- 排障弯路(记录): 覆盖 pcs.client.Transport 的方案失败——transport 指针实证(0x...6000 设置 vs 0x...61c0 实际拨号)
  存在第二个 transport, 与上游 lazyInit 的交互纠缠不清; 最终放弃 transport 覆盖, 改用上游官方钩子
  **requester.SetTCPHostBind(host, ipv4) 预填 tcpCache**, dialContext 的 CacheOperationWithError 直接命中 v4, 彻底绕开
- 修复: xengine.applyV4FirstDial——启动首账号构造时一次性探测 v6 可达性(3s 预算/BPW_FORCE_V4=1 强制),
  不可达则对 pan.baidu.com/pcs.baidu.com/d.pcs.baidu.com/baidu.com 预绑 v4; 日志明示
- 实测: 真实凭证 quota 3.5s 返回(18TB/12TB), files 列表返回真实目录; 全量测试 9 包 ok; 产物已重编译
- 教训: 涉上游内部状态时优先找上游官方导出的钩子, 不要和私有字段(lazyInit)玩覆盖捉迷藏

### W5 工程事实(后续 Wave 引用)
- 直链解析器=`ProxyConfig.Resolve` 函数字段注入(生产 resolveViaEngine;冒烟 dlink.map 兜底),不覆写方法
- 下载任务不入 worker,走 max_download_load 信号量;断流续取=Range: bytes=N- 重试 3 次
- SSE=publish 覆盖快照+订阅协程 500ms 节流;Last-Event-ID 重放快照
- W5 顺手补了 W3 遗留:Options.Password 首次初始化接线(config_impl.go)
