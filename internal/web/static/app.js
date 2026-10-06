"use strict";

const token = document.querySelector('meta[name="wq-token"]').content;
const $ = (id) => document.getElementById(id);

let state = null;
let config = null;
let rules = [];

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "X-WQ-Token": token, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `エラー (${res.status})`);
  return data;
}

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v === true) node.setAttribute(k, "");
    else if (v !== false && v != null) node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c != null) node.append(c);
  }
  return node;
}

let messageTimer;
function notify(text, isError = false) {
  const box = $("message");
  box.textContent = text;
  box.className = isError ? "error" : "";
  box.hidden = false;
  clearTimeout(messageTimer);
  messageTimer = setTimeout(() => { box.hidden = true; }, isError ? 10000 : 4000);
}

async function run(action, done) {
  try {
    await action();
    if (done) notify(done);
  } catch (e) {
    notify(e.message, true);
  }
}

function duration(ms) {
  if (ms <= 0) return "0分";
  const minutes = Math.ceil(ms / 60000);
  const d = Math.floor(minutes / 1440), h = Math.floor((minutes % 1440) / 60), m = minutes % 60;
  if (d > 0) return `${d}日${h}時間`;
  if (h > 0) return `${h}時間${m}分`;
  return `${m}分`;
}

function clock(iso) {
  return new Date(iso).toLocaleString("ja-JP", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

const statusLabels = {
  fresh: ["新鮮", "ok"],
  stale: ["古い", "warn"],
  reset_estimated: ["リセット済み（推定）", "warn"],
};

function renderWindows() {
  const failed = new Set(state.providers.filter((p) => p.error).map((p) => p.id));
  const disabled = new Set(state.providers.filter((p) => !p.enabled).map((p) => p.id));
  const now = new Date(state.now).getTime();
  const cards = state.windows.filter((w) => !disabled.has(w.provider)).map((w) => {
    let [label, tone] = statusLabels[w.status] || [w.status, ""];
    if (failed.has(w.provider)) [label, tone] = ["取得失敗", "bad"];
    const remaining = Math.round(w.remaining);
    const level = remaining <= 10 ? "critical" : remaining <= 20 ? "low" : "";
    const ended = w.status === "reset_estimated";
    const rows = [
      el("dt", {}, ended ? "リセット時刻" : "リセットまで"),
      el("dd", {}, !w.resets_at ? "不明" : ended ? `${clock(w.resets_at)}（経過）`
        : `${duration(new Date(w.resets_at).getTime() - now)}（${clock(w.resets_at)}）`),
      el("dt", {}, "観測"),
      el("dd", {}, `${duration(now - new Date(w.observed_at).getTime())}前`),
    ];
    if (w.sent_levels.length) {
      rows.push(el("dt", {}, "通知済み"), el("dd", {}, w.sent_levels.map((l) => `${l}%`).join("・")));
    }
    if (w.reset_scheduled_for) {
      rows.push(el("dt", {}, "リセット通知"), el("dd", {}, `${clock(w.reset_scheduled_for)} に予約済み`));
    }
    // Set through the style object: the page's CSP forbids style attributes.
    const bar = el("div");
    bar.style.width = `${remaining}%`;
    return el("div", { class: "card" },
      el("h3", {}, `${w.provider_name} ${w.window}`, el("span", { class: `badge ${tone}` }, label)),
      el("div", { class: "value" }, `${remaining}%`, el("small", {}, w.status === "reset_estimated" ? " 残り（推定）" : " 残り")),
      el("div", { class: `gauge ${level}` }, bar),
      el("dl", {}, rows));
  });
  $("windows").replaceChildren(...cards);
  $("no-windows").hidden = cards.length > 0;
  $("sent-today").textContent = `今日の送信数: ${state.sent_today} / ${state.daily_limit}`;
}

function renderProviders() {
  const cards = state.providers.map((p) => {
    let label = "未取得", tone = "";
    if (!p.enabled) label = "無効";
    else if (p.error) [label, tone] = ["取得失敗", "bad"];
    else if (p.has_data) [label, tone] = ["取得中", "ok"];
    const toggle = el("input", { type: "checkbox", checked: p.enabled });
    toggle.addEventListener("change", () => run(async () => {
      await api("PUT", "/api/config", { providers: { [p.id]: { enabled: toggle.checked } } });
      await refresh();
    }, `${p.name} を${toggle.checked ? "有効" : "無効"}にしました`));
    return el("div", { class: "card" },
      el("h3", {}, p.name, el("span", { class: `badge ${tone}` }, label)),
      el("label", {}, toggle, " 収集する"),
      p.error ? el("p", { class: "warn" }, p.error) : null,
      el("p", { class: "hint" }, p.checked_at ? `最終確認: ${clock(p.checked_at)}` : "まだ確認していません"));
  });
  $("provider-list").replaceChildren(...cards);

  const s = state.statusline;
  let text;
  if (s.error) text = `設定ファイルを読めません: ${s.error}`;
  else if (s.installed && s.wrapped) text = `登録済み。元のステータスライン（${s.wrapped}）の表示を引き継いでいます。`;
  else if (s.installed) text = "登録済み。";
  else if (s.current) text = `未登録。現在のステータスライン: ${s.current}`;
  else text = "未登録。ステータスラインは設定されていません。";
  $("statusline-state").replaceChildren(text, el("br"), el("span", { class: "hint" }, `設定ファイル: ${s.settings_path}`));
  $("statusline-install").disabled = s.installed;
  $("statusline-uninstall").disabled = !s.installed;
}

const kindLabels = {
  threshold: "しきい値", reset: "リセット", reset_scheduled: "リセット予約", reset_cancel: "予約取り消し", test: "テスト",
};

function renderHistory() {
  const rows = state.history.map((h) => el("tr", {},
    el("td", { class: "time" }, clock(h.time)),
    el("td", {}, kindLabels[h.kind] || h.kind),
    el("td", {}, [h.title, h.body].filter(Boolean).join(" ／ ")),
    el("td", {}, h.ok ? "送信済み" : `失敗: ${h.error || ""}`)));
  $("history-rows").replaceChildren(...rows);
  $("no-history").hidden = rows.length > 0;
}

function renderDestinationState() {
  $("subscribe-url").textContent = state.subscribe_url;
  $("topic-weak").hidden = !state.topic_weak;
  $("token-state").textContent = state.token_in_file
    ? "トークンは設定ファイルに保存されています（OS の資格情報ストアには未対応です）。"
    : "トークンは設定されていません。";
  $("clear-token").hidden = !state.token_in_file;
}

function fillDestination() {
  const form = $("destination-form");
  form.server.value = config.ntfy.server;
  form.topic.value = config.ntfy.topic;
  form.token.value = "";
}

function knownTargets() {
  const keys = new Set(state ? state.windows.map((w) => w.key) : []);
  for (const r of rules) r.targets.forEach((t) => keys.add(t));
  return [...keys].sort();
}

function renderRules() {
  const targets = knownTargets();
  const nodes = rules.map((rule, index) => {
    const field = (label, input) => el("label", {}, label, input);
    const bind = (input, apply) => { input.addEventListener("input", () => apply(input)); return input; };

    const enabled = bind(el("input", { type: "checkbox", checked: rule.enabled }), (i) => { rule.enabled = i.checked; });
    const title = bind(el("input", { value: rule.title }), (i) => { rule.title = i.value; });
    const body = bind(el("input", { value: rule.body }), (i) => { rule.body = i.value; });
    const priority = el("select", {}, [1, 2, 3, 4, 5].map((p) => el("option", { value: p, selected: p === rule.priority }, `${p}${p === 3 ? "（標準）" : ""}`)));
    priority.addEventListener("change", () => { rule.priority = Number(priority.value); });

    const row = [field("優先度", priority)];
    if (rule.kind === "threshold") {
      const levels = bind(el("input", { value: rule.levels.join(", "), inputmode: "numeric" }), (i) => {
        rule.levels = i.value.split(/[,、\s]+/).filter(Boolean).map(Number);
      });
      row.unshift(field("残量のしきい値（%）", levels));
    }

    const all = el("input", { type: "checkbox", checked: rule.targets.length === 0 });
    const boxes = targets.map((key) => {
      const box = el("input", { type: "checkbox", checked: rule.targets.includes(key), disabled: rule.targets.length === 0 });
      box.addEventListener("change", () => {
        rule.targets = box.checked ? [...rule.targets, key] : rule.targets.filter((t) => t !== key);
      });
      return el("label", {}, box, ` ${key}`);
    });
    all.addEventListener("change", () => {
      rule.targets = all.checked ? [] : targets.slice();
      renderRules();
    });

    return el("div", { class: "rule" },
      el("div", { class: "head" },
        el("label", {}, enabled, ` ${rule.kind === "threshold" ? "しきい値通知" : "リセット通知"}`),
        el("span", {},
          el("button", { onclick: () => run(() => api("POST", "/api/test", { rule_id: rule.id }).then(refresh), "テスト通知を送りました（保存済みの内容で送信）") }, "テスト送信"),
          " ",
          el("button", { onclick: () => { rules.splice(index, 1); renderRules(); } }, "削除"))),
      el("div", { class: "row" }, row),
      field("タイトル", title),
      field("本文", body),
      el("label", {}, "対象"),
      el("div", { class: "targets" }, el("label", {}, all, " すべて"), boxes));
  });
  $("rule-list").replaceChildren(...nodes);
}

function addRule(kind) {
  const base = kind === "threshold"
    ? { levels: [20, 10, 5], priority: 4, title: "{provider} {window} 残り{remaining}%", body: "リセットまで {resets_in}" }
    : { priority: 3, title: "{provider} {window} リセット", body: "利用枠が回復しました" };
  rules.push({ id: `${kind}-${Date.now().toString(36)}`, kind, enabled: true, targets: [], ...base });
  renderRules();
}

async function loadConfig() {
  config = await api("GET", "/api/config");
  rules = structuredClone(config.rules);
  fillDestination();
  renderRules();
}

async function refresh() {
  state = await api("GET", "/api/state");
  renderWindows();
  renderProviders();
  renderHistory();
  renderDestinationState();
}

function showTab(tab) {
  if (!document.querySelector(`#tabs [data-tab="${CSS.escape(tab)}"]`)) return;
  for (const b of $("tabs").children) b.classList.toggle("active", b.dataset.tab === tab);
  for (const s of document.querySelectorAll("main > section")) s.hidden = s.id !== tab;
  if (tab === "rules" && config) renderRules();
}

// The tab is kept in the URL fragment so a reload stays on the same screen.
$("tabs").addEventListener("click", (e) => {
  if (e.target.dataset.tab) location.hash = e.target.dataset.tab;
});
window.addEventListener("hashchange", () => showTab(location.hash.slice(1)));

$("add-threshold").addEventListener("click", () => addRule("threshold"));
$("add-reset").addEventListener("click", () => addRule("reset"));
$("save-rules").addEventListener("click", () => run(async () => {
  config = await api("PUT", "/api/config", { rules });
  rules = structuredClone(config.rules);
  renderRules();
}, "ルールを保存しました"));

$("destination-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const form = e.target;
  const ntfy = { server: form.server.value.trim(), topic: form.topic.value.trim() };
  if (form.token.value) ntfy.token = form.token.value;
  run(async () => {
    config = await api("PUT", "/api/config", { ntfy });
    fillDestination();
    await refresh();
  }, "通知先を保存しました");
});

$("regenerate").addEventListener("click", () => {
  if (!confirm("トピック名を再生成します。スマートフォン側の購読もやり直しになります。よろしいですか？")) return;
  run(async () => {
    config = await api("POST", "/api/topic/regenerate");
    fillDestination();
    await refresh();
  }, "トピック名を再生成しました");
});

$("clear-token").addEventListener("click", () => run(async () => {
  config = await api("PUT", "/api/config", { ntfy: { server: config.ntfy.server, topic: config.ntfy.topic, token: "" } });
  await refresh();
}, "トークンを削除しました"));

$("test-send").addEventListener("click", () => run(() => api("POST", "/api/test", {}).then(refresh), "テスト通知を送りました（保存済みの通知先へ送信）"));

$("statusline-install").addEventListener("click", () => {
  if (!confirm("Claude Code の設定ファイルを書き換えて、ステータスラインに wrist-quota を登録します。変更前の内容は退避し、解除すると元に戻ります。よろしいですか？")) return;
  run(() => api("POST", "/api/statusline", { action: "install" }).then(refresh), "ステータスラインに登録しました");
});

$("statusline-uninstall").addEventListener("click", () => run(() => api("POST", "/api/statusline", { action: "uninstall" }).then(refresh), "登録を解除しました"));

run(async () => {
  await refresh();
  await loadConfig();
  if (location.hash) showTab(location.hash.slice(1));
});
setInterval(() => refresh().catch(() => {}), 5000);
