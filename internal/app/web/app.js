import { provinces, matchesCandidate, selectForTest, bestResult } from './node-selection.mjs';

(() => {
  'use strict';

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const tokenKey = 'xdp-api-token';
  const state = {
    token: sessionStorage.getItem(tokenKey) || '', config: null, status: null,
    page: 'service', group: 'global', groups: [], candidates: [], selected: new Set(),
    results: new Map(), job: null, testStarting: false, autoPreparing: false, importing: false, serviceBusy: false, candidateRequest: 0,
    candidateController: null, candidateRequestMode: '', candidateLoading: false, candidateSourceWarning: '',
    logs: [], lastLogID: 0, logsStarted: false, configLoaded: false, statusTimer: null,
    downloads: [], storage: null, downloadsTimer: null, downloadsLoading: false,
    downloadsMutation: 0, downloadsBusy: new Set(), downloadSubmitting: false, delivery: null
  };
  const groupNames = { global: '全局', cn1: 'cn1', cn2: 'cn2', store: 'Microsoft Store' };

  function showLogin(message = '') {
    state.token = '';
    sessionStorage.removeItem(tokenKey);
    $('#appShell').hidden = true;
    $('#loginScreen').hidden = false;
    $('#loginError').textContent = message;
    $('#loginError').hidden = !message;
    $('#tokenInput').focus();
  }

  async function api(path, options = {}) {
    const controller = new AbortController();
    const timeout = options.timeout ?? 5000;
    const timer = setTimeout(() => controller.abort(), timeout);
    const headers = new Headers(options.headers || {});
    if (state.token) headers.set('Authorization', `Bearer ${state.token}`);
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    try {
      const response = await fetch(path, {
        method: options.method || 'GET', headers, signal: controller.signal,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
        cache: 'no-store'
      });
      const body = response.status === 204 ? null : await response.json().catch(() => ({}));
      if (response.status === 401) {
        showLogin('访问令牌无效或已过期，请重新输入。');
        throw new Error('需要重新登录');
      }
      if (!response.ok) {
        const error = new Error(body?.error || `请求失败（${response.status}）`);
        error.status = response.status;
        throw error;
      }
      return body;
    } catch (error) {
      if (error.name === 'AbortError') throw new Error(`请求超时（${Math.ceil(timeout / 1000)} 秒）`);
      throw error;
    } finally {
      clearTimeout(timer);
    }
  }

  function setMessage(text, kind = 'error') {
    const errorBox = $('#globalError');
    const successBox = $('#globalNotice');
    errorBox.hidden = true;
    successBox.hidden = true;
    if (!text) return;
    const box = kind === 'success' ? successBox : errorBox;
    box.textContent = text;
    box.hidden = false;
  }

  function setButtonBusy(button, busy, busyText = '处理中…') {
    if (busy) {
      button.dataset.label = button.textContent.trim();
      button.textContent = busyText;
      button.disabled = true;
    } else {
      button.textContent = button.dataset.label || button.textContent;
      button.disabled = false;
    }
  }

  function formatCount(value) {
    if (value === undefined || value === null || Number.isNaN(Number(value))) return '—';
    return Number(value).toLocaleString('zh-CN');
  }

  function renderStatus(status) {
    state.status = status;
    const running = Boolean(status?.running);
    const stateEl = $('#serviceState');
    stateEl.classList.toggle('is-running', running);
    stateEl.classList.toggle('is-stopped', !running);
    $('span:last-child', stateEl).textContent = running ? '服务运行中' : '服务已停止';
    $('#serviceToggle').textContent = state.serviceBusy ? '处理中…' : running ? '停止服务' : '启动服务';
    $('#serviceToggle').dataset.action = running ? 'stop' : 'start';
    $('#serviceToggle').disabled = state.serviceBusy;
    $('#serviceBanner').className = `service-banner ${running ? 'is-running' : 'is-stopped'}`;
    $('#serviceBannerTitle').textContent = running ? '加速服务已开启' : '加速服务未开启 · 客户端尚未使用加速服务';
    const lanIP = status?.addresses?.lanIP || state.config?.lanIP || '未配置';
    $('#serviceBannerText').textContent = running
      ? `DNS / HTTP${status?.addresses?.tls ? ' / TLS' : ''} 正在监听。请将下载客户端 DNS 设为 ${lanIP}。`
      : status?.lastError ? `启动失败：${status.lastError}。检查端口占用和运行权限后重试。`
        : '管理界面和测速可用。先选节点、保存设置，再点击“启动加速服务”；服务开启后再修改客户端 DNS。已设置 DNS 的客户端请暂时恢复自动获取。';
    $('#bannerStart').hidden = running;
    $('#bannerStart').disabled = state.serviceBusy;
    $('#metricDns').textContent = formatCount(status?.metrics?.dnsQueries);
    $('#metricHttp').textContent = formatCount(status?.metrics?.httpRedirects);
    $('#metricTls').textContent = formatCount(status?.metrics?.tlsConnections);
    renderDelivery(state.delivery);
    $('#metricAddress').textContent = lanIP;
    $('#helpAddress').textContent = lanIP;
    $('#statusUpdated').textContent = `更新于 ${new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
  }

  function pathValue(object, path) {
    return path.split('.').reduce((value, key) => value?.[key], object);
  }

  function setPath(object, path, value) {
    const keys = path.split('.');
    const finalKey = keys.pop();
    const target = keys.reduce((node, key) => node[key] ||= {}, object);
    target[finalKey] = value;
  }

  function renderConfig(config) {
    state.config = config;
    state.configLoaded = true;
    $$('[name]', $('#configForm')).forEach(input => {
      const value = pathValue(config, input.name);
      if (input.type === 'checkbox') input.checked = Boolean(value);
      else input.value = value ?? '';
    });
    $('#configState').textContent = '已读取';
    $('#restartHint').textContent = '';
    if (state.status) renderStatus(state.status);
    const groupConfig = config.groups?.[state.group];
    $('#testURL').value = groupConfig?.testURL || '';
    $('#testURL').placeholder = groupConfig?.testURL ? '' : '未配置测速 URL';
    renderDelivery(state.delivery);
  }

  async function loadConfig() {
    try {
      const config = await api('/api/config');
      renderConfig(config);
    } catch (error) {
      if (state.token) setMessage(`读取配置失败：${error.message}`);
      $('#configState').textContent = '读取失败';
    }
  }

  async function refreshStatus() {
    if (!state.token) return;
    try {
      renderStatus(await api('/api/status'));
    } catch (error) {
      if (state.token) {
        state.status = null;
        $('#serviceState').classList.remove('is-running');
        $('#serviceState').classList.add('is-stopped');
        $('span:last-child', $('#serviceState')).textContent = '状态不可用';
        $('#serviceToggle').disabled = true;
        $('#serviceBanner').className = 'service-banner is-unknown';
        $('#serviceBannerTitle').textContent = '无法确认加速服务状态';
        $('#serviceBannerText').textContent = '与管理服务的连接暂时中断，正在重试。请检查服务进程和网络连接。';
        $('#bannerStart').hidden = true;
      }
    }
  }

  function activatePage(page) {
    state.page = page;
    if (page !== 'speed') closeImportPopover();
    $$('.nav-tab').forEach(tab => {
      const active = tab.dataset.page === page;
      tab.classList.toggle('active', active);
      tab.setAttribute('aria-selected', String(active));
      tab.tabIndex = active ? 0 : -1;
    });
    $$('.page').forEach(section => {
      const active = section.id === `page-${page}`;
      section.hidden = !active;
      section.classList.toggle('active', active);
    });
    setMessage('');
    if (page === 'logs') startLogs();
    if (page === 'speed' && !state.candidates.length) loadCandidates(false);
    if (page === 'downloads') startDownloads();
    window.scrollTo(0, 0);
  }

  function textNode(tag, text, className) {
    const node = document.createElement(tag);
    node.textContent = text ?? '';
    if (className) node.className = className;
    return node;
  }

  function makeEmptyRow(colspan, message) {
    const row = document.createElement('tr');
    const cell = textNode('td', message, 'empty-cell');
    cell.colSpan = colspan;
    row.append(cell);
    return row;
  }

  function closeImportPopover() {
    $('.import-details').open = false;
  }

  function candidateKey(item) { return String(item.ip || '').trim(); }

  function candidateFilters() {
    return { query: $('#candidateSearch').value, province: $('#candidateProvince').value, carrier: $('#candidateCarrier').value };
  }

  function visibleCandidates() {
    const filters = candidateFilters();
    return state.candidates.filter(item => matchesCandidate(item, filters));
  }

  function renderFilterHint(count) {
    const limit = Number($('#autoTestLimit').value);
    const maxBytes = Number(state.config?.test?.maxBytes) || 10485760;
    const budget = Math.min(state.candidates.length ? count : limit, limit);
    $('#regionHint').textContent = `${state.candidates.length ? `当前筛选匹配 ${count} 个节点。` : '自动测速会先加载候选。'}按省份、运营商和搜索条件抽样最多 ${limit} 个，流量上限约 ${(budget * maxBytes / 1048576).toFixed(0)} MiB。地区以候选源标注为准，未标注地区需手选；测完后手动应用并启动服务。`;
  }

  function resultSpeed(result) {
    if (result?.error) return -1;
    const value = Number(result?.mbps);
    return Number.isFinite(value) && value >= 0 ? value : -1;
  }

  function renderCandidates() {
    const visible = visibleCandidates();
    renderFilterHint(visible.length);
    const rows = $('#candidateRows');
    rows.replaceChildren();
    const list = visible.map(item => ({ item, result: state.results.get(candidateKey(item)) }));
    list.sort((a, b) => {
      const speedA = resultSpeed(a.result);
      const speedB = resultSpeed(b.result);
      if (speedA >= 0 || speedB >= 0) return speedB - speedA;
      return candidateKey(a.item).localeCompare(candidateKey(b.item));
    });
    if (!list.length) {
      rows.append(makeEmptyRow(7, state.candidates.length ? '没有符合搜索条件的候选地址。' : '加载候选地址后开始测速。'));
      return;
    }
    for (const { item, result } of list) {
      const ip = candidateKey(item);
      const row = document.createElement('tr');
      const selectCell = document.createElement('td');
      selectCell.className = 'select-col';
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox'; checkbox.checked = state.selected.has(ip);
      checkbox.setAttribute('aria-label', `选择 ${ip}`);
      checkbox.addEventListener('change', () => {
        if (checkbox.checked && state.selected.size >= 128) {
          checkbox.checked = false;
          $('#candidateWarning').textContent = '每次最多选择 128 个地址进行测速。';
          return;
        }
        checkbox.checked ? state.selected.add(ip) : state.selected.delete(ip);
        updateSelectionCount();
      });
      selectCell.append(checkbox);
      const ipCell = textNode('td', ip, 'mono ip-cell');
      const locationCell = textNode('td', item.location || '—', 'location-cell');
      const latency = Number(result?.latencyMS);
      const latencyCell = textNode('td', Number.isFinite(latency) && latency >= 0 ? `${latency.toFixed(1)} ms` : '—', 'mono');
      const speed = Number(result?.mbps);
      const mibPerSecond = speed / 8.388608;
      const speedCell = textNode('td', !result?.error && Number.isFinite(speed) && speed >= 0 ? `${mibPerSecond.toFixed(2)} MiB/s` : '—', 'mono speed-value');
      const outcome = result?.error ? result.error : result ? '完成' : '未测试';
      const stateCell = textNode('td', outcome, result?.error ? 'result-error' : result ? 'result-ok' : 'muted');
      const actionCell = document.createElement('td');
      const apply = textNode('button', '应用', 'text-button apply-button');
      apply.type = 'button'; apply.disabled = Boolean(state.job || state.testStarting || state.autoPreparing);
      apply.addEventListener('click', () => applyCandidate(ip, item.location || ''));
      actionCell.append(apply);
      row.append(selectCell, ipCell, locationCell, latencyCell, speedCell, stateCell, actionCell);
      rows.append(row);
    }
  }

  function updateSelectionCount() {
    $('#selectedCount').textContent = `${state.selected.size}/128`;
    updateTestControls();
  }

  function renderCandidateMeta(data) {
    const when = data.updatedAt ? new Date(data.updatedAt).toLocaleString('zh-CN') : '';
    const source = data.origin ? `来源：${data.origin}` : '';
    $('#candidateSummary').textContent = `${data.items?.length || 0} 个候选地址${source ? ` · ${source}` : ''}${when ? ` · 更新于 ${when}` : ''}`;
    state.candidateSourceWarning = data.warning || '';
    $('#candidateWarning').textContent = state.candidateSourceWarning;
  }

  function updateCandidateButtons() {
    const load = $('#loadCandidates');
    const refresh = $('#refreshCandidates');
    const busy = state.candidateLoading && state.candidateRequestMode;
    const testing = Boolean(state.job || state.testStarting || state.autoPreparing || state.importing);
    load.disabled = Boolean(busy || testing);
    refresh.disabled = Boolean(busy || testing);
    load.textContent = busy && state.candidateRequestMode === 'load' ? '加载中…' : '加载候选';
    refresh.textContent = busy && state.candidateRequestMode === 'refresh' ? '更新中…' : '在线更新';
  }

  function updateTestControls() {
    const busy = Boolean(state.job || state.testStarting || state.autoPreparing || state.importing);
    $('#speedGroup').disabled = busy;
    $('#loadCandidates').disabled = busy || state.candidateLoading;
    $('#refreshCandidates').disabled = busy || state.candidateLoading;
    $('#importCandidates').disabled = busy || state.candidateLoading;
    $('#importText').disabled = busy;
    $('#autoTest').disabled = busy || state.candidateLoading;
    $('#autoTest').textContent = state.autoPreparing ? '正在准备…' : '按地区自动测速';
    for (const id of ['candidateProvince', 'candidateCarrier', 'autoTestLimit', 'candidateSearch', 'selectVisible', 'clearSelection', 'testURL']) {
      $(`#${id}`).disabled = busy;
    }
    $('#startTest').disabled = busy || state.candidateLoading || state.selected.size === 0;
    $$('.apply-button').forEach(button => { button.disabled = busy; });
  }

  async function loadCandidates(refresh = false) {
    const requestId = ++state.candidateRequest;
    if (state.candidateController) state.candidateController.abort();
    const controller = new AbortController();
    state.candidateController = controller;
    const currentGroup = state.group;
    state.candidateRequestMode = refresh ? 'refresh' : 'load';
    state.candidateLoading = true;
    updateCandidateButtons();
    updateSelectionCount();
    $('#candidateSummary').textContent = refresh ? '正在在线获取候选地址…' : '正在读取候选地址…';
    $('#candidateWarning').textContent = '';
    const timeoutController = setTimeout(() => controller.abort(), 20000);
    try {
      const headers = new Headers({ Authorization: `Bearer ${state.token}` });
      const response = await fetch(`/api/candidates?group=${encodeURIComponent(currentGroup)}${refresh ? '&refresh=true' : ''}`, { headers, signal: controller.signal, cache: 'no-store' });
      const data = await response.json().catch(() => ({}));
      if (response.status === 401) { showLogin('访问令牌无效或已过期，请重新输入。'); throw new Error('需要重新登录'); }
      if (!response.ok) throw new Error(data?.error || `请求失败（${response.status}）`);
      if (requestId !== state.candidateRequest || currentGroup !== state.group) return;
      state.candidates = Array.isArray(data.items) ? data.items : [];
      state.selected.clear(); state.results.clear();
      $('#testProgress').hidden = true;
      $('#testRecommendation').hidden = true;
      renderCandidateMeta(data); renderCandidates(); updateSelectionCount();
      return true;
    } catch (error) {
      if (requestId === state.candidateRequest && currentGroup === state.group && state.token) {
        const message = error.name === 'AbortError' ? '候选地址请求超时，请稍后重试。' : `无法加载候选地址：${error.message}`;
        $('#candidateSummary').textContent = message;
        $('#candidateRows').replaceChildren(makeEmptyRow(7, '检查网络连接或稍后重试。'));
      }
    } finally {
      clearTimeout(timeoutController);
      if (requestId === state.candidateRequest) {
        state.candidateLoading = false;
        state.candidateRequestMode = '';
        state.candidateController = null;
        updateCandidateButtons();
        updateSelectionCount();
      }
    }
  }

  async function importCandidates() {
    const text = $('#importText').value.trim();
    if (!text) { $('#candidateWarning').textContent = '请先粘贴 IP 地址。'; return; }
    const group = state.group;
    const requestId = state.candidateRequest;
    const button = $('#importCandidates'); setButtonBusy(button, true, '正在导入…');
    state.importing = true; updateTestControls();
    try {
      const data = await api('/api/candidates/import', { method: 'POST', body: { group, text } });
      if (state.page !== 'speed' || group !== state.group || requestId !== state.candidateRequest) return;
      state.candidates = Array.isArray(data.items) ? data.items : [];
      state.selected.clear(); state.results.clear();
      $('#testProgress').hidden = true;
      $('#testRecommendation').hidden = true;
      renderCandidateMeta(data); renderCandidates(); updateSelectionCount();
      $('#importText').value = '';
      closeImportPopover();
    } catch (error) {
      if (group === state.group && requestId === state.candidateRequest) $('#candidateWarning').textContent = `导入失败：${error.message}`;
    }
    finally { state.importing = false; setButtonBusy(button, false); updateTestControls(); }
  }

  async function autoTest() {
    if (state.job || state.testStarting || state.autoPreparing || state.candidateLoading || state.importing) return;
    $('#candidateWarning').textContent = state.candidateSourceWarning;
    if (!$('#candidateProvince').value) {
      $('#candidateWarning').textContent = '请先选择所在省份 / 地区，再自动测速。';
      $('#candidateProvince').focus(); return;
    }
    if (!$('#testURL').value.trim()) {
      $('#candidateWarning').textContent = '此域名组还没有测速 URL，请先填写真实可下载的资源地址。';
      $('#testURL').focus(); return;
    }
    state.autoPreparing = true; updateTestControls();
    try {
      if (!state.candidates.length && !await loadCandidates(false)) return;
      const chosen = selectForTest(state.candidates, candidateFilters(), Number($('#autoTestLimit').value));
      if (!chosen.length) {
        $('#candidateWarning').textContent = '没有匹配的地区节点。请放宽运营商或搜索条件，尝试其他省份，或手动导入带地区标注的候选。';
        return;
      }
      state.selected = new Set(chosen.map(candidateKey));
      renderCandidates(); updateSelectionCount();
      await startTest();
    } finally { state.autoPreparing = false; updateTestControls(); }
  }

  async function startTest() {
    if (state.job || state.testStarting) return;
    const ips = [...state.selected].slice(0, 128);
    if (!ips.length) return;
    const testURL = $('#testURL').value.trim();
    if (!testURL) { setMessage('请填写有效的测速 URL。'); $('#testURL').focus(); return; }
    setMessage('');
    $('#testRecommendation').hidden = true;
    state.testStarting = true;
    updateTestControls();
    try {
      const job = await api('/api/tests', { method: 'POST', body: { group: state.group, ips, testURL } });
      state.job = job;
      state.testStarting = false;
      state.results.clear(); renderJob(job); pollJob(job.id);
    } catch (error) {
      state.testStarting = false;
      setMessage(`启动测速失败：${error.message}`);
      updateTestControls(); updateSelectionCount();
    }
  }

  function renderJob(job) {
    state.job = job.state === 'running' ? job : null;
    const running = job.state === 'running';
    $('#cancelTest').hidden = !running;
    $('#testProgress').hidden = false;
    const total = Number(job.total) || 0;
    const completed = Number(job.completed) || 0;
    $('#progressText').textContent = running ? '正在测试选中节点' : job.state === 'cancelled' ? '测速已取消' : '测速完成';
    $('#progressCount').textContent = `${completed} / ${total}`;
    $('#progressBar').max = Math.max(total, 1); $('#progressBar').value = Math.min(completed, total);
    if (Array.isArray(job.results)) {
      for (const result of job.results) state.results.set(String(result.ip), result);
    }
    if (!running) {
      const best = bestResult(job.results || []);
      $('#testRecommendation').hidden = false;
      $('#applyBest').hidden = !best;
      $('#applyBest').dataset.ip = best?.ip || '';
      $('#recommendationText').textContent = best
        ? `${job.state === 'cancelled' ? '测速已取消，已完成样本中' : '本轮'}最快：${best.ip} · ${(best.mbps / 8.388608).toFixed(2)} MiB/s。应用后请手动启动加速服务。`
        : '本轮没有可用的测速结果。请检查测速 URL、候选节点或网络连接后重试。';
    }
    renderCandidates(); updateSelectionCount(); updateTestControls();
  }

  async function pollJob(id) {
    while (state.job?.id === id && state.token) {
      await new Promise(resolve => setTimeout(resolve, 1000));
      if (state.job?.id !== id) return;
      try {
        const job = await api(`/api/tests/${encodeURIComponent(id)}`);
        if (state.job?.id !== id) return;
        renderJob(job);
        if (job.state !== 'running') return;
      } catch (error) {
        if (state.job?.id !== id || !state.token) return;
        $('#progressText').textContent = `读取进度暂时失败，将自动重试：${error.message}`;
      }
    }
  }

  async function cancelTest() {
    if (!state.job) return;
    const job = state.job;
    $('#cancelTest').disabled = true;
    try {
      renderJob(await api(`/api/tests/${encodeURIComponent(job.id)}/cancel`, { method: 'POST', body: {} }));
    } catch (error) { $('#progressText').textContent = `取消失败：${error.message}`; }
    finally { $('#cancelTest').disabled = false; }
  }

  async function applyCandidate(ip, location) {
    if (state.job || state.testStarting || state.autoPreparing) return;
    const button = $$('.apply-button').find(item => item.closest('tr')?.children[1]?.textContent === ip);
    if (button) { button.disabled = true; button.textContent = '应用中…'; }
    try {
      const data = await api('/api/apply', { method: 'POST', body: { group: state.group, ip } });
      if (data.config) renderConfig(data.config);
      renderStatus({ ...(state.status || {}), ...(data.status || {}), running: false });
      setMessage(`已将 ${ip}${location ? `（${location}）` : ''} 应用到${groupNames[state.group]}。服务已停止，请检查设置后手动重新启动。`, 'success');
    } catch (error) { setMessage(`应用节点失败：${error.message}`); }
    finally { if (button?.isConnected) { button.disabled = false; button.textContent = '应用'; } }
  }

  function formatStorageBytes(value) {
    const bytes = Number(value);
    if (!Number.isFinite(bytes) || bytes < 0) return '—';
    if (bytes < 1024) return `${Math.round(bytes)} B`;
    const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
    let amount = bytes;
    let unit = -1;
    do { amount /= 1024; unit++; } while (amount >= 1024 && unit < units.length - 1);
    return `${amount.toFixed(amount >= 100 ? 0 : amount >= 10 ? 1 : 2)} ${units[unit]}`;
  }

  function downloadStateLabel(value) {
    return ({ queued: '排队中', running: '下载中', paused: '已暂停', completed: '已完成', failed: '失败', cancelled: '已取消' })[value] || String(value || '未知');
  }

  function renderDelivery(delivery = state.delivery) {
    const enabled = Boolean(state.config?.offlineEnabled ?? delivery?.enabled);
    const running = Boolean(state.status?.running ?? delivery?.running);
    const hits = state.status?.metrics?.localHits ?? delivery?.hits;
    const bytes = state.status?.metrics?.localBytes ?? delivery?.bytes;
    $('#deliveryPanel').classList.toggle('is-active', enabled && running);
    $('#deliveryState').textContent = !enabled
      ? '离线下载已关闭，当前仅提供节点加速。可在服务设置中启用“离线下载与 Xbox 本地回传”。'
      : !running ? '回传已配置，但加速服务未启动。启动服务后即可回传已完成的 Xbox HTTP 资源。'
        : '回传运行中：已完成且受支持的 Xbox HTTP 资源可由 Xbox 继续下载。';
    $('#deliveryHits').textContent = formatCount(hits);
    $('#deliveryBytes').textContent = formatStorageBytes(bytes);
    $('#createDownload').disabled = !enabled || state.downloadSubmitting;
  }

  function setDownloadsMessage(message, kind = 'error') {
    const error = $('#downloadsError');
    const notice = $('#downloadsNotice');
    error.hidden = true; notice.hidden = true;
    if (!message) return;
    const target = kind === 'success' ? notice : error;
    target.textContent = message;
    target.hidden = false;
  }

  function renderStorage(storage) {
    state.storage = storage || null;
    $('#storagePath').textContent = storage?.path || '下载目录不可用';
    $('#storageTotal').textContent = formatStorageBytes(storage?.totalBytes);
    $('#storageUsed').textContent = formatStorageBytes(storage?.usedBytes);
    $('#storageFree').textContent = formatStorageBytes(storage?.freeBytes);
    const availableBytes = Number(storage?.availableBytes);
    const reserveBytes = Number(storage?.reserveBytes);
    const downloadableBytes = Number.isFinite(availableBytes) && Number.isFinite(reserveBytes)
      ? Math.max(0, availableBytes - reserveBytes) : NaN;
    $('#storageAvailable').textContent = formatStorageBytes(downloadableBytes);
    $('#storageDownloads').textContent = formatStorageBytes(storage?.downloadBytes);
    const reserve = formatStorageBytes(storage?.reserveBytes);
    $('#storageNote').textContent = storage?.reserveBytes === undefined
      ? '可供下载空间会扣除预留空间；默认预留 256 MiB。'
      : `可供下载空间已扣除 ${reserve} 预留空间；默认预留 256 MiB。`;
    $('#storageUpdated').textContent = `更新于 ${new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
  }

  function safeDownloadName(task) {
    let name = String(task.name || '').trim();
    if (!name) {
      try {
        const segments = new URL(task.url).pathname.split('/').filter(Boolean);
        name = segments.length ? decodeURIComponent(segments.at(-1)) : '';
      } catch { name = ''; }
    }
    name = name.replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_').replace(/^\.+$/, '');
    return name.slice(0, 255) || `download-${task.id}`;
  }

  function renderDownloads() {
    const tbody = $('#downloadRows');
    tbody.replaceChildren();
    if (!state.downloads.length) {
      tbody.append(makeEmptyRow(7, state.downloadsLoading ? '正在读取离线下载任务…' : '还没有离线下载任务。填写下载 URL 后创建一个任务。'));
      return;
    }
    for (const task of state.downloads) {
      const id = String(task.id);
      const row = document.createElement('tr');
      const nameCell = textNode('td', task.name || `下载任务 ${id}`, 'download-name');
      nameCell.title = `任务 ${id}`;
      if (Number(task.totalBytes) > 0 && task.state !== 'cancelled') {
        const progress = document.createElement('progress');
        progress.max = Number(task.totalBytes);
        progress.value = Number(task.bytes) || 0;
        progress.setAttribute('aria-label', `${task.name} 下载进度`);
        nameCell.append(progress);
        nameCell.append(textNode('small', `${Math.min(100, 100 * (Number(task.bytes) || 0) / Number(task.totalBytes)).toFixed(1)}%`));
      }
      row.append(nameCell);
      const taskState = textNode('span', downloadStateLabel(task.state), `download-state state-${task.state}`);
      const stateCell = document.createElement('td'); stateCell.append(taskState);
      if (task.state === 'completed') {
        const deliveryLabel = !task.localReady ? '格式不支持回传或文件不可用'
          : !(state.config?.offlineEnabled ?? state.delivery?.enabled) ? '未开启回传'
            : !(state.status?.running ?? state.delivery?.running) ? '服务未启动' : '可回传';
        stateCell.append(textNode('small', deliveryLabel, 'delivery-task-state'));
      }
      row.append(stateCell);
      const received = formatStorageBytes(task.bytes);
      const totalBytes = Number(task.totalBytes);
      const expected = Number.isFinite(totalBytes) && totalBytes >= 0 ? formatStorageBytes(totalBytes) : '未知';
      row.append(textNode('td', received, 'mono'));
      row.append(textNode('td', expected, 'mono'));
      const speed = Number(task.speedBPS);
      row.append(textNode('td', Number.isFinite(speed) && speed >= 0 ? `${(speed / 1048576).toFixed(2)} MiB/s` : '—', 'mono'));
      row.append(textNode('td', task.error || '—', task.error ? 'download-error' : 'muted'));
      const actions = document.createElement('td');
      actions.className = 'download-actions';
      const busy = state.downloadsBusy.has(id) || state.downloadSubmitting;
      const addAction = (label, handler, className = 'button secondary') => {
        const button = textNode('button', label, `${className} mini-action`);
        button.type = 'button'; button.disabled = busy;
        button.addEventListener('click', handler);
        actions.append(button);
      };
      if (task.state === 'running' || task.state === 'queued') {
        addAction('暂停', () => updateDownloadTask(task, 'pause'));
        addAction('取消', () => updateDownloadTask(task, 'cancel'));
      } else if (task.state === 'paused' || task.state === 'failed') {
        addAction('继续', () => updateDownloadTask(task, 'resume'));
        addAction('取消', () => updateDownloadTask(task, 'cancel'));
      } else if (task.state === 'completed') {
        addAction('保存到此设备', () => saveDownloadFile(task), 'button primary');
      }
      if (task.state === 'running') {
        const hint = textNode('span', '请先暂停，再删除', 'muted delete-running-hint');
        hint.title = '下载任务运行中时不能删除文件';
        actions.append(hint);
      } else {
        addAction('删除文件', () => deleteDownloadTask(task), 'button danger-outline');
      }
      row.append(actions);
      tbody.append(row);
    }
  }

  function mergeDownloadTask(task) {
    const id = String(task.id);
    const index = state.downloads.findIndex(existing => String(existing.id) === id);
    if (index < 0) state.downloads.unshift(task);
    else state.downloads[index] = task;
    state.downloadsMutation++;
    renderDownloads();
  }

  async function refreshDownloads() {
    if (state.page !== 'downloads' || !state.token || state.downloadsLoading) return;
    state.downloadsLoading = true;
    const revision = state.downloadsMutation;
    $('#refreshDownloads').disabled = true;
    if (!state.downloads.length) renderDownloads();
    try {
      const data = await api('/api/downloads');
      if (state.page !== 'downloads' || revision !== state.downloadsMutation) return;
      state.downloads = Array.isArray(data.tasks) ? data.tasks : [];
      state.delivery = data.delivery || state.delivery;
      renderDelivery(state.delivery);
      renderStorage(data.storage);
      renderDownloads();
      if ($('#downloadsError').textContent.startsWith('读取离线下载状态失败：')) $('#downloadsError').hidden = true;
    } catch (error) {
      if (state.page === 'downloads') setDownloadsMessage(`读取离线下载状态失败：${error.message}`);
    } finally {
      state.downloadsLoading = false;
      $('#refreshDownloads').disabled = false;
      if (!state.downloads.length) renderDownloads();
    }
  }

  function startDownloads() {
    setDownloadsMessage('');
    refreshDownloads();
    if (!state.downloadsTimer) {
      state.downloadsTimer = setInterval(() => {
        if (state.page === 'downloads') refreshDownloads();
      }, 2000);
    }
  }

  async function createDownload(event) {
    event.preventDefault();
    if (state.downloadSubmitting) return;
    if (!(state.config?.offlineEnabled ?? state.delivery?.enabled)) {
      setDownloadsMessage('请先在服务设置中启用“离线下载与 Xbox 本地回传”。');
      return;
    }
    const rawURL = $('#downloadURL').value.trim();
    let url;
    try { url = new URL(rawURL); }
    catch { setDownloadsMessage('请输入有效的下载 URL。'); $('#downloadURL').focus(); return; }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') {
      setDownloadsMessage('下载地址必须使用 HTTP 或 HTTPS。'); $('#downloadURL').focus(); return;
    }
    state.downloadSubmitting = true;
    const button = $('#createDownload');
    setButtonBusy(button, true, '正在创建…');
    $$('.download-actions button').forEach(action => { action.disabled = true; });
    try {
      const task = await api('/api/downloads', {
        method: 'POST', body: { url: url.href, name: $('#downloadName').value.trim() }
      });
      mergeDownloadTask(task);
      $('#downloadURL').value = '';
      $('#downloadName').value = '';
      setDownloadsMessage('下载任务已创建。', 'success');
    } catch (error) { setDownloadsMessage(`创建下载任务失败：${error.message}`); }
    finally {
      state.downloadSubmitting = false;
      setButtonBusy(button, false);
      renderDelivery(state.delivery);
      renderDownloads();
    }
  }

  async function updateDownloadTask(task, action) {
    const id = String(task.id);
    if (state.downloadsBusy.has(id)) return;
    state.downloadsBusy.add(id); renderDownloads();
    try {
      const updated = await api(`/api/downloads/${encodeURIComponent(id)}/${action}`, { method: 'POST', body: {} });
      mergeDownloadTask(updated);
      setDownloadsMessage(`任务已${action === 'pause' ? '暂停' : action === 'resume' ? '继续' : '取消'}。`, 'success');
    } catch (error) { setDownloadsMessage(`任务操作失败：${error.message}`); }
    finally { state.downloadsBusy.delete(id); renderDownloads(); }
  }

  async function deleteDownloadTask(task) {
    if (task.state === 'running') {
      setDownloadsMessage('请先暂停下载任务，再删除文件。');
      return;
    }
    const id = String(task.id);
    const name = task.name || `下载任务 ${id}`;
    if (!window.confirm(`确定永久删除“${name}”及其已下载文件吗？此操作无法恢复。`)) return;
    if (state.downloadsBusy.has(id)) return;
    state.downloadsBusy.add(id); renderDownloads();
    try {
      await api(`/api/downloads/${encodeURIComponent(id)}`, { method: 'DELETE' });
      state.downloads = state.downloads.filter(item => String(item.id) !== id);
      state.downloadsMutation++;
      renderDownloads();
      setDownloadsMessage('任务及其已下载文件已永久删除。', 'success');
      await refreshDownloads();
    } catch (error) { setDownloadsMessage(`删除失败：${error.message}`); }
    finally { state.downloadsBusy.delete(id); renderDownloads(); }
  }

  async function authorizedFileResponse(id) {
    const response = await fetch(`/api/downloads/${encodeURIComponent(id)}/file`, {
      headers: { Authorization: `Bearer ${state.token}` }, cache: 'no-store'
    });
    if (response.status === 401) {
      showLogin('访问令牌无效或已过期，请重新输入。');
      throw new Error('需要重新登录');
    }
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      const error = new Error(body?.error || `文件读取失败（${response.status}）`);
      error.status = response.status;
      throw error;
    }
    return response;
  }

  async function saveDownloadFile(task) {
    const id = String(task.id);
    if (state.downloadsBusy.has(id)) return;
    state.downloadsBusy.add(id); renderDownloads();
    const filename = safeDownloadName(task);
    let writable = null;
    try {
      if (typeof window.showSaveFilePicker === 'function') {
        let handle;
        try { handle = await window.showSaveFilePicker({ suggestedName: filename }); }
        catch (error) { if (error.name === 'AbortError') return; throw error; }
        const response = await authorizedFileResponse(id);
        if (!response.body) throw new Error('浏览器无法读取文件流。');
        writable = await handle.createWritable();
        const reader = response.body.getReader();
        try {
          while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            await writable.write(value);
          }
          await writable.close();
          writable = null;
        } catch (error) {
          await writable.abort().catch(() => {});
          writable = null;
          throw error;
        }
      } else {
        const response = await authorizedFileResponse(id);
        const maxBytes = 256 * 1024 * 1024;
        const contentLength = Number(response.headers.get('Content-Length'));
        if (Number.isFinite(contentLength) && contentLength > maxBytes) {
          await response.body?.cancel().catch(() => {});
          throw new Error('文件超过浏览器回退保存的 256 MiB 限制。请从 NAS 下载目录读取，或使用 CLI 输出位置。');
        }
        if (!response.body) throw new Error('浏览器无法流式读取文件。');
        const reader = response.body.getReader();
        const chunks = [];
        let received = 0;
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          received += value.byteLength;
          if (received > maxBytes) {
            await reader.cancel().catch(() => {});
            throw new Error('文件超过浏览器回退保存的 256 MiB 限制。请从 NAS 下载目录读取，或使用 CLI 输出位置。');
          }
          chunks.push(value);
        }
        const blob = new Blob(chunks, { type: response.headers.get('Content-Type') || 'application/octet-stream' });
        const objectURL = URL.createObjectURL(blob);
        const anchor = document.createElement('a');
        anchor.href = objectURL; anchor.download = filename; anchor.hidden = true;
        let revokeScheduled = false;
        try {
          document.body.append(anchor); anchor.click();
          setTimeout(() => URL.revokeObjectURL(objectURL), 60000);
          revokeScheduled = true;
        } finally {
          anchor.remove();
          if (!revokeScheduled) URL.revokeObjectURL(objectURL);
        }
      }
      setDownloadsMessage(`“${filename}”已保存到此设备。`, 'success');
    } catch (error) {
      setDownloadsMessage(`保存文件失败：${error.message}`);
    } finally {
      if (writable) await writable.abort().catch(() => {});
      state.downloadsBusy.delete(id);
      renderDownloads();
    }
  }

  function renderLogs() {
    const tbody = $('#logRows'); tbody.replaceChildren();
    const query = $('#logSearch').value.trim().toLocaleLowerCase();
    const kind = $('#logKind').value;
    const filtered = state.logs.filter(item => {
      if (kind && !String(item.kind || '').toLowerCase().startsWith(kind)) return false;
      return !query || `${item.client || ''} ${item.host || ''} ${item.message || ''} ${item.url || ''}`.toLocaleLowerCase().includes(query);
    });
    $('#logCount').textContent = `${filtered.length} 条记录`;
    if (!filtered.length) {
      tbody.append(makeEmptyRow(5, state.logs.length ? '没有符合筛选条件的请求。' : '还没有请求日志。客户端发起 DNS 请求后会显示在这里。'));
      return;
    }
    for (const item of filtered) {
      const row = document.createElement('tr');
      const date = item.time ? new Date(item.time) : null;
      row.append(textNode('td', date && !Number.isNaN(date.valueOf()) ? date.toLocaleString('zh-CN') : String(item.time || '—'), 'log-time'));
      row.append(textNode('td', String(item.kind || '—').toUpperCase(), `kind-pill kind-${String(item.kind || '').toLowerCase()}`));
      row.append(textNode('td', item.client || '—', 'mono'));
      row.append(textNode('td', item.host || '—', 'mono host-cell'));
      const detail = document.createElement('td');
      const message = textNode('span', item.message || '—', 'log-message'); detail.append(message);
      if (item.url) {
        const copy = textNode('button', '复制 URL', 'text-button copy-button'); copy.type = 'button';
        copy.addEventListener('click', async () => {
          try { await copyText(String(item.url)); copy.textContent = '已复制'; setTimeout(() => { if (copy.isConnected) copy.textContent = '复制 URL'; }, 1500); }
          catch { copy.textContent = '无法复制'; }
        });
        detail.append(copy);
        const offline = textNode('button', '加入离线下载', 'text-button copy-button'); offline.type = 'button';
        offline.addEventListener('click', () => {
          $('#downloadURL').value = String(item.url);
          activatePage('downloads');
          $('#downloadURL').focus();
        });
        detail.append(offline);
      }
      row.append(detail); tbody.append(row);
    }
  }

  async function copyText(value) {
    if (navigator.clipboard?.writeText) {
      try { await navigator.clipboard.writeText(value); return; } catch { /* Use the local document fallback. */ }
    }
    const active = document.activeElement;
    const ranges = [];
    const selection = window.getSelection();
    if (selection) for (let index = 0; index < selection.rangeCount; index++) ranges.push(selection.getRangeAt(index).cloneRange());
    const inputSelection = active && typeof active.selectionStart === 'number'
      ? { start: active.selectionStart, end: active.selectionEnd, direction: active.selectionDirection }
      : null;
    const textarea = document.createElement('textarea');
    textarea.value = value;
    textarea.setAttribute('readonly', '');
    textarea.style.position = 'fixed'; textarea.style.opacity = '0'; textarea.style.pointerEvents = 'none';
    document.body.append(textarea);
    textarea.select(); textarea.setSelectionRange(0, textarea.value.length);
    let copied = false;
    try { copied = document.execCommand('copy'); } catch { copied = false; } finally { textarea.remove(); }
    if (active?.isConnected && typeof active.focus === 'function') {
      active.focus({ preventScroll: true });
      if (inputSelection && typeof active.setSelectionRange === 'function') {
        active.setSelectionRange(inputSelection.start, inputSelection.end, inputSelection.direction);
      }
    }
    if (selection && ranges.length) {
      selection.removeAllRanges();
      ranges.forEach(range => selection.addRange(range));
    }
    if (!copied) throw new Error('复制失败');
  }

  async function refreshLogs() {
    if (state.page !== 'logs' || !state.token) return;
    const button = $('#refreshLogs');
    button.disabled = true;
    try {
      const data = await api(`/api/logs?after=${encodeURIComponent(state.lastLogID)}`);
      if (state.page !== 'logs') return;
      const incoming = Array.isArray(data.items) ? data.items : [];
      state.logs = [...state.logs, ...incoming].slice(-500);
      if (data.lastID !== undefined) state.lastLogID = Number(data.lastID) || state.lastLogID;
      else if (incoming.length) state.lastLogID = Math.max(state.lastLogID, ...incoming.map(item => Number(item.id) || 0));
      renderLogs();
    } catch (error) {
      $('#logCount').textContent = `日志读取失败：${error.message}`;
    } finally { button.disabled = false; }
  }

  function startLogs() {
    if (!state.logsStarted) {
      state.logsStarted = true;
      refreshLogs();
      setInterval(() => { if (state.page === 'logs') refreshLogs(); }, 2000);
    } else refreshLogs();
  }

  async function saveConfig(event) {
    event.preventDefault();
    if (!state.configLoaded) return;
    const button = $('#saveConfig');
    setButtonBusy(button, true, '保存中…');
    $('#configState').textContent = '正在保存';
    const merged = structuredClone(state.config);
    $$('[name]', $('#configForm')).forEach(input => {
      const raw = input.value.trim();
      const value = input.type === 'checkbox' ? input.checked
        : input.type === 'number' ? (raw === '' ? pathValue(merged, input.name) : Number(raw))
          : raw;
      setPath(merged, input.name, value);
    });
    try {
      const saved = await api('/api/config', { method: 'PUT', body: merged });
      renderConfig(saved || merged);
      $('#configState').textContent = '已保存';
      setMessage('网络配置已保存；Web 监听地址需重启进程生效。', 'success');
      await refreshStatus();
    } catch (error) {
      $('#configState').textContent = '保存失败';
      const message = error.status === 409 ? '服务运行中，需先停止服务再保存这些设置。' : `保存设置失败：${error.message}`;
      setMessage(message);
    } finally { setButtonBusy(button, false); }
  }

  async function toggleService() {
    const button = $('#serviceToggle');
    const action = button.dataset.action;
    if (!action || button.disabled) return;
    state.serviceBusy = true;
    button.disabled = true;
    $('#bannerStart').disabled = true;
    button.textContent = action === 'start' ? '正在启动…' : '正在停止…';
    try {
      renderStatus(await api(`/api/service/${action}`, { method: 'POST', body: {} }));
      setMessage(action === 'start' ? '服务已启动。' : '服务已停止，请将下载客户端 DNS 手动恢复为自动获取。', 'success');
    } catch (error) { setMessage(`服务${action === 'start' ? '启动' : '停止'}失败：${error.message}`); await refreshStatus(); }
    finally { state.serviceBusy = false; if (state.status) renderStatus(state.status); }
  }

  async function loadGroups() {
    try {
      const groups = await api('/api/groups');
      state.groups = Array.isArray(groups) ? groups : [];
    } catch { state.groups = []; }
  }

  function changeGroup(group) {
    if (group === state.group || state.job || state.testStarting || state.autoPreparing || state.importing) return;
    closeImportPopover();
    state.group = group;
    state.candidateRequest++;
    state.candidateController?.abort();
    state.candidateController = null;
    state.candidateLoading = false;
    state.candidateRequestMode = '';
    updateCandidateButtons();
    state.candidates = []; state.selected.clear(); state.results.clear();
    state.candidateSourceWarning = '';
    $('#testProgress').hidden = true;
    $('#testRecommendation').hidden = true;
    $('#candidateSummary').textContent = '尚未加载候选地址';
    $('#candidateWarning').textContent = '';
    $('#testURL').value = state.config?.groups?.[group]?.testURL || '';
    $('#testURL').placeholder = $('#testURL').value ? '' : '未配置测速 URL';
    $('#candidateRows').replaceChildren(makeEmptyRow(7, '加载候选地址后开始测速。'));
    updateSelectionCount();
    updateTestControls();
    if (state.page === 'speed') loadCandidates(false);
  }

  async function connect(token) {
    if (!token) { showLogin(); return; }
    state.token = token;
    try {
      const status = await api('/api/status');
      sessionStorage.setItem(tokenKey, token);
      $('#loginScreen').hidden = true; $('#appShell').hidden = false;
      renderStatus(status);
      await Promise.all([loadConfig(), loadGroups()]);
      if (!state.statusTimer) state.statusTimer = setInterval(refreshStatus, 3000);
      setMessage('');
    } catch (error) {
      if (state.token) showLogin(`无法连接：${error.message}`);
    }
  }

  $('#loginForm').addEventListener('submit', event => {
    event.preventDefault();
    const token = $('#tokenInput').value.trim();
    if (token) connect(token);
  });
  $$('.nav-tab').forEach(tab => tab.addEventListener('click', () => activatePage(tab.dataset.page)));
  $$('.nav-tab').forEach((tab, index, tabs) => tab.addEventListener('keydown', event => {
    let next;
    if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
    else if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = tabs.length - 1;
    else return;
    event.preventDefault(); activatePage(tabs[next].dataset.page); tabs[next].focus();
  }));
  $('.brand').addEventListener('click', event => { event.preventDefault(); activatePage('service'); });
  $('#serviceToggle').addEventListener('click', toggleService);
  $('#bannerStart').addEventListener('click', toggleService);
  $('#configForm').addEventListener('submit', saveConfig);
  $('#downloadForm').addEventListener('submit', createDownload);
  $('#refreshDownloads').addEventListener('click', refreshDownloads);
  $('#speedGroup').addEventListener('change', event => changeGroup(event.target.value));
  $('#loadCandidates').addEventListener('click', () => loadCandidates(false));
  $('#refreshCandidates').addEventListener('click', () => loadCandidates(true));
  function filtersChanged() {
    $('#candidateWarning').textContent = state.candidateSourceWarning;
    renderCandidates();
  }
  $('#candidateSearch').addEventListener('input', filtersChanged);
  for (const province of provinces) $('#candidateProvince').append(new Option(province, province));
  for (const id of ['candidateProvince', 'candidateCarrier', 'autoTestLimit']) $(`#${id}`).addEventListener('change', filtersChanged);
  $('#autoTest').addEventListener('click', autoTest);
  $('#applyBest').addEventListener('click', () => {
    const ip = $('#applyBest').dataset.ip;
    if (ip) applyCandidate(ip, state.candidates.find(item => item.ip === ip)?.location || '');
  });
  $('#selectVisible').addEventListener('click', () => {
    state.selected = new Set(visibleCandidates().slice(0, 128).map(candidateKey));
    if (state.selected.size >= 128) $('#candidateWarning').textContent = '已达到每次测速 128 个地址的上限。';
    renderCandidates(); updateSelectionCount();
  });
  $('#clearSelection').addEventListener('click', () => { state.selected.clear(); renderCandidates(); updateSelectionCount(); });
  $('#importCandidates').addEventListener('click', importCandidates);
  document.addEventListener('click', event => {
    const popover = $('.import-details');
    if (popover.open && !popover.contains(event.target)) closeImportPopover();
  });
  $('#startTest').addEventListener('click', startTest);
  $('#cancelTest').addEventListener('click', cancelTest);
  $('#logSearch').addEventListener('input', renderLogs);
  $('#logKind').addEventListener('change', renderLogs);
  $('#refreshLogs').addEventListener('click', refreshLogs);
  $('#testURL').addEventListener('input', () => { if (state.config?.groups?.[state.group]) state.config.groups[state.group].testURL = $('#testURL').value; });
  $('#configForm').addEventListener('input', event => {
    if (event.target.name === 'webListen') $('#restartHint').textContent = '修改 Web 管理监听地址后需重启进程。';
  });

  if (state.token) connect(state.token); else showLogin();
})();
