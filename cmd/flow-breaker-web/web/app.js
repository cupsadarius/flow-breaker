// flow-breaker phone client. Today groups come from the daemon's /status and
// habits from /habits; this file only renders them and sends actions.
"use strict";

const $ = (id) => document.getElementById(id);
let lastSync = null;

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (method !== "GET") {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify({ source: "phone", ...body });
  }
  const res = await fetch("/api/" + path, opts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function toast(msg) {
  const t = $("toast");
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), 3500);
}

function ago(seconds) {
  const m = Math.round(Math.abs(seconds) / 60);
  const s = m >= 60 ? `${Math.floor(m / 60)}h${String(m % 60).padStart(2, "0")}` : `${m}m`;
  return seconds < 0 ? `${s} ago` : `in ${s}`;
}

function taskRow(t, kind, status) {
  const b = el("button", "row " + kind);
  b.type = "button";
  b.append(el("span", "t", t.time), el("span", "d", t.desc), el("span", "s", status));
  b.addEventListener("click", () => openSheet(t));
  return b;
}

function renderToday(st) {
  const box = $("today");
  box.replaceChildren();
  const overdue = st.overdue || [];
  const upcoming = st.upcoming || [];
  const done = st.done || [];
  const groups = [
    ["overdue", "Overdue", overdue],
    ["next", "Next", upcoming.slice(0, 1)],
    ["later", "Later", upcoming.slice(1)],
  ];
  for (const [kind, label, list] of groups) {
    if (!list.length) continue;
    box.append(el("h3", kind, label));
    for (const t of list) box.append(taskRow(t, kind, ago(t.seconds)));
  }
  if (done.length) {
    const d = el("details");
    d.append(el("summary", "", `Done (${done.length})`));
    for (const t of done) d.append(taskRow(t, "done", "done"));
    box.append(d);
  }
  const total = overdue.length + upcoming.length + done.length;
  if (!total) box.append(el("p", "empty", "No tasks today."));
  $("today-count").textContent = total ? `${done.length}/${total}` : "";
}

function renderHabits(habits) {
  const box = $("habits");
  box.replaceChildren();
  if (!habits.length) {
    box.append(el("p", "empty", "No habits yet. Repeat a task to start one."));
    return;
  }
  let done = 0, scheduled = 0;
  for (const h of habits) {
    done += h.week_done;
    scheduled += h.week_scheduled;
    const card = el("div", "habit");
    const top = el("div", "top");
    top.append(el("span", "", h.name), el("span", "streak", h.streak ? `🔥 ${h.streak}` : "0"));
    const grid = el("div", "grid");
    grid.setAttribute("aria-label", "last 4 weeks, Monday to Sunday");
    for (const c of h.cells) grid.append(el("i", c));
    card.append(top, grid);
    box.append(card);
  }
  if (scheduled) {
    const week = el("p", "", `This week ${done}/${scheduled} · ${Math.round((done * 100) / scheduled)}%`);
    week.id = "week";
    box.append(week);
  }
}

function renderInbox(items) {
  const box = $("inbox");
  box.replaceChildren();
  $("inbox-count").textContent = items.length || "";
  if (!items.length) {
    box.append(el("li", "empty", "Inbox empty."));
    return;
  }
  for (const it of items) {
    const li = el("li");
    const b = el("button", "row", "📥 " + it.desc);
    b.type = "button";
    b.addEventListener("click", () => scheduleInbox(it));
    const x = el("button", "x", "✕");
    x.type = "button";
    x.setAttribute("aria-label", "Delete " + it.desc);
    x.addEventListener("click", () => deleteInbox(it));
    li.append(b, x);
    box.append(li);
  }
}

async function load() {
  try {
    const [st, habits, inbox] = await Promise.all([api("GET", "status"), api("GET", "habits"), api("GET", "inbox")]);
    renderToday(st);
    renderHabits(habits);
    renderInbox(inbox);
    lastSync = new Date();
    document.body.classList.remove("stale");
    $("stale").hidden = true;
  } catch {
    document.body.classList.add("stale");
    $("stale").hidden = false;
    $("stale").textContent = lastSync
      ? `⚠ daemon unreachable — stale since ${lastSync.toTimeString().slice(0, 5)}`
      : "⚠ daemon unreachable — never synced";
  }
  $("date").textContent = new Date().toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
}

async function act(promise, ok) {
  try {
    const res = await promise;
    toast(ok(res));
  } catch (e) {
    toast(e.message);
  }
  load();
}

function openSheet(t) {
  const sheet = $("sheet");
  $("sheet-title").textContent = `${t.time}  ${t.desc}`;
  sheet.onclose = () => {
    switch (sheet.returnValue) {
      case "done":
        act(api("POST", `task/${t.id}/done`), () => "Marked done");
        break;
      case "snooze":
        act(api("POST", `task/${t.id}/snooze`), () => "Snoozed");
        break;
      case "edit": {
        const time = prompt("New time (HH:MM)", t.time);
        if (time) act(api("POST", `task/${t.id}`, { time }), () => `Moved to ${time}`);
        break;
      }
    }
  };
  sheet.returnValue = "cancel";
  sheet.showModal();
}

function scheduleInbox(it) {
  const text = prompt(`When? (e.g. at 15:00, tomorrow 9:30)\n${it.desc}`);
  if (text) act(api("POST", `inbox/${it.id}/schedule`, { text }), () => "Scheduled");
}

function deleteInbox(it) {
  if (confirm(`Delete "${it.desc}"?`)) act(api("DELETE", `inbox/${it.id}`), () => "Deleted");
}

$("add").addEventListener("submit", (e) => {
  e.preventDefault();
  const input = $("add-text");
  const text = input.value.trim();
  if (!text) return;
  act(api("POST", "capture", { text }).then((r) => {
    input.value = "";
    return r;
  }), (r) => r.echo || "Added");
});

function b64urlBytes(s) {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=");
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

async function enablePush() {
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
    toast("Notifications need the installed app in Chrome.");
    return;
  }
  if ((await Notification.requestPermission()) !== "granted") {
    toast("Notifications are blocked for this app in Chrome settings.");
    return;
  }
  try {
    const reg = await navigator.serviceWorker.ready;
    const key = b64urlBytes((await api("GET", "push/key")).public_key);
    let sub = await reg.pushManager.getSubscription();
    const old = sub && new Uint8Array(sub.options.applicationServerKey || []);
    if (sub && (!old || old.length !== key.length || old.some((v, i) => v !== key[i]))) {
      await sub.unsubscribe();
      sub = null;
    }
    sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    const res = await fetch("/api/push/subscription", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sub.toJSON()),
    });
    if (!res.ok) throw new Error(await res.text());
    await api("POST", "push/test");
    $("bell").classList.add("on");
    toast("Notifications on. A test notification is on its way.");
  } catch (e) {
    toast("Could not turn on notifications: " + e.message);
  }
}

$("bell").addEventListener("click", enablePush);

if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js");
  navigator.serviceWorker.ready
    .then((reg) => reg.pushManager.getSubscription())
    .then((sub) => sub && Notification.permission === "granted" && $("bell").classList.add("on"));
}

document.addEventListener("visibilitychange", () => document.visibilityState === "visible" && load());
setInterval(() => document.visibilityState === "visible" && load(), 30000);
if (location.hash === "#add") $("add-text").focus();
load();
