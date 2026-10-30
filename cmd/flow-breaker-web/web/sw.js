// flow-breaker service worker: caches the app shell (never /api/*) and shows
// alarm notifications with Done and Snooze buttons.
"use strict";

const CACHE = "flow-breaker-v1";
const SHELL = ["/", "/index.html", "/app.js", "/style.css", "/manifest.webmanifest", "/icon-192.png"];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

// Network first, so a new build shows up; the cached shell when offline.
self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== "GET" || url.origin !== self.location.origin || url.pathname.startsWith("/api/")) return;
  event.respondWith(
    fetch(event.request)
      .then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(event.request, copy));
        }
        return res;
      })
      .catch(() => caches.match(event.request).then((hit) => hit || caches.match("/index.html"))),
  );
});

// Push payload: {title, body, taskId, tag, priority}. The daemon repeats an
// alarm every minute; the phone buzzes only when the priority rose.
self.addEventListener("push", (event) => {
  let m = {};
  try {
    m = event.data ? event.data.json() : {};
  } catch {
    /* not JSON */
  }
  event.waitUntil((async () => {
    const tag = m.tag || "flow-breaker";
    const priority = m.priority || 3;
    const [prev] = await self.registration.getNotifications({ tag });
    const louder = !prev || priority > ((prev.data && prev.data.priority) || 0);
    const opts = {
      body: m.body || "",
      tag,
      icon: "/icon-192.png",
      data: { taskId: m.taskId, priority },
      renotify: Boolean(prev) && louder,
      silent: !louder,
      requireInteraction: priority >= 5,
    };
    if (m.taskId) opts.actions = [{ action: "done", title: "Done" }, { action: "snooze", title: "Snooze" }];
    await self.registration.showNotification(m.title || "flow-breaker", opts);
  })());
});

self.addEventListener("notificationclick", (event) => {
  const n = event.notification;
  n.close();
  const id = n.data && n.data.taskId;
  if (id && (event.action === "done" || event.action === "snooze")) {
    event.waitUntil(fetch(`/api/task/${id}/${event.action}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source: "phone" }),
    }));
    return;
  }
  event.waitUntil(self.clients.matchAll({ type: "window" }).then((wins) => (wins[0] ? wins[0].focus() : self.clients.openWindow("/"))));
});
