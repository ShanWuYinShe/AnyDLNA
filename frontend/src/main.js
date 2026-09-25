// AnyDLNA 前端逻辑：通过 window.go.main.App 调用 Go 绑定方法。

const $ = (id) => document.getElementById(id);

const state = {
    selectedUDN: null,
    pending: null,       // 待投屏来源：{type:'file'|'url', path/url, name, durationSec, ...}
    polling: null,       // 轮询定时器
    scrubbing: false,    // 用户正在拖动进度条
    stoppedCount: 0,     // 连续 STOPPED 次数，用于判定投屏结束
    config: null,        // 当前设置（代理与 Cookies）
};

function toast(msg, ms = 3600) {
    const el = $('toast');
    el.textContent = msg;
    el.classList.remove('hidden');
    clearTimeout(toast._t);
    toast._t = setTimeout(() => el.classList.add('hidden'), ms);
}

function fmtClock(sec) {
    sec = Math.max(0, Math.floor(sec || 0));
    const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
    return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

function call(method, ...args) {
    return window.go.main.App[method](...args).catch((err) => {
        const msg = typeof err === 'string' ? err : (err && err.message) || JSON.stringify(err);
        toast(msg);
        throw err;
    });
}

// errText 把 Go 返回的错误统一转成可展示文本（不弹 toast，供内联展示）。
function errText(err) {
    return typeof err === 'string' ? err : (err && err.message) || '操作失败';
}

// ---------- 设备 ----------

async function searchDevices() {
    $('deviceHint').textContent = '正在搜索局域网设备…';
    try {
        const devices = await call('SearchDevices', 6000);
        // 与已有列表合并去重，不清空旧设备：本次没应答的设备（广播间隔、
        // 丢包、临时离线）不代表已失效，直接清空会让用户丢失已在列表中的设备。
        // addDeviceItem 按 UDN 去重，已存在的设备不会被重复添加。
        devices.forEach(addDeviceItem);
        updateDeviceHint();
        tryAutoSelect();
    } catch {
        $('deviceHint').textContent = '搜索失败，请重试';
    }
}

// updateDeviceHint 按列表实际内容更新提示。
// 用 DOM 里的数量而非本次搜索结果，才能反映合并后的完整列表。
function updateDeviceHint() {
    const total = $('deviceList').querySelectorAll('.device').length;
    $('deviceHint').textContent = total
        ? `列表中共 ${total} 台设备，点击选择`
        : '未发现设备：应用会持续监听并定时搜索，设备上线后会自动出现在列表；也可点「搜索设备」或输入电视 IP 手动添加';
}

// addDeviceItem 把一台设备渲染进列表并绑定选中事件；同一设备（UDN）不重复添加。
function addDeviceItem(d) {
    const list = $('deviceList');
    if (list.querySelector(`li[data-udn="${CSS.escape(d.udn)}"]`)) {
        return;
    }
    const li = document.createElement('li');
    li.className = 'device' + (d.offline ? ' offline' : '');
    li.dataset.udn = d.udn;
    li.dataset.model = d.model || d.host;
    li.innerHTML = `<div class="d-name"></div><div class="d-model"></div>`;
    li.querySelector('.d-name').textContent = d.name;
    li.querySelector('.d-model').textContent = (d.offline ? '离线 · ' : '') + li.dataset.model;
    li.onclick = () => {
        if (li.classList.contains('offline')) {
            toast('该设备连续多轮未应答，可能已离线；仍可尝试投屏');
        }
        list.querySelectorAll('.device').forEach((x) => x.classList.remove('selected'));
        li.classList.add('selected');
        state.selectedUDN = d.udn;
        try { localStorage.setItem('anydlna.lastUDN', d.udn); } catch { /* 存储不可用时静默 */ }
        showDeviceCapabilities(d);
    };
    list.appendChild(li);
}

// tryAutoSelect 自动选中上一次使用的设备（localStorage 持久化）：
// 打开应用时大多是想接着往同一台电视投，省掉每次手动点选。
// 返回是否完成了自动选中。
function tryAutoSelect() {
    if (state.selectedUDN) return false;
    let last;
    try { last = localStorage.getItem('anydlna.lastUDN'); } catch { return false; }
    if (!last) return false;
    const li = $('deviceList').querySelector(`li[data-udn="${CSS.escape(last)}"]`);
    if (!li) return false;
    li.click();
    $('deviceHint').textContent = '已自动选中上次的设备，点击其他设备可更换';
    return true;
}

// updateDeviceOffline 按后端推送更新设备条目的离线状态（置灰，不删除条目）。
function updateDeviceOffline(d) {
    const li = $('deviceList').querySelector(`li[data-udn="${CSS.escape(d.udn)}"]`);
    if (!li) return;
    li.classList.toggle('offline', !!d.offline);
    const model = li.querySelector('.d-model');
    if (model) {
        model.textContent = (d.offline ? '离线 · ' : '') + (li.dataset.model || '');
    }
}

// showDeviceCapabilities 展示选中设备声明的接收能力。
// 这直接决定投屏时能否免转码，因此让用户能看到依据。
async function showDeviceCapabilities(d) {
    const el = $('deviceCaps');
    el.classList.remove('hidden');
    el.textContent = `正在查询「${d.name}」支持的格式…`;
    let caps;
    try {
        caps = await window.go.main.App.DeviceCapabilityInfo(d.udn);
    } catch {
        el.textContent = '查询设备格式失败，将按通用策略处理。';
        return;
    }
    if (!caps || !caps.queried) {
        el.textContent = '该设备未上报支持的格式，将按通用策略处理（H.264 免转码）。';
        return;
    }
    const ok = [];
    if (caps.supportsMp4) ok.push('MP4');
    if (caps.supportsMkv) ok.push('MKV');
    if (caps.supportsTs) ok.push('MPEG-TS');
    const total = (caps.videoMIMEs || []).length;
    el.innerHTML = '';
    const lead = document.createElement('div');
    lead.textContent = `设备声明支持 ${total} 种视频格式`;
    el.appendChild(lead);
    const detail = document.createElement('div');
    if (ok.length) {
        detail.innerHTML = '可用于免转码直出：<b></b>';
        detail.querySelector('b').textContent = ok.join(' / ');
    } else {
        detail.textContent = '未声明可直出的常见容器，将以换封装或转码方式投屏。';
    }
    el.appendChild(detail);
}

async function addDeviceManually() {
    const host = $('deviceIp').value.trim();
    if (!host) { toast('请输入电视的 IP 地址'); return; }
    $('btnAddDevice').disabled = true;
    try {
        const d = await call('AddDeviceManually', host);
        $('deviceHint').textContent = '已手动添加设备，点击选择';
        addDeviceItem(d);
        $('deviceIp').value = '';
    } catch { /* toast 已提示 */ }
    finally { $('btnAddDevice').disabled = false; }
}

// 常驻监听：电视上线广播时由后端推送，设备自动出现在列表里。
if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('device:discovered', (d) => {
        addDeviceItem(d);
        // 上次的设备可能比应用晚开机：发现它时补一次自动选中。
        if (tryAutoSelect()) return;
        $('deviceHint').textContent = '已自动发现新设备，点击选择';
        toast('发现新设备：' + d.name);
    });
    // 设备连续多轮未应答搜索（置灰）或重新应答（恢复）时更新列表项。
    window.runtime.EventsOn('device:offline', updateDeviceOffline);
    // 应用重启后恢复了上次投屏的控制（电视可能还在播/暂停在原内容上）。
    window.runtime.EventsOn('cast:restored', (st) => {
        if (st && st.active) {
            startControls(st);
            toast('已恢复上次投屏的控制：' + st.device + ' · ' + st.file);
        }
    });
}

// ---------- 设置视图切换 ----------

function showSettings() {
    $('mainView').classList.add('hidden');
    $('settingsView').classList.remove('hidden');
    $('btnSettings').classList.add('hidden');
    $('btnBack').classList.remove('hidden');
    loadSettings();
}

function showMain() {
    $('settingsView').classList.add('hidden');
    $('mainView').classList.remove('hidden');
    $('btnBack').classList.add('hidden');
    $('btnSettings').classList.remove('hidden');
}

// ---------- 设置：加载与保存 ----------

async function loadSettings() {
    let cfg;
    try {
        cfg = await window.go.main.App.GetConfig();
    } catch {
        toast('读取设置失败');
        return;
    }
    state.config = cfg;

    const proxyMode = cfg.proxyMode || 'system';
    document.querySelector(`input[name="proxyMode"][value="${proxyMode}"]`).checked = true;
    $('proxyInput').value = cfg.proxyUrl || '';

    const cookieMode = cfg.cookieMode || 'none';
    document.querySelector(`input[name="cookieMode"][value="${cookieMode}"]`).checked = true;
    if (cfg.cookieBrowser) {
        $('cookieBrowser').value = cfg.cookieBrowser;
    }

    $('castTitleInput').value = cfg.castTitle || '';

    syncProxyRows();
    syncCookieRows();
    // 同时刷新系统代理提示与 Cookies 状态。
    refreshSystemProxy();
    refreshCookieStatus();
}

// syncProxyRows 按当前代理模式显示/隐藏手动地址输入。
function syncProxyRows() {
    const mode = document.querySelector('input[name="proxyMode"]:checked').value;
    $('proxyUrlRow').classList.toggle('hidden', mode !== 'manual');
}

// syncCookieRows 按当前 Cookies 模式显示对应的附加选项。
function syncCookieRows() {
    const mode = document.querySelector('input[name="cookieMode"]:checked').value;
    $('browserRow').classList.toggle('hidden', mode !== 'browser');
    $('loginBrowserBox').classList.toggle('hidden', mode !== 'loginbrowser');
    if (mode === 'loginbrowser') {
        refreshLoginBrowser();
    }
}

// refreshSystemProxy 展示当前检测到的系统代理，便于用户判断「跟随系统」是否有效。
async function refreshSystemProxy() {
    try {
        const proxy = await window.go.main.App.SystemProxy();
        $('sysProxyHint').textContent = proxy
            ? `当前检测到：${proxy}`
            : '未检测到系统代理或代理环境变量，将直连访问';
    } catch {
        $('sysProxyHint').textContent = '检测系统代理失败';
    }
}

// collectConfig 从界面收集设置。
function collectConfig() {
    return {
        proxyMode: document.querySelector('input[name="proxyMode"]:checked').value,
        proxyUrl: $('proxyInput').value.trim(),
        cookieMode: document.querySelector('input[name="cookieMode"]:checked').value,
        cookieBrowser: $('cookieBrowser').value,
        castTitle: $('castTitleInput').value,
    };
}

async function saveSettings() {
    const cfg = collectConfig();
    if (cfg.proxyMode === 'manual' && !cfg.proxyUrl) {
        toast('手动代理模式下请填写代理地址');
        return;
    }
    try {
        await call('SetConfig', cfg);
        state.config = cfg;
        toast('设置已保存');
    } catch { /* toast 已提示 */ }
}

// showResult 在指定位置展示一行结果（成功/失败着色）。
function showResult(el, text, ok) {
    el.textContent = text;
    el.classList.remove('hidden', 'ok', 'err');
    el.classList.add(ok ? 'ok' : 'err');
}

async function testProxy() {
    const cfg = collectConfig();
    $('btnTestProxy').disabled = true;
    $('btnTestProxy').textContent = '测试中…';
    try {
        const msg = await window.go.main.App.TestProxy(cfg);
        showResult($('proxyResult'), msg, true);
    } catch (err) {
        showResult($('proxyResult'), errText(err), false);
    } finally {
        $('btnTestProxy').disabled = false;
        $('btnTestProxy').textContent = '测试代理连通性';
    }
}

// ---------- 设置：Cookies ----------

async function refreshCookieStatus() {
    let info;
    try {
        info = await window.go.main.App.GetCookieStatus();
    } catch {
        return;
    }
    const el = $('cookieStatus');
    if (info && info.exists) {
        el.textContent = `已保存 ${info.count} 条 Cookies（${info.savedAt}）`;
        el.classList.add('ok');
    } else {
        el.textContent = '尚未保存 Cookies';
        el.classList.remove('ok');
    }
}

// refreshLoginBrowser 展示本机检测到的浏览器与登录窗口运行状态；
// 没有可用浏览器时提示改用「读取本机浏览器」。
async function refreshLoginBrowser() {
    let info;
    try {
        info = await window.go.main.App.LoginBrowserStatus();
    } catch {
        return;
    }
    const el = $('browserHint');
    if (!info.supported) {
        el.textContent = '未检测到 Chrome / Edge / Brave 等浏览器：请改用「读取本机浏览器」，或先安装其中之一。';
        $('btnOpenLogin').disabled = true;
        $('btnSaveCookies').disabled = true;
        return;
    }
    $('btnOpenLogin').disabled = false;
    $('btnSaveCookies').disabled = false;
    const names = (info.browsers || []).map((b) => b.name).join(' / ');
    el.textContent = info.running
        ? `登录窗口正在运行（${info.executable || names}）`
        : `将使用：${names}`;
}

// openLoginBrowser 打开应用专用浏览器窗口，供用户登录站点。
async function openLoginBrowser() {
    const site = $('loginSite').value.trim() || 'https://www.youtube.com';
    $('btnOpenLogin').disabled = true;
    try {
        await call('OpenLoginBrowser', site);
        showResult($('loginResult'),
            '登录窗口已打开：请在该窗口中完成登录，然后点击「我已登录，保存 Cookies」。', true);
        await refreshLoginBrowser();
    } catch (err) {
        showResult($('loginResult'), errText(err), false);
    } finally {
        $('btnOpenLogin').disabled = false;
    }
}

// closeLoginBrowser 关闭登录窗口，避免浏览器进程长期驻留（登录状态保留）。
async function closeLoginBrowser() {
    try {
        await call('CloseLoginBrowser');
        showResult($('loginResult'), '已关闭登录窗口（登录状态已保留）。', true);
        await refreshLoginBrowser();
    } catch { /* toast 已提示 */ }
}

// resetLoginBrowser 清除登录窗口的独立 profile，等同于退出所有站点登录。
async function resetLoginBrowser() {
    try {
        await call('ResetLoginBrowser');
        showResult($('loginResult'), '已重置登录状态，下次需重新登录。', true);
        await refreshCookieStatus();
        await refreshLoginBrowser();
    } catch { /* toast 已提示 */ }
}

// saveBrowserCookies 把登录浏览器中的 Cookies 读回并保存。
async function saveBrowserCookies() {
    const cfg = collectConfig();
    // 保存后必须让设置处于 loginbrowser 模式，否则 Cookies 不会被使用。
    if (cfg.cookieMode !== 'loginbrowser') {
        document.querySelector('input[name="cookieMode"][value="loginbrowser"]').checked = true;
        syncCookieRows();
        cfg.cookieMode = 'loginbrowser';
    }
    $('btnSaveCookies').disabled = true;
    $('btnSaveCookies').textContent = '保存中…';
    try {
        const info = await window.go.main.App.SaveBrowserCookies();
        await window.go.main.App.SetConfig(cfg);
        state.config = cfg;
        showResult($('loginResult'), `已保存 ${info.count} 条 Cookies`, true);
        await refreshCookieStatus();
    } catch (err) {
        showResult($('loginResult'), errText(err), false);
    } finally {
        $('btnSaveCookies').disabled = false;
        $('btnSaveCookies').textContent = '我已登录，保存 Cookies';
    }
}

async function clearCookies() {
    try {
        await call('ClearCookies');
        showResult($('loginResult'), '已清除应用保存的 Cookies', true);
        await refreshCookieStatus();
    } catch { /* toast 已提示 */ }
}

// ---------- 视频来源（本地文件 / 在线链接）----------

async function pickVideo() {
    // 传入当前选中设备：后端据此查询该设备声明的格式，给出真实方案。
    const v = await call('PickVideo', state.selectedUDN || '');
    if (!v) return; // 用户取消
    state.pending = { type: 'file', ...v };
    renderPending();
}

async function resolveURL() {
    const url = $('urlInput').value.trim();
    if (!url) { toast('请先粘贴视频链接'); return; }
    $('btnResolve').disabled = true;
    $('btnResolve').textContent = '解析中…';
    try {
        const r = await call('ResolveURL', state.selectedUDN || '', url);
        state.pending = { type: 'url', ...r };
        renderPending();
    } catch { /* toast 已提示 */ }
    finally {
        $('btnResolve').disabled = false;
        $('btnResolve').textContent = '解析';
    }
}

// modeText 把输出方式转成面向用户的说明与样式。
// direct（原文件直出）与 remux（换封装）都不重编码视频，属于「无损」档。
function modeText(p) {
    switch (p.mode) {
        case 'direct':
            return { text: '免转码：设备已声明支持该格式，原文件直出（可拖动进度）', cls: 'ok' };
        case 'remux':
            // 非 faststart 的 MP4 需要先换封装才能立即起播，单独说明原因。
            if (p.type === 'file' && p.fastStart === false) {
                return {
                    text: '免转码：该 MP4 索引在文件末尾，换封装后即可立即起播（画质无损）',
                    cls: 'ok',
                };
            }
            return { text: '免转码：仅换封装，画质无损、几乎不占 CPU', cls: 'ok' };
        default:
            return { text: '设备无法解码该编码，需实时转码（较耗 CPU）', cls: 'tc' };
    }
}

function renderPending() {
    const p = state.pending;
    if (!p) return;
    $('videoCard').classList.remove('hidden');
    $('videoHint').classList.add('hidden');
    $('vName').textContent = p.name || p.title || '未命名';
    const m = modeText(p);
    if (p.type === 'file') {
        const res = p.width ? `${p.width}×${p.height} · ` : '';
        $('vMeta').textContent = `本地文件 · ${res}${p.videoCodec || '?'} + ${p.audioCodec || '无声'} · ${fmtClock(p.durationSec)} · ${p.sizeMB.toFixed(0)} MB`;
    } else {
        const from = p.extractor ? `来源 ${p.extractor}` : '在线视频';
        const dur = p.isLive ? '直播' : fmtClock(p.durationSec);
        const codecs = [p.videoCodec, p.audioCodec].filter(Boolean).join(' + ');
        $('vMeta').textContent = `${from}${p.uploader ? ' · ' + p.uploader : ''} · ${dur}${codecs ? ' · ' + codecs : ''}`;
    }
    $('vBadge').textContent = m.text;
    $('vBadge').className = 'video-badge ' + m.cls;
}

// ---------- 投屏 ----------

async function cast() {
    if (!state.selectedUDN) { toast('请先选择一台播放设备'); return; }
    const p = state.pending;
    if (!p) { toast('请先选择本地视频或解析在线链接'); return; }

    // 在线视频解析可能耗时较久，给出明确反馈，避免用户以为界面无响应。
    const btn = $('btnCast');
    const label = btn.textContent;
    btn.disabled = true;
    btn.textContent = p.type === 'url' ? '正在解析并投屏…' : '正在投屏…';
    try {
        const st = p.type === 'file'
            ? await call('Cast', state.selectedUDN, p.path)
            : await call('CastURL', state.selectedUDN, p.url);
        startControls(st);
    } catch { /* toast 已提示 */ }
    finally {
        btn.disabled = false;
        btn.textContent = label;
    }
}

function startControls(st) {
    $('controls').classList.remove('hidden');
    $('castDot').classList.add('on');
    $('castTitle').textContent = `${st.device} · ${st.file}`;
    $('btnPlayPause').textContent = '暂停';
    if (state.polling) clearInterval(state.polling);
    state.stoppedCount = 0;
    state.polling = setInterval(poll, 1000);
    poll();
    window.go.main.App.GetVolume()
        .then((v) => { $('vol').value = v; })
        .catch(() => { /* 设备可能不支持音量控制 */ });
}

async function poll() {
    let p;
    try {
        p = await window.go.main.App.Poll();
    } catch {
        return;
    }
    if (!p || !p.state) { // 已停止投屏
        stopControls();
        return;
    }
    if (p.state === 'PLAYING' || p.state === 'PAUSED_PLAYBACK') {
        state.stoppedCount = 0;
        $('btnPlayPause').textContent = p.state === 'PLAYING' ? '暂停' : '播放';
        if (!state.scrubbing) {
            $('seek').max = Math.max(1, Math.round(p.durationSec));
            $('seek').value = Math.round(p.positionSec);
            $('curTime').textContent = fmtClock(p.positionSec);
            $('durTime').textContent = fmtClock(p.durationSec);
        }
    } else if (p.state === 'STOPPED' && ++state.stoppedCount >= 3) {
        // 设备明确停止（连续 3 次）：调后端 StopCast 回收流会话与转码进程，
        // 只清界面的话会话要等下一次投屏才释放（UNKNOWN 视为暂时无响应，不计数）。
        stopCast();
    }
}

function stopControls() {
    if (state.polling) { clearInterval(state.polling); state.polling = null; }
    $('controls').classList.add('hidden');
    $('castDot').classList.remove('on');
}

async function stopCast() {
    await call('StopCast').catch(() => {});
    stopControls();
}

// ---------- 事件绑定 ----------

$('btnSearch').onclick = searchDevices;
$('btnAddDevice').onclick = addDeviceManually;
$('deviceIp').onkeydown = (e) => { if (e.key === 'Enter') addDeviceManually(); };
$('btnPick').onclick = pickVideo;
$('btnResolve').onclick = resolveURL;
$('urlInput').onkeydown = (e) => { if (e.key === 'Enter') resolveURL(); };
$('btnCast').onclick = cast;
$('btnStop').onclick = stopCast;
$('btnPlayPause').onclick = () => call('PlayPause').catch(() => {});

// 设置视图。
$('btnSettings').onclick = showSettings;
$('btnBack').onclick = showMain;
$('btnSaveSettings').onclick = saveSettings;
$('btnTestProxy').onclick = testProxy;
$('btnOpenLogin').onclick = openLoginBrowser;
$('btnSaveCookies').onclick = saveBrowserCookies;
$('btnCloseLogin').onclick = closeLoginBrowser;
$('btnResetLogin').onclick = resetLoginBrowser;
$('btnClearCookies').onclick = clearCookies;
document.querySelectorAll('input[name="proxyMode"]').forEach((el) => {
    el.onchange = syncProxyRows;
});
document.querySelectorAll('input[name="cookieMode"]').forEach((el) => {
    el.onchange = syncCookieRows;
});

const seek = $('seek');
seek.oninput = () => { state.scrubbing = true; };
seek.onchange = () => {
    call('SeekTo', Number(seek.value))
        .catch(() => {})
        .finally(() => { state.scrubbing = false; });
};

let volTimer = null;
$('vol').onchange = () => call('SetVolume', Number($('vol').value)).catch(() => {});
$('vol').oninput = () => {
    clearTimeout(volTimer);
    volTimer = setTimeout(() => call('SetVolume', Number($('vol').value)).catch(() => {}), 300);
};

// ---------- 启动 ----------

// whenReady 等待 Wails 注入的绑定可用后再执行。
// 运行时脚本可能晚于本脚本执行，直接调用会因 window.go 尚未定义而失败。
function whenReady(fn, tries = 50) {
    if (window.go && window.go.main && window.go.main.App) {
        fn();
        return;
    }
    if (tries > 0) {
        setTimeout(() => whenReady(fn, tries - 1), 100);
    }
}

// 打开即搜索一次。被动监听只能等到设备主动广播 SSDP alive，
// 而不少电视（含实测的目标设备）平时不广播、只应答搜索，
// 若只依赖监听，打开应用后列表会长时间为空，看起来像「搜不到设备」。
// 同时查询一次投屏状态：应用重启后若恢复了上次投屏的控制，直接显示控制条。
whenReady(() => {
    searchDevices();
    window.go.main.App.GetCastStatus()
        .then((st) => { if (st && st.active) startControls(st); })
        .catch(() => { /* 后端不可达时轮询路径会再试 */ });
});
