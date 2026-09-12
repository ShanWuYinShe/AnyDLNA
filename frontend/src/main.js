// AnyDLNA 前端逻辑：通过 window.go.main.App 调用 Go 绑定方法。

const $ = (id) => document.getElementById(id);

const state = {
    selectedUDN: null,
    pending: null,       // 待投屏来源：{type:'file'|'url', path/url, name, durationSec, ...}
    polling: null,       // 轮询定时器
    scrubbing: false,    // 用户正在拖动进度条
    stoppedCount: 0,     // 连续 STOPPED 次数，用于判定投屏结束
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

// ---------- 设备 ----------

async function searchDevices() {
    $('deviceHint').textContent = '正在搜索局域网设备…';
    $('deviceList').innerHTML = '';
    state.selectedUDN = null;
    try {
        const devices = await call('SearchDevices', 6000);
        if (!devices.length) {
            $('deviceHint').textContent = '未发现设备：应用会持续监听电视广播，设备上线后会自动出现在列表；也可在下方输入电视 IP 手动添加';
            return;
        }
        $('deviceHint').textContent = `发现 ${devices.length} 台设备，点击选择`;
        devices.forEach(addDeviceItem);
    } catch {
        $('deviceHint').textContent = '搜索失败，请重试';
    }
}

// addDeviceItem 把一台设备渲染进列表并绑定选中事件；同一设备（UDN）不重复添加。
function addDeviceItem(d) {
    const list = $('deviceList');
    if (list.querySelector(`li[data-udn="${CSS.escape(d.udn)}"]`)) {
        return;
    }
    const li = document.createElement('li');
    li.className = 'device';
    li.dataset.udn = d.udn;
    li.innerHTML = `<div class="d-name"></div><div class="d-model"></div>`;
    li.querySelector('.d-name').textContent = d.name;
    li.querySelector('.d-model').textContent = d.model || d.host;
    li.onclick = () => {
        list.querySelectorAll('.device').forEach((x) => x.classList.remove('selected'));
        li.classList.add('selected');
        state.selectedUDN = d.udn;
    };
    list.appendChild(li);
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
        $('deviceHint').textContent = '已自动发现新设备，点击选择';
        toast('发现新设备：' + d.name);
    });
}

// ---------- 视频来源（本地文件 / 在线链接）----------

async function pickVideo() {
    const v = await call('PickVideo');
    if (!v) return; // 用户取消
    state.pending = { type: 'file', ...v };
    renderPending();
}

async function resolveURL() {
    const url = $('urlInput').value.trim();
    if (!url) { toast('请先粘贴视频链接'); return; }
    $('btnResolve').disabled = true;
    try {
        const r = await call('ResolveURL', url);
        state.pending = { type: 'url', ...r };
        renderPending();
    } catch { /* toast 已提示 */ }
    finally { $('btnResolve').disabled = false; }
}

function renderPending() {
    const p = state.pending;
    if (!p) return;
    $('videoCard').classList.remove('hidden');
    $('videoHint').classList.add('hidden');
    $('vName').textContent = p.name || p.title || '未命名';
    if (p.type === 'file') {
        const res = p.width ? `${p.width}×${p.height} · ` : '';
        $('vMeta').textContent = `本地文件 · ${res}${p.videoCodec || '?'} + ${p.audioCodec || '无声'} · ${fmtClock(p.durationSec)} · ${p.sizeMB.toFixed(0)} MB`;
        $('vBadge').textContent = p.directPlay ? '电视可直接解码，原文件直出' : '本地文件需转码（H.264/AAC MPEG-TS 实时转码）';
        $('vBadge').className = 'video-badge ' + (p.directPlay ? 'ok' : 'tc');
    } else {
        const from = p.extractor ? `来源 ${p.extractor}` : '在线视频';
        const dur = p.isLive ? '直播' : fmtClock(p.durationSec);
        $('vMeta').textContent = `${from}${p.uploader ? ' · ' + p.uploader : ''} · ${dur}`;
        $('vBadge').textContent = '在线视频经本机解析转码中转';
        $('vBadge').className = 'video-badge tc';
    }
}

// ---------- 投屏 ----------

async function cast() {
    if (!state.selectedUDN) { toast('请先选择一台播放设备'); return; }
    const p = state.pending;
    if (!p) { toast('请先选择本地视频或解析在线链接'); return; }
    try {
        const st = p.type === 'file'
            ? await call('Cast', state.selectedUDN, p.path)
            : await call('CastURL', state.selectedUDN, p.url);
        startControls(st);
    } catch { /* toast 已提示 */ }
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
        stopControls();
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
