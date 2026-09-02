"use strict";

const state = {
  token: sessionStorage.getItem("codex-link-token") || "",
  snapshot: null,
};

const elements = {
  connectionGrid: document.querySelector("#connection-grid"),
  threadRows: document.querySelector("#thread-rows"),
  threadCount: document.querySelector("#thread-count"),
  queueList: document.querySelector("#queue-list"),
  queueSummary: document.querySelector("#queue-summary"),
  workspaceList: document.querySelector("#workspace-list"),
  deliveryList: document.querySelector("#delivery-list"),
  runtimeStats: document.querySelector("#runtime-stats"),
  responseMode: document.querySelector("#response-mode"),
  visualStyle: document.querySelector("#visual-style"),
  remoteLockState: document.querySelector("#remote-lock-state"),
  toggleLock: document.querySelector("#toggle-lock"),
  loginDialog: document.querySelector("#login-dialog"),
  loginForm: document.querySelector("#login-form"),
  tokenInput: document.querySelector("#token-input"),
  threadDialog: document.querySelector("#thread-dialog"),
  threadForm: document.querySelector("#thread-form"),
  threadWorkspace: document.querySelector("#thread-workspace"),
  threadName: document.querySelector("#thread-name"),
  notice: document.querySelector("#notice"),
  sidebarState: document.querySelector("#sidebar-state"),
  sidebarVersion: document.querySelector("#sidebar-version"),
  lastRefresh: document.querySelector("#last-refresh"),
};

function escapeHTML(value) {
  return String(value == null ? "" : value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function icon(name) {
  return '<svg aria-hidden="true"><use href="#i-' + escapeHTML(name) + '"/></svg>';
}

function formatTime(timestamp) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(Number(timestamp) * 1000));
}

function formatDuration(seconds) {
  const value = Math.max(0, Number(seconds) || 0);
  if (value < 60) return value + " 秒";
  if (value < 3600) return Math.floor(value / 60) + " 分钟";
  if (value < 86400) return Math.floor(value / 3600) + " 小时";
  return Math.floor(value / 86400) + " 天";
}

function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value < 1024) return value + " B";
  if (value < 1024 * 1024) return (value / 1024).toFixed(1) + " KB";
  return (value / 1024 / 1024).toFixed(1) + " MB";
}

function showNotice(message, kind) {
  elements.notice.textContent = message;
  elements.notice.hidden = !message;
  elements.notice.dataset.kind = kind || "warn";
}

function openLogin(message) {
  if (message) showNotice(message, "warn");
  if (!elements.loginDialog.open) elements.loginDialog.showModal();
  elements.tokenInput.focus();
}

async function api(path, options) {
  const settings = Object.assign({}, options || {});
  settings.headers = Object.assign({
    "Content-Type": "application/json",
    "X-Codex-Link-Token": state.token,
  }, settings.headers || {});
  const response = await fetch(path, settings);
  let payload = {};
  try {
    payload = await response.json();
  } catch (_) {
    payload = {};
  }
  if (response.status === 401) {
    state.token = "";
    sessionStorage.removeItem("codex-link-token");
    openLogin("管理令牌已失效，请重新输入。");
    throw new Error("管理令牌无效");
  }
  if (!response.ok) throw new Error(payload.error || "请求失败");
  return payload;
}

function connectionRow(iconName, title, detail, status, statusClass, meta) {
  return '<div class="connection-row">' +
    icon(iconName) +
    "<div><strong>" + escapeHTML(title) + "</strong><small>" + escapeHTML(detail) + "</small></div>" +
    '<span class="status ' + escapeHTML(statusClass) + '">' + escapeHTML(status) + "</span>" +
    '<span class="mono">' + escapeHTML(meta || "—") + "</span>" +
    '<span class="mono">WEB</span>' +
    "</div>";
}

function renderConnections(snapshot) {
  const connection = snapshot.connection;
  const runtime = snapshot.runtime;
  elements.connectionGrid.classList.remove("skeleton");
  elements.connectionGrid.innerHTML =
    connectionRow("link", "微信 Link", connection.account_count + " 个绑定账号", connection.wechat_connected ? "已连接" : "等待连接", connection.wechat_connected ? "ok" : "warn", runtime.wechat.healthy + "/" + runtime.wechat.monitors + " 健康") +
    connectionRow("target", "Codex App Server", "消息直接进入当前 Codex 目标", connection.codex_ready ? "就绪" : "不可用", connection.codex_ready ? "ok" : "warn", runtime.version) +
    connectionRow("folder", "当前工作空间", connection.current_workspace || "未选择", "受信任", "ok", connection.public_url || "仅本机") +
    connectionRow("message", "目标线程", connection.current_thread_name || "将在首次消息时创建", connection.current_thread_id ? "已选择" : "自动", connection.current_thread_id ? "ok" : "", connection.current_thread_id ? connection.current_thread_id.slice(-8) : "NEW");
}

function renderThreads(snapshot) {
  const threads = snapshot.threads || [];
  elements.threadCount.textContent = threads.length + " 个线程";
  if (!threads.length) {
    elements.threadRows.innerHTML = '<tr><td colspan="5" class="empty">还没有可用线程。发送第一条微信消息，或在这里新建。</td></tr>';
    return;
  }
  elements.threadRows.innerHTML = threads.map(function (thread) {
    const action = thread.current
      ? '<span class="current-label">当前目标</span>'
      : '<button class="row-action" data-thread-id="' + escapeHTML(thread.id) + '" data-workspace-id="' + escapeHTML(thread.workspace_id) + '">设为目标</button>';
    return "<tr>" +
      '<td class="thread-title">' + escapeHTML(thread.title) + "<small>" + escapeHTML(thread.short_id) + "</small></td>" +
      "<td>" + escapeHTML(thread.workspace_name || thread.workspace_id) + "</td>" +
      "<td>" + escapeHTML(thread.status || "idle") + "</td>" +
      "<td>" + escapeHTML(formatTime(thread.updated_at)) + "</td>" +
      "<td>" + action + "</td>" +
      "</tr>";
  }).join("");
}

function renderQueue(snapshot) {
  const queue = snapshot.queue || {paused: false, tasks: []};
  const tasks = queue.tasks || [];
  elements.queueSummary.textContent = (queue.paused ? "已暂停 · " : "实时 · ") + tasks.length + " 项";
  if (!tasks.length) {
    elements.queueList.innerHTML = '<div class="empty">队列为空。新的微信消息会直接进入 Codex。</div>';
    return;
  }
  elements.queueList.innerHTML = tasks.slice(0, 6).map(function (task) {
    const controls = task.state === "queued"
      ? '<footer><button data-task-action="move_front" data-task-id="' + escapeHTML(task.id) + '">置顶</button><button data-task-action="delete" data-task-id="' + escapeHTML(task.id) + '">删除</button></footer>'
      : "";
    return '<article class="queue-item ' + escapeHTML(task.state) + '">' +
      '<div class="queue-marker">' + escapeHTML(task.short_id) + "</div>" +
      "<h3>" + escapeHTML(task.summary || "未命名请求") + "</h3>" +
      "<p>" + escapeHTML(task.stage || task.state) + " · " + escapeHTML(formatTime(task.created_at)) + "</p>" +
      controls +
      "</article>";
  }).join("");
}

function renderWorkspaces(snapshot) {
  const workspaces = snapshot.workspaces || [];
  elements.workspaceList.innerHTML = workspaces.length ? workspaces.map(function (workspace) {
    const action = workspace.current
      ? '<span class="current-label">当前</span>'
      : '<button class="row-action" data-workspace-id="' + escapeHTML(workspace.id) + '">切换</button>';
    return '<div class="workspace-row"><strong>' + escapeHTML(workspace.name) + "</strong><code>" + escapeHTML(workspace.root) + "</code>" + action + "</div>";
  }).join("") : '<div class="empty">未配置受信任工作空间。</div>';
  elements.threadWorkspace.innerHTML = workspaces.map(function (workspace) {
    return '<option value="' + escapeHTML(workspace.id) + '"' + (workspace.current ? " selected" : "") + ">" + escapeHTML(workspace.name) + "</option>";
  }).join("");
}

function renderDeliveries(snapshot) {
  const deliveries = snapshot.deliveries || [];
  elements.deliveryList.innerHTML = deliveries.length ? deliveries.slice(0, 8).map(function (delivery) {
    return '<div class="delivery-row"><strong>' + escapeHTML(delivery.title || delivery.id) + "</strong><code>" +
      escapeHTML(formatBytes(delivery.size)) + " · " + escapeHTML(formatTime(delivery.created_at)) +
      '</code><span class="' + (delivery.available ? "current-label" : "mono") + '">' + (delivery.available ? "可交付" : "已过期") + "</span></div>";
  }).join("") : '<div class="empty">暂无交付记录。</div>';
}

function renderOptions(select, options, selected) {
  select.innerHTML = (options || []).map(function (option) {
    const value = option.id || option.value || option.mode || option.style || "";
    const label = option.name || option.label || value;
    return '<option value="' + escapeHTML(value) + '"' + (value === selected ? " selected" : "") + ">" + escapeHTML(label) + "</option>";
  }).join("");
}

function renderInspector(snapshot) {
  const runtime = snapshot.runtime;
  elements.runtimeStats.innerHTML =
    "<div><dt>进程状态</dt><dd>" + escapeHTML(runtime.status) + "</dd></div>" +
    "<div><dt>运行时间</dt><dd>" + escapeHTML(formatDuration(runtime.uptime_seconds)) + "</dd></div>" +
    "<div><dt>执行中</dt><dd>" + escapeHTML(runtime.tasks.running) + "</dd></div>" +
    "<div><dt>等待中</dt><dd>" + escapeHTML(runtime.tasks.queued) + "</dd></div>" +
    "<div><dt>投递中</dt><dd>" + escapeHTML(runtime.tasks.delivering) + "</dd></div>";
  renderOptions(elements.responseMode, snapshot.options.response_modes, snapshot.preferences.response_mode);
  renderOptions(elements.visualStyle, snapshot.options.styles, snapshot.preferences.style);
  const lockEnabled = snapshot.preferences.remote_lock_enabled;
  const locked = snapshot.preferences.remote_locked;
  elements.remoteLockState.textContent = !lockEnabled ? "未配置" : (locked ? "已锁定" : "未锁定");
  elements.toggleLock.disabled = !lockEnabled;
  elements.toggleLock.querySelector("span").textContent = locked ? "解除远程锁定" : "启用远程锁定";
  elements.sidebarState.textContent = runtime.draining ? "正在排空" : (runtime.status === "ready" ? "运行正常" : runtime.status);
  elements.sidebarVersion.textContent = runtime.version || "—";
}

function render(snapshot) {
  state.snapshot = snapshot;
  renderConnections(snapshot);
  renderThreads(snapshot);
  renderQueue(snapshot);
  renderWorkspaces(snapshot);
  renderDeliveries(snapshot);
  renderInspector(snapshot);
  elements.lastRefresh.textContent = new Intl.DateTimeFormat("zh-CN", {hour: "2-digit", minute: "2-digit", second: "2-digit"}).format(new Date());
  showNotice(snapshot.warning || "", snapshot.warning ? "warn" : "");
}

async function refresh() {
  if (!state.token) {
    openLogin("");
    return;
  }
  try {
    render(await api("/api/snapshot"));
  } catch (error) {
    if (state.token) showNotice(error.message, "warn");
  }
}

async function mutate(path, body, successMessage) {
  try {
    await api(path, {method: "POST", body: JSON.stringify(body)});
    if (successMessage) showNotice(successMessage, "ok");
    await refresh();
  } catch (error) {
    showNotice(error.message, "warn");
  }
}

elements.loginForm.addEventListener("submit", async function (event) {
  event.preventDefault();
  state.token = elements.tokenInput.value.trim();
  sessionStorage.setItem("codex-link-token", state.token);
  try {
    const snapshot = await api("/api/snapshot");
    elements.loginDialog.close();
    elements.tokenInput.value = "";
    render(snapshot);
  } catch (_) {
    elements.tokenInput.select();
  }
});

document.querySelector("#refresh").addEventListener("click", refresh);
document.querySelector("#new-thread").addEventListener("click", function () {
  elements.threadDialog.showModal();
  elements.threadName.focus();
});

elements.threadForm.addEventListener("submit", function (event) {
  event.preventDefault();
  if (!event.submitter || event.submitter.value !== "create") {
    elements.threadDialog.close();
    return;
  }
  elements.threadDialog.close();
  mutate("/api/threads/new", {
    workspace_id: elements.threadWorkspace.value,
    name: elements.threadName.value.trim(),
  }, "已创建新的 Codex 线程。");
  elements.threadName.value = "";
});

document.addEventListener("click", function (event) {
  const threadButton = event.target.closest("[data-thread-id]");
  if (threadButton) {
    mutate("/api/threads/target", {
      thread_id: threadButton.dataset.threadId,
      workspace_id: threadButton.dataset.workspaceId,
    }, "已切换微信消息的 Codex 目标。");
    return;
  }
  const workspaceButton = event.target.closest(".workspace-row [data-workspace-id]");
  if (workspaceButton) {
    mutate("/api/workspaces/select", {workspace_id: workspaceButton.dataset.workspaceId}, "已切换工作空间。");
    return;
  }
  const queueAction = event.target.closest("[data-queue-action]");
  if (queueAction) {
    mutate("/api/queue/action", {action: queueAction.dataset.queueAction}, "队列状态已更新。");
    return;
  }
  const taskAction = event.target.closest("[data-task-action]");
  if (taskAction) {
    mutate("/api/queue/action", {
      action: taskAction.dataset.taskAction,
      task_id: taskAction.dataset.taskId,
    }, "请求队列已更新。");
    return;
  }
  const runtimeAction = event.target.closest("[data-runtime-action]");
  if (runtimeAction) {
    mutate("/api/runtime/action", {action: runtimeAction.dataset.runtimeAction}, "运行状态已更新。");
  }
});

document.querySelector("#save-preferences").addEventListener("click", function () {
  mutate("/api/preferences", {
    response_mode: elements.responseMode.value,
    style: elements.visualStyle.value,
  }, "回复设置已保存。");
});

elements.toggleLock.addEventListener("click", function () {
  if (!state.snapshot) return;
  const locked = state.snapshot.preferences.remote_locked;
  if (locked) {
    const code = window.prompt("输入远程锁定码");
    if (code == null) return;
    mutate("/api/security/lock", {locked: false, code: code}, "已解除远程锁定。");
    return;
  }
  mutate("/api/security/lock", {locked: true}, "已启用远程锁定。");
});

const observer = new IntersectionObserver(function (entries) {
  const visible = entries.filter(function (entry) {
    return entry.isIntersecting;
  }).sort(function (a, b) {
    return b.intersectionRatio - a.intersectionRatio;
  })[0];
  if (!visible) return;
  document.querySelectorAll("[data-section-link]").forEach(function (link) {
    link.classList.toggle("active", link.dataset.sectionLink === visible.target.id);
  });
}, {rootMargin: "-15% 0px -65% 0px", threshold: [0, 0.2, 0.8]});
document.querySelectorAll(".page-section, .inspector-section[id]").forEach(function (section) {
  observer.observe(section);
});

refresh();
setInterval(function () {
  if (state.token && document.visibilityState === "visible") refresh();
}, 15000);
