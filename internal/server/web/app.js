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
  login: { tab: "password", server: "us", url: "", email: "", clientId: "", method: "", twoStep: null, prefilled: false },
  match: "host",
  skip: null,          // folder IDs to leave out once the user changed them; null = server's choice
  filter: "all",
  reveal: {},          // id -> {state: "loading"|"shown", value, timer}
  details: {},         // id -> {open, state: "loading"|"ok"|"error", data, error}
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
    // The login request can still be running when polling already sees the account.
    if (s.hasPlan && !app.plan) await loadPlan();
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
  if (app.plan && app.plan.match) app.match = app.plan.match;
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

async function toggleDetails(id, card) {
  const d = app.details[id];
  if (d && d.open) { d.open = false; rerenderCard(card); return; }
  if (d && d.state === "ok") { d.open = true; rerenderCard(card); return; }
  app.details[id] = { open: true, state: "loading" };
  rerenderCard(card);
  try {
    app.details[id] = { open: true, state: "ok", data: await api("GET", "/api/details?id=" + encodeURIComponent(id)) };
  } catch (e) {
    app.details[id] = { open: true, state: "error", error: e.message };
  }
  rerenderCard(card);
}

function fmtDate(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

const MATCH = { 0: "um_domain", 1: "um_host", 2: "um_starts", 3: "um_exact", 4: "um_regex", 5: "um_never" };

function detailsHTML(d) {
  if (d.state === "loading") return `<span class="muted"><span class="spinner" style="width:11px;height:11px"></span> ${esc(t("loading"))}</span>`;
  if (d.state === "error") return `<span class="err">${esc(d.error)}</span>`;
  const x = d.data;
  const row = (k, v) => v ? `<div class="dk">${esc(t(k))}</div><div class="dv">${v}</div>` : "";
  const flags = [x.totp && "TOTP", x.passkey && "Passkey", x.favorite && t("feat_favorite"), x.reprompt && t("reprompt")].filter(Boolean);
  return `<div class="details">
    ${row("d_username", esc(x.username || "-"))}
    ${row("d_uris", x.uris.length ? x.uris.map((u) => `<div>${esc(u.uri)} <span class="muted">· ${esc(t(MATCH[u.match] || "um_default"))}</span></div>`).join("") : "-")}
    ${row("d_created", x.created ? esc(fmtDate(x.created)) : "")}
    ${row("d_revised", x.revised ? esc(fmtDate(x.revised)) : "")}
    ${row("d_pw_revised", x.passwordRevised ? esc(fmtDate(x.passwordRevised)) : "")}
    ${row("d_history", x.history.length ? esc(x.history.map(fmtDate).join(" · ")) : "")}
    ${row("d_fields", x.fields.length ? x.fields.map((f) => `<div>${esc(f.name)}: ${f.hidden ? `<span class="muted">${esc(t("hidden"))}</span>` : esc(f.value)}</div>`).join("") : "")}
    ${row("d_attachments", x.attachments.length ? x.attachments.map((a) => `<div>${esc(a.name)} <span class="muted">${esc(a.size || "")}</span></div>`).join("") : "")}
    ${row("d_flags", flags.length ? esc(flags.join(" · ")) : "")}
    ${row("d_notes", x.notes ? `<div class="note">${esc(x.notes)}</div>` : "")}
  </div>`;
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
  // Setup (downloading the CLI) is a step only while it is needed.
  const order = (s && s.bwReady ? [] : ["setup"]).concat(["login", "scan", "review", "merge"]);
  const stepOf = { setup: "setup", login: "login", scan: "scan", review: "review", dryrun: "merge", merge: "merge", loading: "setup" };
  const cur = order.indexOf(stepOf[view]);
  // "Suchen" and "Prüfen" can be opened directly once logged in.
  const canOpen = (k) => s && s.account && !busy() && view !== k && (k === "scan" || (k === "review" && app.plan));
  const stepsHTML = order.map((k, i) => {
    const cls = i === cur ? "on" : i < cur ? "done" : "";
    const label = `${i < cur ? "✓ " : ""}${esc(t("step_" + k))}`;
    return canOpen(k) ? `<button type="button" class="${cls}" data-step="${k}">${label}</button>` : `<span class="${cls}">${label}</span>`;
  }).join("");
  if ($("#steps").dataset.html !== stepsHTML) { $("#steps").dataset.html = stepsHTML; $("#steps").innerHTML = stepsHTML; }
  $("#foot").textContent = s ? t("foot", { version: s.version, bw: s.bwVersion }) : "";

  // busy banner
  const b = $("#busy");
  const text = busyText();
  b.hidden = !text;
  $("#busyText").textContent = text || "";

  // keep focus and scroll when only the review is re-rendered by polling
  if (view === "review" && $("#main").dataset.view === "review" && app.plan && $("#toolbar")) {
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

// Fill the form with the last successful login (server, email or client id; never secrets).
function takeLastLogin() {
  const l = app.login, last = app.server && app.server.lastLogin;
  if (l.prefilled || !last) return;
  l.prefilled = true;
  const known = Object.entries(SERVERS).find(([, url]) => url === last.server);
  l.server = known ? known[0] : "self";
  l.url = known ? "" : last.server.replace(/^https:\/\//i, "");
  l.tab = last.clientId ? "apikey" : "password";
  l.email = last.email || "";
  l.clientId = last.clientId || "";
}

function viewLogin() {
  takeLastLogin();
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
      ${viewFolders(running)}
      <p class="muted">${esc(t("always_rule"))}</p>
      <div class="row" style="margin-top:14px">${app.plan
        ? `<button class="btn" id="applySettings" ${running ? "disabled" : ""}>${scanning ? '<span class="spinner"></span>' : ""}${esc(scanning ? t("scan_running") : t("apply_settings"))}</button>
           <button class="btn secondary" id="scan" ${running ? "disabled" : ""}>${esc(t("reread"))}</button>`
        : `<button class="btn" id="scan" ${running ? "disabled" : ""}>${scanning ? '<span class="spinner"></span>' : ""}${esc(scanning ? t("scan_running") : t("scan_button"))}</button>`}</div>
      ${app.plan ? `<p class="muted" style="margin:10px 0 0">${esc(t("scan_again_hint"))}</p>` : ""}</div>
    <div class="panel"><h2>${esc(t("selftest_title"))}</h2><p>${esc(t("selftest_text"))}</p>${selftestResult()}
      <div class="row" style="margin-top:12px"><button class="btn secondary" id="selftest" ${running ? "disabled" : ""}>${testing ? '<span class="spinner"></span>' : ""}${esc(testing ? t("phase_selftest") : t("selftest_button"))}</button></div></div>`;
}

function skippedFolders() {
  const folders = (app.server && app.server.folders) || [];
  return app.skip || new Set(folders.filter((f) => f.skip).map((f) => f.id));
}

function viewFolders(running) {
  const folders = (app.server && app.server.folders) || [];
  if (!folders.length) return "";
  const skip = skippedFolders();
  const names = new Set(folders.map((f) => f.name));
  const left = folders.filter((f) => skip.has(f.id));
  const summary = left.length ? t("folders_searched", { list: left.map((f) => f.name).join(", ") }) : t("folders_none");
  // nested folders ("a/b") are shown below their parent, indented
  const row = (f) => {
    const parts = f.name.split("/");
    const parent = parts.slice(0, -1).join("/");
    const depth = parent && names.has(parent) ? parts.length - 1 : 0;
    const label = depth ? parts[parts.length - 1] : f.name;
    return `<label class="folder-row ${skip.has(f.id) ? "off" : ""}" style="padding-left:${12 + depth * 22}px"><input type="checkbox" name="folder" value="${esc(f.id)}" ${skip.has(f.id) ? "" : "checked"} ${running ? "disabled" : ""}>
      <span class="fname">${esc(label)}</span>${f.trash && label.toLowerCase() !== t("folder_trash").toLowerCase() ? `<span class="tag">${esc(t("folder_trash"))}</span>` : ""}<span class="muted">${f.count}</span></label>`;
  };
  return `<details class="folders" id="folders" ${app.foldersOpen ? "open" : ""}><summary><b>${esc(t("folders_title"))}</b> <span class="muted">${esc(summary)}</span></summary>
    <small class="muted">${esc(t("folders_hint"))}</small>
    <div class="folder-list">${folders.map(row).join("")}</div></details>`;
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
  const left = ((app.server && app.server.folders) || []).filter((f) => f.skip).map((f) => f.name);
  const folderLine = left.length ? t("folders_searched", { list: left.join(", ") }) : t("folders_none");
  const help = (k) => `<li><b>${esc(t("help_" + k))}:</b> ${esc(t("help_" + k + "_text"))}</li>`;
  return `<div class="panel intro"><h2>${esc(t("review_title"))}</h2><p>${esc(t("review_help"))}</p>
    <ul>${help("include")}${help("keep")}${help("password")}</ul>
    <p>${esc(t("help_saved"))} ${esc(folderLine)} <button class="link" id="changeFolders" type="button">${esc(t("folders_change"))}</button></p></div>
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
    <span class="count">${esc(t("count", { a: act.length, n: app.plan.groups.length }))} · ${esc(t("count_del", { n: trash }))}</span>
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

const EYE = '<svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><circle cx="12" cy="12" r="3" fill="none" stroke="currentColor" stroke-width="2"/></svg>';
const EYE_OFF = '<svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><circle cx="12" cy="12" r="3" fill="none" stroke="currentColor" stroke-width="2"/><path d="M4 20 20 4" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>';

// Password labels are letters; plans saved by older versions still say P1, P2.
function pwLetter(pw) {
  const m = /^P(\d+)$/.exec(pw || "");
  if (!m) return pw;
  let n = Number(m[1]) - 1, s = "";
  while (n >= 0) { s = String.fromCharCode(65 + (n % 26)) + s; n = Math.floor(n / 26) - 1; }
  return s;
}

function pwBadge(e) {
  const l = pwLetter(e.pw);
  const idx = [...l].reduce((n, c) => n * 26 + c.charCodeAt(0) - 64, 0) - 1;
  const tip = e.pwChanged ? ` title="${esc(t("pw_changed", { d: fmtDate(e.pwChanged) }))}"` : "";
  return `<span class="pwtag c${idx % 6}"${tip}>${esc(t("pw_name", { l }))}</span>`;
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
  // columns that are empty for every entry of this card are left out
  const show = {
    folder: g.entries.some((e) => e.folder),
    collections: g.entries.some((e) => e.collections.length),
    extras: g.entries.some((e) => (e.features || []).length),
  };
  const cols = 8 + Object.values(show).filter(Boolean).length;
  const rows = g.entries.map((e) => {
    const r = app.reveal[e.id];
    const d = app.details[e.id];
    const revealBtn = e.pw === "-" ? "" : r && r.state === "loading"
      ? `<button class="icon" disabled aria-label="${esc(t("loading"))}"><span class="spinner" style="width:12px;height:12px;margin:0"></span></button>`
      : `<button class="icon" data-reveal="${esc(e.id)}" title="${esc(r ? t("hide") : t("reveal"))}" aria-label="${esc(r ? t("hide") : t("reveal"))}">${r ? EYE_OFF : EYE}</button>`;
    return `<tr class="entry${e.include ? "" : " excluded"}${d && d.open ? " open" : ""}" data-row="${esc(e.id)}" title="${esc(t("row_tip"))}">
      <td><input type="checkbox" data-include="${esc(e.id)}" ${e.include ? "checked" : ""}></td>
      <td><input type="radio" name="k${g.no}" data-keeper="${esc(e.id)}" ${g.keeper === e.id ? "checked" : ""} ${e.include ? "" : "disabled"}></td>
      <td class="ename">${esc(e.name)}</td>
      <td>${esc(e.username || "-")}</td>
      <td><div class="pw"><label><input type="radio" name="p${g.no}" data-pwfrom="${esc(e.id)}" ${g.passwordFrom === e.id ? "checked" : ""} ${e.include && e.pw !== "-" ? "" : "disabled"}> ${e.pw === "-" ? "-" : r && r.state === "shown" ? `<code class="plain inline" title="${esc(r.value)}">${esc(r.value)}</code>` : pwBadge(e)}</label>${revealBtn}</div></td>
      <td>${esc(e.owner || t("personal"))}</td>${show.folder ? `<td>${esc(e.folder || "-")}</td>` : ""}${show.collections ? `<td>${esc(e.collections.join(", ") || "-")}</td>` : ""}
      <td class="hosts">${esc(e.hosts.join(", ") || "-")}</td>${show.extras ? `<td class="extras">${esc((e.features || []).map(featureText).join(", ") || "-")}</td>` : ""}<td class="nowrap">${esc(fmtDate(e.revision))}</td></tr>
      ${d && d.open ? `<tr class="detail"><td></td><td colspan="${cols - 1}">${detailsHTML(d)}</td></tr>` : ""}`;
  }).join("");
  el.innerHTML = `<div class="ghead"><label><input type="checkbox" data-active ${g.active ? "checked" : ""} ${blocked.length || busy() ? "disabled" : ""}> <b>${g.no}. ${esc(g.title)}</b></label>
      <span class="muted">${esc(t("scope_" + scope))}</span>${tags.join("")}</div>
    <div class="name"><span class="muted">${esc(t("name_after"))}</span><input type="text" data-name value="${esc(g.newName || "")}" placeholder="${esc(keeper.name || "")}"></div>
    <div class="scroll"><table><tr><th title="${esc(t("tip_with"))}">${esc(t("col_with"))}</th><th title="${esc(t("tip_keep"))}">${esc(t("col_keep"))}</th><th>${esc(t("col_name"))}</th><th>${esc(t("col_user"))}</th><th title="${esc(t("tip_password"))}">${esc(t("col_password"))}</th><th>${esc(t("col_owner"))}</th>${show.folder ? `<th>${esc(t("col_folder"))}</th>` : ""}${show.collections ? `<th>${esc(t("col_collections"))}</th>` : ""}<th>${esc(t("col_hosts"))}</th>${show.extras ? `<th>${esc(t("col_extras"))}</th>` : ""}<th>${esc(t("col_changed"))}</th></tr>${rows}</table></div>`;

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
  // the whole row opens the details; its controls keep their own meaning
  el.querySelectorAll("tr[data-row]").forEach((tr) => tr.onclick = (ev) => {
    if (ev.target.closest("input, button, label, a, code")) return;
    toggleDetails(tr.dataset.row, el);
  });
  el.querySelector("[data-name]").oninput = (ev) => { g.newName = ev.target.value.trim(); changed(false); };
}

// ---------------------------------------------------------------- dry run and merge

function changeText(c, step) {
  if (c.kind.startsWith("history_dropped:")) return t("ch_history_dropped", { n: c.kind.split(":")[1] });
  return t("ch_" + c.kind, { n: c.n || 0, from: c.from || 0, v: step.newName || "" });
}

// where an entry lives, e.g. "Personal · folder Backup · last edited 2026-01-21 18:00"
function whereText(owner, folder, revised) {
  const parts = [owner || t("personal")];
  if (folder) parts.push(t("in_folder", { folder }));
  if (revised) parts.push(t("edited", { d: fmtDate(revised) }));
  return parts.join(" · ");
}

function stepHTML(s) {
  const title = `<div class="t">${esc(s.title)}</div>`;
  if (s.problem) return `<div class="step">${title}<div class="err">${esc(t("skipped", { why: t("pr_" + s.problem, { names: (s.names || []).join(", ") }) }))}</div></div>`;
  const changes = [];
  if (s.newName) changes.push(t("ch_name", { v: s.newName }));
  for (const c of s.changes || []) changes.push(changeText(c, s));
  const keep = [s.keeperOwner || t("personal")];
  if (s.keeperFolder) keep.push(t("in_folder", { folder: s.keeperFolder }));
  if (s.pw && s.pw !== "-") keep.push(t("pw_label", { pw: pwLetter(s.pw) }));
  return `<div class="step">${title}
    <div>${esc(t("keeps", { name: s.keeperName }))} <span class="muted">${esc(keep.join(" · "))}</span></div>
    ${changes.length ? `<div>${esc(t("takes", { list: changes.join(", ") }))}</div>` : ""}
    ${(s.trash || []).map((x) => `<div>${esc(t("trash", { name: x.name }))} <span class="muted">${esc(whereText(x.owner, x.folder, x.revised))}</span></div>`).join("")}
    ${s.warning ? `<div class="msg warn">${esc(t("warn_" + s.warning))}</div>` : ""}</div>`;
}

function dryCounts() {
  const steps = app.dryrun || [];
  const ok = steps.filter((s) => !s.problem);
  return { ok: ok.length, t: ok.reduce((n, s) => n + (s.trash || []).length, 0), s: steps.length - ok.length };
}

// Rough duration: every CLI call takes one to two seconds; a group needs one
// edit per old password, the final edit, a check and one delete per duplicate.
function mergeDuration() {
  let calls = 4;
  for (const s of (app.dryrun || []).filter((x) => !x.problem)) {
    const hist = (s.changes || []).find((c) => c.kind === "history");
    calls += 3 + (hist ? hist.n : 0) + (s.trash || []).length;
  }
  const min = Math.round((calls * 1.5) / 60);
  return min < 1 ? t("dur_lt_minute") : t("dur_minutes", { n: min });
}

function summaryText(c) { return t("dry_merge", { n: c.ok }) + ", " + t("dry_trash", { n: c.t }); }

function viewDryRun() {
  const c = dryCounts();
  return `<div class="panel"><h2>${esc(t("dryrun_title"))}</h2>
      <div class="msg info">${esc(summaryText(c))}${c.s ? " " + esc(t("dryrun_skipped", { n: c.s })) : ""}</div>
      ${c.ok ? `<div class="msg warn">${esc(t("merge_warn", { d: mergeDuration() }))}<br>${esc(t("merge_window"))}</div>` : ""}
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
    // remembered values go in once; after that the typed values are kept by restoreInputs
    const fill = { url: app.login.url, email: app.login.email, clientId: app.login.clientId };
    for (const [name, value] of Object.entries(fill)) {
      if (f[name] && !f[name].value && value && !app.login.filled?.[name]) { f[name].value = value; (app.login.filled ||= {})[name] = true; }
    }
    if (app.login.prefilled && !document.activeElement?.closest?.("#loginForm") && f.password) f.password.focus();

    f.onsubmit = (e) => { e.preventDefault(); doLogin(f); };

  }
  document.querySelectorAll("input[name=match]").forEach((r) => r.onchange = () => { app.match = r.value; render(); });
  on("#folders", "ontoggle", (e) => { app.foldersOpen = e.target.open; });
  document.querySelectorAll("input[name=folder]").forEach((c) => c.onchange = () => {
    const skip = new Set(skippedFolders());
    if (c.checked) skip.delete(c.value); else skip.add(c.value);
    app.skip = skip;
    render();
  });
  on("#scan", "onclick", () => flushSave().then(() => startJob("/api/scan", { match: app.match, skipFolders: [...skippedFolders()] })));
  on("#applySettings", "onclick", () => flushSave().then(() => startJob("/api/scan", { match: app.match, skipFolders: [...skippedFolders()], reuse: true })));
  on("#selftest", "onclick", () => { app.selftest = null; startJob("/api/selftest"); });
  on("#toReview", "onclick", async () => { if (!app.plan) await loadPlan(); app.view = "review"; render(); });
  on("#merge", "onclick", () => {
    const c = dryCounts();
    if (confirm(t("merge_confirm", { m: t("dry_merge", { n: c.ok }), d: t("dry_trash", { n: c.t }) }))) { app.view = "merge"; startJob("/api/apply"); }
  });
  on("#cancel", "onclick", (ev) => { ev.target.disabled = true; ev.target.textContent = t("cancel_requested"); api("POST", "/api/cancel").catch(() => {}); });
  on("#rescanAfter", "onclick", () => { app.view = "scan"; render(); });
  if (view === "review") {
    renderReviewToolbar();
    renderGroups();
    on("#changeFolders", "onclick", () => flushSave().then(() => { app.view = "scan"; app.foldersOpen = true; render(); const f = $(".folders"); if (f) f.scrollIntoView({ block: "center" }); }));
  }
}

$("#lang").onclick = () => { setLang(LANG === "de" ? "en" : "de"); $("#main").dataset.view = ""; render(); };
$("#quit").onclick = quit;
$("#steps").onclick = (e) => {
  const b = e.target.closest("button[data-step]");
  if (!b) return;
  flushSave().then(() => { app.view = b.dataset.step; render(); });
};
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
