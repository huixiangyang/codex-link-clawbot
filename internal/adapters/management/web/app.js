"use strict";
const $ = (id) => document.getElementById(id);
const escapeHTML = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const icon = (name) => `<svg aria-hidden="true"><use href="#i-${name}"/></svg>`;
const stamp = (seconds) =>
  seconds
    ? new Date(seconds * 1000).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })
    : "—";
const size = (bytes) =>
  bytes >= 1048576
    ? `${(bytes / 1048576).toFixed(1)} MiB`
    : `${Math.max(0, Math.ceil(bytes / 1024))} KiB`;
const executionLabels = {
  unknown: "打断未确认",
  running: "正在执行",
  succeeded: "执行完成",
  completed: "执行完成",
  failed: "执行失败",
  interrupted: "执行中断",
  cancelled: "已取消",
};
const reasonLabels = {
  interrupt_unconfirmed: "打断尚未确认，请刷新会话或再次打断",
  codex_failed: "Codex 执行失败，可检查原始输入后决定是否重新执行。",
  restart_running: "服务在执行过程中重启，未自动重新执行。",
  user_cancelled: "用户取消了这条请求，已发生的修改没有自动撤销。",
  removed_pending: "旧等待指令已取消，未执行。需要继续时请重新发送。",
  project_unavailable: "原工作空间不可用，请在本机恢复对应配置或目录。",
  session_busy: "会话在准备期间开始了其他执行，本条指令未提交，请管理会话后重新发送。",
  session_unavailable:
    "原会话不可用或已归档。请先检查会话；系统没有改用其他会话。",
  payload_invalid: "原始输入不完整或校验失败，无法安全继续执行。",
  result_freeze_failed:
    "执行已完成，请选择恢复保存成果，不需要重新执行。",
};
const deliveryLabels = {
  awaiting_archive: "等待恢复保存",
  not_started: "尚未投递",
  pending: "正在投递",
  succeeded: "微信已送达",
  explicit_failure: "微信投递失败",
  ambiguous: "微信送达未确认",
  unavailable: "结果已过期或不可用",
};
const state = {
  token: sessionStorage.getItem("codex-link-token") || "",
  snapshot: null,
  route: "work",
  view: "all",
  page: 1,
  pages: 1,
  threadPage: 1,
  threadPages: 1,
  selected: "",
  detail: null,
  dirty: false,
  busy: false,
  requests: [],
  conversations: [],
  generations: {},
  poll: false,
};
const setHTML = (id, html) => {
  const el = $(id);
  if (el.innerHTML !== html) el.innerHTML = html;
};
function notice(message, success = false) {
  $("notice").textContent = message;
  $("notice").classList.toggle("success", success);
  $("notice").hidden = !message;
}
function login() {
  if (!$("login-dialog").open) $("login-dialog").showModal();
}
async function api(path, method = "GET", body, binary = false) {
  let response;
  try {
    response = await fetch(path, {
      method,
      headers: {
        "X-Codex-Link-Token": state.token,
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: "no-store",
      signal: AbortSignal.timeout(85000),
    });
  } catch (error) {
    throw new Error(
      "工作台连接中断或响应超时。操作可能已被接收，请刷新状态后重试。",
    );
  }
  if (response.status === 401) {
    login();
    const err = new Error("管理令牌无效，请重新连接。");
    err.status = 401;
    throw err;
  }
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    const err = new Error(data.error || `操作失败（${response.status}）`);
    err.status = response.status;
    err.session = data.session;
    throw err;
  }
  return binary ? response.blob() : response.json();
}
// 每种读取保留自己的序号，慢响应不能覆盖新的搜索、翻页或请求选择。
async function latest(key, task, apply) {
  const gen = (state.generations[key] || 0) + 1;
  state.generations[key] = gen;
  const result = await task();
  if (gen === state.generations[key]) apply(result);
}
function options(items, selected) {
  return items
    .map(
      (i) =>
        `<option value="${escapeHTML(i.id)}" ${i.id === selected ? "selected" : ""}>${escapeHTML(i.name)}</option>`,
    )
    .join("");
}
function renderSnapshot(data) {
  state.snapshot = data;
  const { runtime, target, execution } = data;
  $("target-title").textContent = target.title;
  $("target-status").textContent = target.available
    ? target.thread_id
      ? `会话 ${target.thread_id} · ${data.session.stage}`
      : "首条请求建立会话，之后的消息持续同一对话"
    : "目标暂不可用，请在会话页重新选择";
  if (document.activeElement !== $("workspace-select"))
    setHTML("workspace-select", options(data.workspaces, target.workspace_id));
  $("connection-state").textContent = runtime.draining
    ? "实例正在排空"
    : runtime.codex.ready && runtime.wechat.healthy > 0
      ? "微信与 Codex 已连接"
      : "部分连接待恢复";
  $("connection-state").classList.toggle(
    "degraded",
    runtime.draining || !runtime.codex.ready || runtime.wechat.healthy < 1,
  );
  $("version").textContent = runtime.version;
  $("toggle-runtime").textContent = runtime.draining ? "恢复实例" : "排空实例";
  $("toggle-lock").textContent = data.locked ? "解锁微信入口" : "锁定微信入口";
  $("toggle-lock").disabled = !data.capabilities.remote_lock || state.busy;
  if (!state.dirty) {
    setHTML(
      "response-mode",
      options(data.modes, data.preferences.response_mode),
    );
    setHTML("visual-style", options(data.styles, data.preferences.style));
    modeDescription();
  }
  if (data.capacity) {
    const c = data.capacity;
    $("capacity-status").textContent = `输入 ${size(c.input_bytes)} / ${size(c.input_limit)} · 结果（含预留）${size(c.result_bytes)} / ${size(c.result_limit)} · 记录 ${c.records} / ${c.record_limit}。${c.next_expiry ? `最近到期：${stamp(c.next_expiry)}。` : ""}在请求详情中可清理这条记录。`;
  }
  const rows = [
    ["Codex", runtime.codex.ready ? "已就绪" : "未就绪"],
    ["微信", runtime.wechat.healthy > 0 ? "连接正常" : "连接待恢复"],
    ["当前绑定", data.binding.owner],
    ["入口状态", data.locked ? "已锁定" : "开放"],
    ["执行中", `${execution.running} 个会话`],
    ["当前目标", target.available ? "可用" : "需要重新选择"],
    ["阅读卡", data.capabilities.reading ? "已启用" : "未配置"],
    ["语音", data.capabilities.voice ? "已启用" : "未配置"],
  ];
  setHTML(
    "connection-detail",
    rows
      .map(
        ([label, value]) =>
          `<div><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value)}</dd></div>`,
      )
      .join(""),
  );
  $("last-refresh").textContent =
    `同步于 ${new Date().toLocaleTimeString("zh-CN", { hour12: false })}`;
}
function modeDescription() {
  $("mode-description").textContent =
    state.snapshot?.modes.find((i) => i.id === $("response-mode").value)
      ?.description || "";
}
async function loadSnapshot() {
  await latest("snapshot", () => api("/api/snapshot"), renderSnapshot);
}
function pager(id, page, pages, total) {
  setHTML(
    id,
    `<button data-page="${page - 1}" ${page <= 1 ? "disabled" : ""}>上一页</button><span>${page} / ${pages} · ${total} 条</span><button data-page="${page + 1}" ${page >= pages ? "disabled" : ""}>下一页</button>`,
  );
}
function warning(item) {
  return (
    item.archive_failed || ["failed", "interrupted", "unknown"].includes(item.execution) ||
    ["explicit_failure", "ambiguous"].includes(item.delivery)
  );
}
function workspaceName(id) {
  return state.snapshot?.workspaces.find((w) => w.id === id)?.name || id;
}
function renderRequests(data) {
  state.requests = data.items;
  state.page = data.page;
  state.pages = data.pages;
  $("request-total").textContent = String(data.counts.all);
  document.querySelectorAll("[data-view]").forEach((b) => {
    b.classList.toggle("selected", b.dataset.view === state.view);
    b.setAttribute("aria-pressed", String(b.dataset.view === state.view));
    b.querySelector("span").textContent = data.counts[b.dataset.view] || 0;
  });
  setHTML(
    "request-list",
    data.items.length
      ? data.items
          .map(
            (item) =>
              `<button class="request-row ${state.selected === item.id ? "active" : ""}" data-request="${escapeHTML(item.id)}" aria-pressed="${state.selected === item.id}"><div class="row-meta"><span class="${warning(item) ? "status-warning" : ""}"><i class="status-dot"></i>${escapeHTML(executionLabels[item.execution] || item.execution)}</span><time>${stamp(item.created_at)}</time></div><h3>${escapeHTML(item.summary)}</h3><div class="row-context"><span>${escapeHTML(workspaceName(item.workspace_id))}</span><span>· ${escapeHTML(item.blocked || deliveryLabels[item.delivery] || "")}</span></div></button>`,
          )
          .join("")
      : `<div class="empty">${$("request-search").value ? "没有匹配的请求。换个关键词试试。" : state.view === "attention" ? "目前没有需要处理的请求。" : state.view === "active" ? "当前没有执行中的会话。" : "还没有请求。向微信机器人发送消息，第一段工作会从这里开始。"}</div>`,
  );
  pager("request-pagination", data.page, data.pages, data.total);
}
async function loadRequests() {
  const query = new URLSearchParams({
    view: state.view,
    page: state.page,
    q: $("request-search").value,
  });
  await latest("requests", () => api(`/api/requests?${query}`), renderRequests);
}
function renderDetail(item) {
  state.detail = item;
  const result = item.result;
  let actions = "";
  if (item.can_cancel)
    actions +=
      '<button class="secondary danger" data-action="cancel">取消这条请求</button>';
  if (item.can_retry)
    actions += '<button class="primary" data-action="retry">重新执行</button>';
  if (result?.reply)
    actions += '<button class="quiet" data-copy>复制回答</button>';
  if (item.can_redeliver)
    actions +=
      '<button class="secondary" data-action="redeliver">重新投递到微信</button>';
  if (item.can_restore) actions += '<button class="primary" data-action="restore">恢复保存结果</button>';
  if (item.can_release) actions += '<button class="quiet danger" data-action="release">清理这条记录</button>';
  const input = item.input
    ? `${escapeHTML(item.input.text)}${item.input.attachments.length ? `\n附件：${item.input.attachments.map(escapeHTML).join("、")}` : ""}`
    : escapeHTML(item.input_unavailable);
  const receipts = result?.attempts?.length
    ? result.attempts
        .map(
          (r) =>
            `<div class="receipt-row">${stamp(r.attempted_at)} · ${escapeHTML(deliveryLabels[r.outcome] || r.outcome)}${r.failure_code ? ` · ${escapeHTML(r.failure_code)}` : ""}</div>`,
        )
        .join("")
    : "尚无投递记录";
  const html = `<div class="detail-head"><button class="mobile-back" data-close-detail>${icon("arrow-left")}返回列表</button><code>${escapeHTML(item.id)}</code></div><h2>${escapeHTML(item.summary)}</h2><div class="badges"><span class="badge ${["failed", "interrupted", "unknown"].includes(item.execution) ? "warn" : ""}">${escapeHTML(executionLabels[item.execution] || item.execution)}</span><span class="badge ${["ambiguous", "explicit_failure"].includes(item.delivery) ? "warn" : ""}">${escapeHTML(deliveryLabels[item.delivery] || item.delivery)}</span></div><div class="detail-actions">${actions}</div><div class="detail-meta">${escapeHTML(workspaceName(item.workspace_id))} / ${escapeHTML(item.thread_id || "等待建立会话")}<br>提交于 ${stamp(item.created_at)}${item.started_at ? `<br>开始于 ${stamp(item.started_at)}` : ""}${item.finished_at ? `<br>结束于 ${stamp(item.finished_at)}` : ""}${item.retry_of ? `<br>重新执行自 ${escapeHTML(item.retry_of)}` : ""}</div>${item.blocked ? `<p class="notice">${escapeHTML(item.blocked)}</p>` : ""}${item.reason ? `<p class="muted">停止原因：${escapeHTML(reasonLabels[item.reason] || "请求已停止，请检查本机运行日志。")}</p>` : ""}<span class="detail-label">原始请求</span><div class="original-input">${input}</div><span class="detail-label">${result ? "回答与文件" : "当前进展"}</span>${result ? `<div class="answer">${escapeHTML(result.reply || "本次结果以文件形式交付。")}</div>${result.artifacts.map((file, index) => `<button class="artifact" data-download="${index}"><span>${escapeHTML(file.name)}<small>${size(file.size)}</small></span>${icon("download")}</button>`).join("")}${(result.image_urls || []).map((url, index) => `<p class="muted"><a href="${escapeHTML(url)}" target="_blank" rel="noopener noreferrer">外部图片 ${index + 1}（可用期由源站决定） ${icon("arrow-up-right")}</a></p>`).join("")}<p class="muted">结果可取回至 ${stamp(item.result_expires_at)}</p>` : `<p class="muted">${item.archive_failed ? "执行已完成，成果尚未保存完整。请恢复保存，不需要重新执行。" : item.execution === "succeeded" ? "结果已过期或不可用，执行记录仍保留。" : item.can_retry ? "执行未完成。原始输入仍在保留期内，可以创建一条新请求重新执行。" : escapeHTML(item.stage || "等待处理")}</p>`}${item.can_retry ? '<p class="muted">重新执行可能重复此前已经完成的文件修改等操作。</p>' : ""}${result ? `<details><summary>投递记录 · ${(result.attempts || []).length} 次</summary>${receipts}</details>` : ""}<div class="retention-note">${item.input_expires_at ? `原始输入保留至 ${stamp(item.input_expires_at)} · ` : ""}请求记录保留 30 天</div>`;
  const opened = $("request-detail").querySelector("details")?.open;
  setHTML("request-detail", html);
  if (opened && $("request-detail").querySelector("details"))
    $("request-detail").querySelector("details").open = true;
}
async function loadDetail() {
  if (!state.selected) return;
  const id = state.selected;
  await latest(
    "detail",
    () => api(`/api/requests/${encodeURIComponent(id)}`),
    (data) => {
      if (state.selected === id) renderDetail(data);
    },
  );
}
async function selectRequest(id) {
  state.selected = id;
  document.querySelector(".workbench").classList.add("detail-open");
  document.querySelectorAll("[data-request]").forEach((el) => {
    el.classList.toggle("active", el.dataset.request === id);
    el.setAttribute("aria-pressed", String(el.dataset.request === id));
  });
  $("request-detail").innerHTML = '<p class="empty">正在读取结果…</p>';
  await loadDetail();
}
function renderThreads(data) {
  state.conversations = data.items;
  state.threadPage = data.page;
  state.threadPages = data.pages;
  setHTML(
    "thread-list",
    data.items.length
      ? data.items
          .map(
            (item, index) =>
              `<article class="thread-item"><span class="thread-index">${String((data.page - 1) * 12 + index + 1).padStart(2, "0")}</span><div><h3>${escapeHTML(item.title)}${item.current ? '<span class="thread-current">当前目标</span>' : ""}</h3><p class="muted">${escapeHTML(item.workspace_name)} · ${stamp(item.updated_at)}<br>${escapeHTML(item.id)}</p></div><div class="thread-actions"><button class="${item.current ? "quiet" : "secondary"}" data-thread-action="select" data-id="${escapeHTML(item.id)}" ${item.current ? "disabled" : ""}>${item.current ? "正在使用" : "继续此会话"}</button><button class="secondary" data-thread-action="manage" data-id="${escapeHTML(item.id)}">${item.activity.busy ? "正在执行 · 管理" : "管理会话"}</button><button class="quiet" data-thread-action="rename" data-id="${escapeHTML(item.id)}">改名</button><button class="quiet danger" data-thread-action="archive" data-id="${escapeHTML(item.id)}">归档</button></div></article>`,
          )
          .join("")
      : '<p class="empty">没有找到会话。可以新建会话，或向微信机器人发送第一条消息。</p>',
  );
  pager("thread-pagination", data.page, Math.max(1, data.pages), data.total);
}
async function loadThreads() {
  const query = new URLSearchParams({
    page: state.threadPage,
    q: $("thread-search").value,
  });
  await latest(
    "threads",
    () => api(`/api/conversations?${query}`),
    renderThreads,
  );
}
async function refresh() {
  await loadSnapshot();
  if (state.route === "work") await Promise.all([loadRequests(), loadDetail()]);
  if (state.route === "threads") await loadThreads();
}
function report(error) {
  if (error.status !== 401) notice(error.message);
  if (error.session) openSession(error.session,error.message);
}
function run(task) {
  task().catch(report);
}
async function mutation(task, message) {
  if (state.busy) return;
  state.busy = true;
  const enabled = [...document.querySelectorAll("button:not(:disabled)")];
  enabled.forEach((b) => (b.disabled = true));
  try {
    await task();
    notice(message, true);
    await refresh();
  } catch (error) {
    report(error);
    await refresh().catch(() => {});
  } finally {
    state.busy = false;
    enabled.forEach((b) => {
      if (b.isConnected) b.disabled = false;
    });
    if (state.snapshot) renderSnapshot(state.snapshot);
  }
}
let actionHandler = null;
function dialog(
  {
    title,
    description,
    reference = "CONVERSATION",
    input = false,
    password = false,
    value = "",
    workspace = false,
    submit = "确认",
  },
  handler,
) {
  $("action-title").textContent = title;
  $("action-description").textContent = description;
  $("action-reference").textContent = reference;
  $("action-input-label").hidden = !input;
  $("action-input").type = password ? "password" : "text";
  $("action-input").value = value;
  $("action-input").required = password;
  $("action-input").maxLength = password ? 256 : 80;
  $("action-input-title").textContent = password ? "解锁码" : "会话名称";
  $("action-workspace-label").hidden = !workspace;
  setHTML(
    "action-workspace",
    options(state.snapshot.workspaces, state.snapshot.target.workspace_id),
  );
  $("action-submit").textContent = submit;
  actionHandler = handler;
  $("action-dialog").showModal();
}
$("action-cancel").onclick = () => {
  $("action-dialog").close();
  actionHandler = null;
};
$("action-form").onsubmit = (event) => {
  event.preventDefault();
  const handler = actionHandler;
  if (!handler) return;
  const data = {
    name: $("action-input").value,
    workspace: $("action-workspace").value,
  };
  $("action-dialog").close();
  $("action-input").value = "";
  actionHandler = null;
  run(() => handler(data));
};
function requestAction(action) {
  const item = state.detail;
  if (!item || state.busy) return;
  const labels = {
    restore: ["恢复保存成果？", "只取回这次执行的回答和文件，不会再次执行项目操作。", "恢复保存"],
    release: ["清理这条记录与内容？", "删除保存的输入、回答和文件，无法恢复。同时释放记录名额，保留来源回执防止重复执行。", "确认清理"],
    cancel: [
      "取消这条请求？",
      "将中断此请求的执行。已经写入工作空间的文件修改不会自动撤销。",
      "确认取消",
    ],
    retry: [
      "重新执行这条请求？",
      "会创建一条新请求，沿用原工作空间、会话和输入。此前执行可能已经产生部分修改，重新执行可能重复这些操作。",
      "创建重跑请求",
    ],
    redeliver: [
      "重新投递已有结果？",
      "将把已保存的回答和文件再次发送到微信。不会重新执行 Codex；如果上次已送达，微信中可能出现重复消息。",
      "确认重新投递",
    ],
  };
  const [title, description, submit] = labels[action] || [];
  if (!title) return;
  dialog({ title, description, submit, reference: item.id }, () =>
    mutation(
      async () => {
        const key = `codex-link-op:${item.id}:${action}`;
        // 网络响应丢失时保留同一操作编号，重复点击不会创建第二条请求或再次发送。
        const operationID = sessionStorage.getItem(key) || crypto.randomUUID();
        sessionStorage.setItem(key, operationID);
        try {
          const result = await api(
            `/api/requests/${encodeURIComponent(item.id)}/actions`,
            "POST",
            { action, operation_id: operationID },
          );
          sessionStorage.removeItem(key);
          if (action === "release") {
            state.selected = "";
            state.detail = null;
            document.querySelector(".workbench").classList.remove("detail-open");
            $("request-detail").innerHTML = '<p class="muted">记录已清理，请选择其他请求。</p>';
          }
          if (result.id) await selectRequest(result.id);
        } catch (error) {
          if (error.status && error.status < 500)
            sessionStorage.removeItem(key);
          throw error;
        }
      },
      action === "retry" ? "已开始重新执行。" : "请求操作已完成。",
    ),
  );
}
$("request-detail").onclick = (event) => {
  if (event.target.closest("[data-copy]")) {
    run(async () => {
      await navigator.clipboard.writeText(state.detail.result.reply);
      notice("回答已复制。", true);
    });
    return;
  }
  if (event.target.closest("[data-close-detail]")) {
    document.querySelector(".workbench").classList.remove("detail-open");
    return;
  }
  const action = event.target.closest("[data-action]");
  if (action) {
    requestAction(action.dataset.action);
    return;
  }
  const download = event.target.closest("[data-download]");
  if (download && !state.busy) {
    const file =
      state.detail.result.artifacts[Number(download.dataset.download)];
    run(async () => {
      download.disabled = true;
      try {
        const blob = await api(file.url, "GET", undefined, true);
        const url = URL.createObjectURL(blob);
        const link = document.createElement("a");
        link.href = url;
        link.download = file.name;
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 10000);
      } finally {
        download.disabled = false;
      }
    });
  }
};
$("request-list").onclick = (event) => {
  const row = event.target.closest("[data-request]");
  if (row) run(() => selectRequest(row.dataset.request));
};
$("request-filters").onclick = (event) => {
  const button = event.target.closest("[data-view]");
  if (!button) return;
  state.view = button.dataset.view;
  state.page = 1;
  run(loadRequests);
};
function pageHandler(id, field, load) {
  $(id).onclick = (event) => {
    const button = event.target.closest("[data-page]");
    if (!button) return;
    state[field] = Number(button.dataset.page);
    run(load);
  };
}
pageHandler("request-pagination", "page", loadRequests);
pageHandler("thread-pagination", "threadPage", loadThreads);
function searchHandler(id, field, load) {
  let timer;
  $(id).oninput = () => {
    clearTimeout(timer);
    state[field] = 1;
    timer = setTimeout(() => run(load), 220);
  };
}
searchHandler("request-search", "page", loadRequests);
searchHandler("thread-search", "threadPage", loadThreads);
$("new-thread").onclick = () => {
  if (!state.snapshot) return;
  dialog(
    {
      title: "开启一段新对话。",
      description:
        "创建后设为下一条微信消息的目标。正在执行的会话会继续工作，可单独打断。",
      input: true,
      workspace: true,
      submit: "创建并设为目标",
    },
    (values) =>
      mutation(
        () =>
          api("/api/conversations", "POST", {
            workspace_id: values.workspace,
            name: values.name,
          }),
        "新会话已创建，并设为当前目标。",
      ),
  );
};
$("workspace-select").onchange = (event) => {
  const workspace = event.target.value;
  run(() =>
    mutation(
      () => api("/api/target", "PUT", { workspace_id: workspace }),
      "之后的微信消息将进入所选工作空间。",
    ),
  );
};
$("thread-list").onclick = (event) => {
  const button = event.target.closest("[data-thread-action]");
  if (!button) return;
  const item = state.conversations.find((i) => i.id === button.dataset.id);
  if (!item) return;
  const action = button.dataset.threadAction;
  if (action === "manage") { openSession(item.activity); return; }
  if (action === "select") {
    run(() =>
      mutation(
        () =>
          api("/api/target", "PUT", {
            workspace_id: item.workspace_id,
            thread_id: item.id,
          }),
        "当前会话已切换。",
      ),
    );
    return;
  }
  dialog(
    {
      title: action === "rename" ? "给这段对话一个名字。" : "归档这段对话？",
      description:
        action === "rename"
          ? "使用简短、容易辨识的名称。"
          : "归档后不再显示在会话列表中。已有请求和结果仍按保留期可查；如果它是当前目标，下一条消息会创建新会话。",
      reference: item.id,
      input: action === "rename",
      value: action === "rename" ? item.title : "",
      submit: action === "rename" ? "保存名称" : "归档会话",
    },
    (values) =>
      mutation(
        () =>
          api(
            `/api/conversations/${encodeURIComponent(item.id)}/actions`,
            "POST",
            { action, name: values.name },
          ),
        action === "rename" ? "会话名称已保存。" : "会话已归档。",
      ),
  );
};
$("preferences-form").oninput = () => {
  state.dirty = true;
  $("preferences-dirty").hidden = false;
  modeDescription();
};
$("preferences-form").onsubmit = (event) => {
  event.preventDefault();
  const body = {
    response_mode: $("response-mode").value,
    style: $("visual-style").value,
  };
  run(() =>
    mutation(async () => {
      await api("/api/preferences", "PUT", body);
      state.dirty = false;
      $("preferences-dirty").hidden = true;
    }, "回复偏好已保存，之后提交的请求将采用新设置。"),
  );
};
$("toggle-runtime").onclick = () => {
  const draining = state.snapshot.runtime.draining;
  dialog(
    {
      title: draining ? "恢复实例？" : "排空实例？",
      description: draining
        ? "重新接收微信消息，即时开始新工作。"
        : "暂停接收和启动新请求，当前请求继续完成。适用于准备维护或升级。",
      reference: "RUNTIME",
      submit: draining ? "恢复实例" : "开始排空",
    },
    () =>
      mutation(
        () =>
          api("/api/runtime", "POST", {
            action: draining ? "resume" : "drain",
          }),
        "实例运行状态已更新。",
      ),
  );
};
$("toggle-lock").onclick = () => {
  const locked = state.snapshot.locked;
  dialog(
    {
      title: locked ? "解锁微信入口？" : "锁定微信入口？",
      description: locked
        ? "输入本机配置的解锁码。解锁后可继续通过微信提交与恢复请求。"
        : "停止接受微信工作请求，当前执行继续。网页仍可查看和下载结果。",
      reference: "REMOTE ACCESS",
      input: locked,
      password: locked,
      submit: locked ? "解锁" : "锁定",
    },
    (values) =>
      mutation(
        () => api("/api/lock", "POST", { locked: !locked, code: values.name }),
        "微信入口状态已更新。",
      ),
  );
};
$("login-dialog").addEventListener("cancel", (event) => event.preventDefault());
$("login-form").onsubmit = (event) => {
  event.preventDefault();
  state.token = $("token-input").value.trim();
  $("login-error").textContent = "";
  run(async () => {
    try {
      await loadSnapshot();
      sessionStorage.setItem("codex-link-token", state.token);
      $("token-input").value = "";
      $("login-dialog").close();
      notice("");
      await refresh();
    } catch (error) {
      $("login-error").textContent = error.message;
    }
  });
};
$("logout").onclick = () => {
  sessionStorage.removeItem("codex-link-token");
  location.reload();
};
$("refresh").onclick = () =>
  run(async () => {
    await refresh();
    notice("已同步最新状态。", true);
  });
function route() {
  const hash = location.hash.slice(1);
  state.route = ["work", "threads", "settings"].includes(hash) ? hash : "work";
  document
    .querySelectorAll(".page")
    .forEach((el) => (el.hidden = el.id !== state.route));
  document.querySelectorAll("[data-nav]").forEach((el) => {
    el.classList.toggle("active", el.dataset.nav === state.route);
    if (el.dataset.nav === state.route) el.setAttribute("aria-current", "page");
    else el.removeAttribute("aria-current");
  });
  $("page-title").textContent = {
    work: "把请求，变成结果。",
    threads: "让思考，接着发生。",
    settings: "远程工作，如你所愿。",
  }[state.route];
  if (state.token) run(refresh);
}
window.addEventListener("hashchange", route);
route();
if (!state.token) login();
setInterval(() => {
  if (
    !state.token ||
    document.hidden ||
    state.busy ||
    state.poll ||
    $("login-dialog").open ||
    $("action-dialog").open ||
    $("session-dialog").open
  )
    return;
  state.poll = true;
  refresh()
    .catch(report)
    .finally(() => (state.poll = false));
}, 6000);

// 操作菜单固定观察到的请求/轮次 ID；刷新才更新目标，旧确认不会打断新一轮。
let managedSession = null;
function openSession(session, explanation = "同一会话同时只接收一条工作指令。") {
  managedSession = { ...session };
  $("session-title").textContent = explanation.startsWith("提交失败") ? "提交失败" : "管理会话";
  $("session-explanation").textContent = explanation;
  $("session-status").textContent = `${session.stage} · ${session.thread_id ? session.thread_id.slice(-8) : "新对话"}`;
  $("session-interrupt").hidden = !session.can_interrupt;
  if (!$("session-dialog").open) $("session-dialog").showModal();
}
$("manage-session").onclick = () => { if (state.snapshot) openSession(state.snapshot.session); };
$("session-close").onclick = () => $("session-dialog").close();
$("session-switch").onclick = () => { $("session-dialog").close(); location.hash = "threads"; };
$("session-new").onclick = () => { $("session-dialog").close(); $("new-thread").click(); };
$("session-refresh").onclick = () => run(async () => {
  const current = managedSession;
  const session = current.thread_id
    ? await api(`/api/conversations/${encodeURIComponent(current.thread_id)}`)
    : (await api("/api/snapshot")).session;
  if (managedSession === current) openSession(session);
});
$("session-interrupt").onclick = () => {
  const session = { ...managedSession };
  $("session-dialog").close();
  dialog({title:"打断本次执行？",description:"已产生的修改会保留。结束后请重新发送新指令。",reference:session.thread_id || session.task_id,submit:"确认打断"}, () => mutation(async () => {
    if (session.task_id) await api(`/api/requests/${encodeURIComponent(session.task_id)}/actions`,"POST",{action:"cancel"});
    else await api(`/api/conversations/${encodeURIComponent(session.thread_id)}/actions`,"POST",{action:"interrupt",turn_id:session.turn_id});
  }, "已发出打断请求，请刷新状态。"));
};
