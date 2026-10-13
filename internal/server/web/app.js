"use strict";

// ---------------------------------------------------------------- helpers

const $ = (sel, root = document) => root.querySelector(sel);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "X-BitMerger": "1", ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    cache: "no-store",
  });
  let data = null;
  try { data = await res.json(); } catch (e) { /* empty body */ }
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data;
}

// ---------------------------------------------------------------- state

const app = {
  server: null,        // /api/state
  view: null,          // chosen view once logged in
  plan: null,
  dryrun: null,        // steps of the last dry run
  selftest: null,      // results of the last selftest
  lastJobKind: null,
  lastJobRunning: false,
  localBusy: null,     // text while a direct request runs (login)
  error: null,
  login: { tab: "password", server: "us", url: "", email: "", method: "", twoStep: null },
  match: "host",
  filter: "all",
  reveal: {},          // id -> {state: "loading"|"shown", value, timer}
  save: { timer: null, state: "saved", error: null },
  ended: false,
};

const SERVERS = { us: "https://vault.bitwarden.com", eu: "https://vault.bitwarden.eu" };

function jobRunning() { return !!(app.server && app.server.job && app.server.job.running); }
function busy() { return jobRunning() || !!app.localBusy || !!(app.server && app.server.download && app.server.download.running); }

function currentView() {
  const s = app.server;
  if (!s) return "loading";
  if (!s.bwReady) return "setup";
  if (!s.account) return "login";
  if (jobRunning() && s.job.kind === "apply") return "merge";
  return app.view || (s.hasPlan ? "review" : "scan");
}

// ---------------------------------------------------------------- polling

async function poll() {
  if (app.ended) return;
  try {
    const s = await api("GET", "/api/state");
    const before = app.server;
    app.server = s;
    await onJobTransition(before, s);
  } catch (e) {
    app.error = t("error_generic", { e: e.message });
  }
  render();
  setTimeout(poll, busy() ? 900 : 4000);
}

async function onJobTransition(before, s) {
  const wasRunning = app.lastJobRunning;
  const job = s.job;
  app.lastJobRunning = !!(job && job.running);
  if (job) app.lastJobKind = job.kind;
  if (!wasRunning || !job || job.running) return;
  // a job just finished
  if (job.error) { app.error = t("job_error", { e: job.error }); }
  if (job.kind === "scan" && !job.error) {
    await loadPlan();
    app.view = "review";
  } else if (job.kind === "dryrun" && !job.error) {
    const full = await api("GET", "/api/job");
    app.dryrun = full.result || [];
    app.view = "dryrun";
  } else if (job.kind === "selftest") {
    const full = await api("GET", "/api/job");
    app.selftest = full.result || null;
  } else if (job.kind === "apply") {
    app.view = "merge";
  }
}

async function loadPlan() {
  try { app.plan = await api("GET", "/api/plan"); } catch (e) { app.plan = null; }
}

// ---------------------------------------------------------------- actions

async function run(fn) {
  app.error = null;
  try { await fn(); } catch (e) { app.error = t("error_generic", { e: e.message }); }
  render();
}

function startJob(path, body) {
  return run(async () => {
    await api("POST", path, body);
    app.lastJobRunning = true;
    app.server = await api("GET", "/api/state");
  });
}

// The field shows a fixed "https://"; a pasted full URL is accepted as well.
function serverURL(v) {
  v = v.trim().replace(/\/+$/, "");
  return /^https?:\/\//i.test(v) ? v : "https://" + v;
}

async function doLogin(form) {
  const l = app.login;
  const req = {};
  req.server = l.server === "self" ? serverURL(form.url.value) : SERVERS[l.server];
  if (l.tab === "apikey") {
    req.clientId = form.clientId.value.trim();
    req.clientSecret = form.clientSecret.value.trim();
  } else {
    req.email = form.email.value.trim();
    l.email = req.email;
    if (form.twoStepMethod && form.twoStepMethod.value) { req.method = form.twoStepMethod.value; req.code = form.code.value.trim(); }
  }
  req.password = form.password.value;
  app.localBusy = t("login_running");
  app.error = null;
  render();
  try {
    const res = await api("POST", "/api/login", req);
    if (res.twoStep) {
      l.twoStep = res.message || "";
    } else {
      l.twoStep = null;

      app.server = await api("GET", "/api/state");
      if (app.server.hasPlan) await loadPlan();
    }
  } catch (e) {
    app.error = e.message;
  } finally {
    app.localBusy = null;
    render();
  }
}

function scheduleSave() {
  app.save.state = "saving";
  app.save.error = null;
  renderSaveState();
  clearTimeout(app.save.timer);
  app.save.timer = setTimeout(async () => {
    try {
      await api("PUT", "/api/plan", app.plan);
      app.save.state = "saved";
    } catch (e) {
      app.save.state = "error";
      app.save.error = e.message;
    }
    renderSaveState();
  }, 500);
}

async function reveal(id, card) {
  const r = app.reveal[id];
  if (r && r.state === "loading") return;
  if (r && r.state === "shown") { clearTimeout(r.timer); delete app.reveal[id]; rerenderCard(card); return; }
  app.reveal[id] = { state: "loading" };
  rerenderCard(card);
  try {
    const res = await api("GET", "/api/password?id=" + encodeURIComponent(id));
    app.reveal[id] = { state: "shown", value: res.password, timer: setTimeout(() => { delete app.reveal[id]; rerenderCard(card); }, 30000) };
  } catch (e) {
    app.reveal[id] = { state: "shown", value: "⚠ " + e.message, timer: setTimeout(() => { delete app.reveal[id]; rerenderCard(card); }, 8000) };
  }
  rerenderCard(card);
}

async function quit() {
  const msg = jobRunning() ? t("quit_running") : t("quit_confirm");
  if (!confirm(msg)) return;
  try { await api("POST", "/api/quit"); } catch (e) { /* server is going away */ }
  app.ended = true;
  document.body.innerHTML = `<main><div class="panel narrow"><h2>BitMerger</h2><p>${esc(t("bye"))}</p></div></main>`;
}

// ---------------------------------------------------------------- render

function render() {
  if (app.ended) return;
  const s = app.server;
  const view = currentView();

  // header
  $("#lang").textContent = t("lang_switch");
  $("#quit").textContent = t("quit");
  const lo = $("#logout");
  lo.textContent = t("logout");
  lo.hidden = !(s && s.account);
  lo.disabled = busy();
  $("#account").textContent = s && s.account ? s.account.email : "";
  const order = ["setup", "login", "scan", "review", "merge"];
  const stepOf = { setup: "setup", login: "login", scan: "scan", review: "review", dryrun: "merge", merge: "merge", loading: "setup" };
  const cur = order.indexOf(stepOf[view]);
  $("#steps").innerHTML = order.map((k, i) => `<span class="${i === cur ? "on" : i < cur ? "done" : ""}">${i < cur ? "✓ " : ""}${esc(t("step_" + k))}</span>`).join("");
  $("#foot").textContent = s ? t("foot", { version: s.version, bw: s.bwVersion }) : "";

  // busy banner
  const b = $("#busy");
  const text = busyText();
  b.hidden = !text;
  $("#busyText").textContent = text || "";

  // keep focus and scroll when only the review is re-rendered by polling
  if (view === "review" && $("#main").dataset.view === "review" && app.plan) {
    renderReviewToolbar();
    return;
  }
  if ($("#main").dataset.view !== view) app.lastHTML = null;
  $("#main").dataset.view = view;
  const views = { loading: () => "", setup: viewSetup, login: viewLogin, scan: viewScan, review: viewReview, dryrun: viewDryRun, merge: viewMerge };
  const html = (app.error ? `<div class="msg err">${esc(app.error)}</div>` : "") + views[view]();
  if (html === app.lastHTML && $("#main").dataset.view === view) return;
  app.lastHTML = html;
  const kept = keepInputs();
  $("#main").innerHTML = html;
  restoreInputs(kept);
  bind(view);
}

function keepInputs() {
  const values = {};
  $("#main").querySelectorAll("input[name]:not([type=radio]):not([type=checkbox])").forEach((el) => { values[el.name] = el.value; });
  const a = document.activeElement;
  const focus = a && a.name && $("#main").contains(a) ? { name: a.name, start: a.selectionStart, end: a.selectionEnd } : null;
  return { values, focus };
}

function restoreInputs(kept) {
  for (const [name, value] of Object.entries(kept.values)) {
    const el = $("#main").querySelector(`input[name="${name}"]`);
    if (el && !el.value) el.value = value;
  }
  if (kept.focus) {
    const el = $("#main").querySelector(`[name="${kept.focus.name}"]`);
    if (el) {
      el.focus();
      try { if (kept.focus.start != null) el.setSelectionRange(kept.focus.start, kept.focus.end); } catch (e) { /* not a text field */ }
    }
  }
}

function busyText() {
  const s = app.server;
  if (app.localBusy) return app.localBusy;
  if (s && s.download && s.download.running) {
    const pct = s.download.total > 0 ? Math.round((100 * s.download.done) / s.download.total) : 0;
    return t("setup_running") + (pct ? ` ${pct} %` : "");
  }
  if (jobRunning()) {
    const j = s.job;
    if (j.kind === "apply" && j.phase === "merge") return t("merge_running", { d: j.done + 1 > j.total ? j.total : j.done + 1, n: j.total, cur: j.current || "" });
    return t("phase_" + (j.phase || "sync"));
  }
  return null;
}

function viewSetup() {
  const d = app.server.download;
  const running = d && d.running;
  const pct = d && d.total > 0 ? Math.round((100 * d.done) / d.total) : 0;
  return `<div class="panel narrow"><h2>${esc(t("setup_title"))}</h2><p>${esc(t("setup_text"))}</p>
    ${d && d.error ? `<div class="msg err">${esc(d.error)}</div>` : ""}
    ${running ? `<div class="progress"><div style="width:${pct}%"></div></div>` : ""}
    <div class="row" style="margin-top:14px"><button class="btn" id="setup" ${running ? "disabled" : ""}>${running ? '<span class="spinner"></span>' : ""}${esc(running ? t("setup_running") : t("setup_button"))}</button></div></div>`;
}

function viewLogin() {
  const l = app.login;
  const running = !!app.localBusy;
  const tab = (k) => `<button type="button" data-tab="${k}" class="${l.tab === k ? "on" : ""}">${esc(t("tab_" + k))}</button>`;
  return `<form class="panel narrow" id="loginForm" autocomplete="off"><h2>${esc(t("login_title"))}</h2><p>${esc(t("login_text"))}</p>
    <label class="field"><span>${esc(t("server"))}</span><select name="server">
      ${["us", "eu", "self"].map((k) => `<option value="${k}" ${l.server === k ? "selected" : ""}>${esc(t("server_" + k))}</option>`).join("")}</select></label>
    ${l.server === "self" ? `<label class="field"><span>${esc(t("server_url"))}</span><div class="prefixed"><span>https://</span><input type="text" name="url" placeholder="vault.example.com" required autocapitalize="off" spellcheck="false"></div></label>` : ""}
    <div class="tabs">${tab("password")}${tab("apikey")}</div>
    ${l.tab === "apikey" ? `<p class="muted">${esc(t("apikey_hint"))}</p>
      <label class="field"><span>${esc(t("client_id"))}</span><input type="text" name="clientId" required></label>
      <label class="field"><span>${esc(t("client_secret"))}</span><input type="password" name="clientSecret" required></label>`
      : `<label class="field"><span>${esc(t("email"))}</span><input type="email" name="email" required autocomplete="username"></label>`}
    <label class="field"><span>${esc(t("master_password"))}</span><input type="password" name="password" required autocomplete="current-password"></label>
    ${l.twoStep !== null && l.tab === "password" ? `<div class="msg info"><b>${esc(t("twostep_title"))}</b>${l.twoStep ? "<br>" + esc(l.twoStep) : ""}<br>${esc(t("twostep_email_hint"))}</div>
      <div class="row"><label class="field" style="flex:1"><span>${esc(t("twostep_method"))}</span><select name="twoStepMethod">
        ${["0", "1", "3"].map((m) => `<option value="${m}" ${l.method === m ? "selected" : ""}>${esc(t("method_" + m))}</option>`).join("")}</select></label>
      <label class="field" style="flex:1"><span>${esc(t("twostep_code"))}</span><input type="text" name="code" inputmode="numeric" autocomplete="one-time-code"></label></div>` : ""}
    <div class="row" style="margin-top:14px"><button class="btn" type="submit" ${running ? "disabled" : ""}>${running ? '<span class="spinner"></span>' : ""}${esc(running ? t("login_running") : t("login_button"))}</button></div></form>`;
}

function viewScan() {
  const running = busy();
  const choice = (k) => `<label class="choice ${app.match === k ? "on" : ""}"><input type="radio" name="match" value="${k}" ${app.match === k ? "checked" : ""} ${running ? "disabled" : ""}><div><b>${esc(t("match_" + k))}</b><small>${esc(t("match_" + k + "_hint"))}</small></div></label>`;
  const scanning = jobRunning() && app.server.job.kind === "scan";
  const testing = jobRunning() && app.server.job.kind === "selftest";
  return `<div class="panel"><h2>${esc(t("scan_title"))}</h2><p>${esc(t("scan_text"))}</p>
      ${choice("host")}${choice("domain")}
      <p class="muted">${esc(t("always_rule"))}</p>
      <div class="row" style="margin-top:14px"><button class="btn" id="scan" ${running ? "disabled" : ""}>${scanning ? '<span class="spinner"></span>' : ""}${esc(scanning ? t("scan_running") : t("scan_button"))}</button>
      ${app.plan ? `<button class="btn secondary" id="toReview" ${running ? "disabled" : ""}>${esc(t("back_review"))}</button>` : ""}</div></div>
    <div class="panel"><h2>${esc(t("selftest_title"))}</h2><p>${esc(t("selftest_text"))}</p>${selftestResult()}
      <div class="row" style="margin-top:12px"><button class="btn secondary" id="selftest" ${running ? "disabled" : ""}>${testing ? '<span class="spinner"></span>' : ""}${esc(testing ? t("phase_selftest") : t("selftest_button"))}</button></div></div>`;
}

function selftestResult() {
  const r = app.selftest;
  if (!r) return "";
  const allOK = r.every((c) => !c.error && c.checks.every((x) => x.ok));
  return `<div class="msg ${allOK ? "ok" : "err"}"><b>${esc(allOK ? t("selftest_passed") : t("selftest_failed"))}</b>${r.map((c) => `<div style="margin-top:6px">${esc(t("case_" + c.case))}</div>
    ${c.error ? `<div>${esc(c.error)}</div>` : `<ul class="checks">${c.checks.map((x) => `<li>${x.ok ? "✓" : "✗"} ${esc(t("check_" + x.check))}</li>`).join("")}</ul>`}`).join("")}</div>`;
}

// ---------------------------------------------------------------- review

function groupState(g) {
  const incl = g.entries.filter((e) => e.include);
  const orgs = new Set(incl.filter((e) => e.owner).map((e) => e.owner));
  const blocked = g.blocked.map((b) => t("block_" + b));
  if (orgs.size > 1) blocked.push(t("block_multi_org", { orgs: [...orgs].join(", ") }));
  const personal = incl.some((e) => !e.owner);
  const scope = orgs.size > 1 ? "multi_org" : personal && orgs.size ? "personal_org" : orgs.size ? "org" : "personal";
  return { incl, blocked, scope };
}

function viewReview() {
  if (!app.plan) return `<div class="panel"><p>…</p></div>`;
  return `<div class="panel"><h2>${esc(t("review_title"))}</h2><p>${esc(t("review_help"))}</p><p class="muted">${esc(t("pw_legend"))}</p></div>
    <div class="toolbar" id="toolbar"></div>
    <div id="groups">${app.plan.groups.length ? "" : `<div class="panel"><p>${esc(t("no_groups"))}</p></div>`}</div>`;
}

function renderReviewToolbar() {
  const tb = $("#toolbar");
  if (!tb || !app.plan) return;
  const act = app.plan.groups.filter((g) => g.active);
  const trash = act.reduce((s, g) => s + g.entries.filter((e) => e.include).length - 1, 0);
  const running = busy();
  const dry = jobRunning() && app.server.job.kind === "dryrun";
  const html = `${app.error ? `<div class="msg err" style="flex-basis:100%">${esc(app.error)}</div>` : ""}<label>${esc(t("show"))} <select id="filter">${["all", "active", "inactive"].map((k) => `<option value="${k}" ${app.filter === k ? "selected" : ""}>${esc(t("show_" + k))}</option>`).join("")}</select></label>
    <span class="count">${esc(t("count", { a: act.length, n: app.plan.groups.length, t: trash }))}</span>
    <span class="saved" id="saveState"></span><span class="spacer"></span>
    <button class="btn secondary" id="rescan" ${running ? "disabled" : ""}>${esc(t("rescan"))}</button>
    <button class="btn" id="dryrun" ${running || !act.length ? "disabled" : ""}>${dry ? '<span class="spinner"></span>' : ""}${esc(dry ? t("dryrun_running") : t("dryrun_button"))}</button>`;
  // Polling calls this every few seconds; replacing unchanged markup would close an open dropdown.
  if (tb.dataset.html === html) { renderSaveState(); return; }
  tb.dataset.html = html;
  tb.innerHTML = html;
  $("#filter").onchange = (e) => { app.filter = e.target.value; renderGroups(); };
  $("#rescan").onclick = () => { app.view = "scan"; render(); };
  $("#dryrun").onclick = () => { flushSave().then(() => startJob("/api/dryrun")); };
  renderSaveState();
}

function renderSaveState() {
  const el = $("#saveState");
  if (!el) return;
  const s = app.save;
  el.innerHTML = s.state === "saving" ? `<span class="spinner"></span>${esc(t("saving"))}`
    : s.state === "error" ? `<span class="err">${esc(t("save_failed", { e: s.error }))}</span>` : `✓ ${esc(t("saved"))}`;
}

async function flushSave() {
  if (app.save.state !== "saving") return;
  clearTimeout(app.save.timer);
  await api("PUT", "/api/plan", app.plan);
  app.save.state = "saved";
  renderSaveState();
}

function renderGroups() {
  const box = $("#groups");
  if (!box || !app.plan) return;
  box.innerHTML = "";
  for (const g of app.plan.groups) {
    if (app.filter === "active" && !g.active) continue;
    if (app.filter === "inactive" && g.active) continue;
    box.appendChild(cardFor(g));
  }
}

function cardFor(g) {
  const el = document.createElement("section");
  el.dataset.no = g.no;
  fillCard(el, g);
  return el;
}

function rerenderCard(el) {
  const g = app.plan.groups.find((x) => String(x.no) === el.dataset.no);
  if (g) fillCard(el, g);
}

function featureText(f) { return t("feat_" + f.kind, { n: f.count || 0 }); }

function fillCard(el, g) {
  const { blocked, scope } = groupState(g);
  if (blocked.length) g.active = false;
  el.className = "group" + (g.active ? "" : " inactive");
  const keeper = g.entries.find((e) => e.id === g.keeper) || {};
  const tags = [];
  if (g.reasons.includes("name")) tags.push(`<span class="tag warn">${esc(t("reason_name"))}</span>`);
  for (const n of g.notes) tags.push(`<span class="tag warn">${esc(t("note_" + n))}</span>`);
  for (const b of blocked) tags.push(`<span class="tag err">${esc(b)}</span>`);
  const rows = g.entries.map((e) => {
    const r = app.reveal[e.id];
    const revealBtn = e.pw === "-" ? "" : r && r.state === "loading"
      ? `<button class="link" disabled><span class="spinner" style="width:11px;height:11px"></span> ${esc(t("loading"))}</button>`
      : `<button class="link" data-reveal="${esc(e.id)}">${esc(r ? t("hide") : t("reveal"))}</button>`;
    return `<tr class="${e.include ? "" : "excluded"}">
      <td><input type="checkbox" data-include="${esc(e.id)}" ${e.include ? "checked" : ""}></td>
      <td><input type="radio" name="k${g.no}" data-keeper="${esc(e.id)}" ${g.keeper === e.id ? "checked" : ""} ${e.include ? "" : "disabled"}></td>
      <td><div class="pw"><label><input type="radio" name="p${g.no}" data-pwfrom="${esc(e.id)}" ${g.passwordFrom === e.id ? "checked" : ""} ${e.include && e.pw !== "-" ? "" : "disabled"}> ${esc(e.pw)}</label>${revealBtn}</div>
        ${r && r.state === "shown" ? `<code class="plain">${esc(r.value)}</code>` : ""}</td>
      <td>${esc(e.name)}</td><td>${esc(e.owner || t("personal"))}</td><td>${esc(e.collections.join(", ") || "-")}</td>
      <td>${esc(e.hosts.join(", ") || "-")}</td><td>${esc((e.features || []).map(featureText).join(", ") || "-")}</td><td>${esc((e.revision || "").slice(0, 10))}</td></tr>`;
  }).join("");
  el.innerHTML = `<div class="ghead"><label><input type="checkbox" data-active ${g.active ? "checked" : ""} ${blocked.length || busy() ? "disabled" : ""}> <b>${g.no}. ${esc(g.title)}</b></label>
      <span class="muted">${esc(t("scope_" + scope))}</span>${tags.join("")}</div>
    <div class="name"><span class="muted">${esc(t("name_after"))}</span><input type="text" data-name value="${esc(g.newName || "")}" placeholder="${esc(keeper.name || "")}"></div>
    <div class="scroll"><table><tr><th>${esc(t("col_with"))}</th><th>${esc(t("col_keep"))}</th><th>${esc(t("col_password"))}</th><th>${esc(t("col_name"))}</th><th>${esc(t("col_owner"))}</th><th>${esc(t("col_collections"))}</th><th>${esc(t("col_hosts"))}</th><th>${esc(t("col_extras"))}</th><th>${esc(t("col_changed"))}</th></tr>${rows}</table></div>`;

  const changed = (rerender = true) => { if (rerender) fillCard(el, g); renderReviewToolbar(); scheduleSave(); };
  el.querySelector("[data-active]").onchange = (ev) => { g.active = ev.target.checked; changed(); };
  el.querySelectorAll("[data-include]").forEach((x) => x.onchange = () => {
    const e = g.entries.find((e) => e.id === x.dataset.include);
    e.include = x.checked;
    const incl = g.entries.filter((e) => e.include);
    if (!incl.some((e) => e.id === g.keeper) && incl.length) g.keeper = incl[0].id;
    if (!incl.some((e) => e.id === g.passwordFrom)) { const p = incl.find((e) => e.pw !== "-"); if (p) g.passwordFrom = p.id; }
    if (incl.length < 2) g.active = false;
    changed();
  });
  el.querySelectorAll("[data-keeper]").forEach((x) => x.onchange = () => { g.keeper = x.dataset.keeper; changed(); });
  el.querySelectorAll("[data-pwfrom]").forEach((x) => x.onchange = () => { g.passwordFrom = x.dataset.pwfrom; changed(false); });
  el.querySelectorAll("[data-reveal]").forEach((x) => x.onclick = () => reveal(x.dataset.reveal, el));
  el.querySelector("[data-name]").oninput = (ev) => { g.newName = ev.target.value.trim(); changed(false); };
}

// ---------------------------------------------------------------- dry run and merge

function changeText(c, step) {
  if (c.kind.startsWith("history_dropped:")) return t("ch_history_dropped", { n: c.kind.split(":")[1] });
  return t("ch_" + c.kind, { n: c.n || 0, from: c.from || 0, v: step.newName || "" });
}

function stepHTML(s) {
  if (s.problem) return `<div class="step"><div class="t">${s.no}. ${esc(s.title)}</div><div class="err">${esc(t("skipped", { why: t("pr_" + s.problem, { names: (s.names || []).join(", ") }) }))}</div></div>`;
  const changes = [];
  if (s.newName) changes.push(t("ch_name", { v: s.newName }));
  for (const c of s.changes || []) changes.push(changeText(c, s));
  return `<div class="step"><div class="t">${s.no}. ${esc(s.title)}</div>
    <div>${esc(t("keeps", { name: s.keeperName, owner: s.keeperOwner || t("personal"), pw: s.pw || "-" }))}</div>
    ${changes.length ? `<div class="muted">+ ${esc(changes.join(" · "))}</div>` : ""}
    ${(s.trash || []).map((x) => `<div class="muted">🗑 ${esc(t("trash", { name: x.name, owner: x.owner || t("personal") }))}</div>`).join("")}
    ${s.warning ? `<div class="msg warn">${esc(t("warn_" + s.warning))}</div>` : ""}</div>`;
}

function dryCounts() {
  const steps = app.dryrun || [];
  const ok = steps.filter((s) => !s.problem);
  return { ok: ok.length, t: ok.reduce((n, s) => n + (s.trash || []).length, 0), s: steps.length - ok.length };
}

function viewDryRun() {
  const c = dryCounts();
  return `<div class="panel"><h2>${esc(t("dryrun_title"))}</h2><div class="msg info">${esc(t("dryrun_summary", c))}</div>
      <div class="msg warn">${esc(t("merge_warn"))}</div>
      <div class="row"><button class="btn secondary" id="toReview">${esc(t("back_review"))}</button>
      <button class="btn" id="merge" ${busy() || !c.ok ? "disabled" : ""}>${esc(t("merge_button"))}</button></div></div>
    <div class="panel">${(app.dryrun || []).map(stepHTML).join("")}</div>`;
}

function viewMerge() {
  const j = app.server.job;
  if (!j || j.kind !== "apply") return `<div class="panel"><p>…</p><button class="btn secondary" id="toReview">${esc(t("back_review"))}</button></div>`;
  const pct = j.total ? Math.round((100 * j.done) / j.total) : 0;
  const okN = j.log.filter((l) => l.ok).length;
  const errN = j.log.length - okN;
  let head;
  if (j.running) head = `<div class="msg warn">${esc(t("merge_warn"))}</div>`;
  else if (j.cancelled) head = `<div class="msg warn">${esc(t("merge_cancelled", { d: j.done, n: j.total }))}</div>`;
  else head = `<div class="msg ${errN ? "warn" : "ok"}">${esc(t("merge_done", { ok: okN, err: errN }))}</div>`;
  return `<div class="panel"><h2>${esc(t("merge_title"))}</h2>${head}
      ${j.error ? `<div class="msg err">${esc(t("job_error", { e: j.error }))}</div>` : ""}
      <div class="progress"><div style="width:${pct}%"></div></div>
      <div class="muted">${j.total ? j.done + " / " + j.total : ""}${j.running && j.current ? " · " + esc(j.current) : ""}</div>
      <div class="row" style="margin-top:12px">${j.running ? `<button class="btn danger" id="cancel">${esc(t("cancel"))}</button>` : `<button class="btn secondary" id="rescanAfter">${esc(t("rescan"))}</button>`}</div>
      <div class="log">${j.log.slice().reverse().map((l) => `<div class="${l.ok ? "ok" : "err"}">${l.ok ? "✓" : "✗"} ${l.no}. ${esc(l.title)}${l.error ? " — " + esc(l.error) : ""}</div>`).join("") || "<div class='muted'>…</div>"}</div></div>`;
}

// ---------------------------------------------------------------- binding

function bind(view) {
  const on = (sel, ev, fn) => { const el = $(sel); if (el) el[ev] = fn; };
  on("#setup", "onclick", () => run(async () => { await api("POST", "/api/setup"); app.server = await api("GET", "/api/state"); setTimeout(poll, 300); }));
  if (view === "login") {
    const f = $("#loginForm");
    f.server.onchange = () => { app.login.server = f.server.value; render(); };
    f.querySelectorAll("[data-tab]").forEach((b) => b.onclick = () => { app.login.tab = b.dataset.tab; app.login.twoStep = null; render(); });
    if (f.twoStepMethod) f.twoStepMethod.onchange = () => { app.login.method = f.twoStepMethod.value; };

    f.onsubmit = (e) => { e.preventDefault(); doLogin(f); };

  }
  document.querySelectorAll("input[name=match]").forEach((r) => r.onchange = () => { app.match = r.value; render(); });
  on("#scan", "onclick", () => startJob("/api/scan", { match: app.match }));
  on("#selftest", "onclick", () => { app.selftest = null; startJob("/api/selftest"); });
  on("#toReview", "onclick", async () => { if (!app.plan) await loadPlan(); app.view = "review"; render(); });
  on("#merge", "onclick", () => {
    const c = dryCounts();
    if (confirm(t("merge_confirm", c))) { app.view = "merge"; startJob("/api/apply"); }
  });
  on("#cancel", "onclick", (ev) => { ev.target.disabled = true; ev.target.textContent = t("cancel_requested"); api("POST", "/api/cancel").catch(() => {}); });
  on("#rescanAfter", "onclick", () => { app.view = "scan"; render(); });
  if (view === "review") { renderReviewToolbar(); renderGroups(); }
}

$("#lang").onclick = () => { setLang(LANG === "de" ? "en" : "de"); $("#main").dataset.view = ""; render(); };
$("#quit").onclick = quit;
$("#logout").onclick = () => run(async () => { await api("POST", "/api/logout"); app.plan = null; app.view = null; app.server = await api("GET", "/api/state"); $("#main").dataset.view = ""; });

setLang(LANG);
(async () => {
  try {
    app.server = await api("GET", "/api/state");
    if (app.server.hasPlan) await loadPlan();
    if (app.server.job) { app.lastJobRunning = app.server.job.running; app.lastJobKind = app.server.job.kind; }
  } catch (e) { app.error = e.message; }
  render();
  setTimeout(poll, 1000);
})();
