// FlowBreakerSync reads today's events from macOS Calendar and pushes them to
// the flow-breaker daemon (POST /calendar/events). flow-breaker starts it with
// `open -g -j`; it has no window and writes calsync.json after every attempt.
import AppKit
import EventKit

let store = EKEventStore()
let env = ProcessInfo.processInfo.environment
let home = FileManager.default.homeDirectoryForCurrentUser
let workAccount = "darius@optura.ai"
let iso = ISO8601DateFormatter()
let hhmm: DateFormatter = {
    let f = DateFormatter()
    f.dateFormat = "HH:mm"
    return f
}()

func daemonURL() -> String {
    if let u = env["FLOW_BREAKER_URL"], !u.isEmpty { return u }
    let path = home.appendingPathComponent(".config/flow-breaker/client")
    if let s = try? String(contentsOf: path, encoding: .utf8) {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        if !t.isEmpty { return t }
    }
    return "http://localhost:7777"
}

func statusURL() -> URL {
    let dir = env["FLOW_BREAKER_DIR"].map { URL(fileURLWithPath: $0) } ?? home.appendingPathComponent(".flow-breaker")
    try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return dir.appendingPathComponent("calsync.json")
}

var lastSuccess: String = {
    guard let data = try? Data(contentsOf: statusURL()),
          let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return "" }
    return obj["last_success"] as? String ?? ""
}()

func writeStatus(events: Int, error: String) {
    let now = iso.string(from: Date())
    if error.isEmpty { lastSuccess = now }
    let st: [String: Any] = ["last_attempt": now, "last_success": lastSuccess, "events": events, "error": error]
    if let data = try? JSONSerialization.data(withJSONObject: st) {
        try? data.write(to: statusURL(), options: .atomic)
    }
}

func calendars() -> [EKCalendar] {
    let all = store.calendars(for: .event)
    let work = all.filter { $0.source.title.contains(workAccount) }
    return work.isEmpty ? all : work
}

func sync() {
    let start = Calendar.current.startOfDay(for: Date())
    let end = Calendar.current.date(byAdding: .day, value: 1, to: start)!
    let pred = store.predicateForEvents(withStart: start, end: end, calendars: calendars())
    let events = store.events(matching: pred).filter { !$0.isAllDay && $0.status != .canceled && $0.startDate >= start }
    let payload: [[String: Any]] = events.map { ev in
        let declined = ev.attendees?.first(where: { $0.isCurrentUser })?.participantStatus == .declined
        let start = hhmm.string(from: ev.startDate)
        return [
            "uid": (ev.calendarItemExternalIdentifier ?? ev.eventIdentifier ?? ev.title ?? "") + "@" + start,
            "summary": ev.title ?? "",
            "start_time": start,
            "end_time": hhmm.string(from: ev.endDate),
            "calendar_name": ev.calendar.title,
            "declined": declined,
        ]
    }
    guard let url = URL(string: daemonURL() + "/calendar/events"),
          let body = try? JSONSerialization.data(withJSONObject: ["events": payload]) else {
        writeStatus(events: 0, error: "bad daemon URL")
        return
    }
    var req = URLRequest(url: url, timeoutInterval: 30)
    req.httpMethod = "POST"
    req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    req.httpBody = body
    URLSession.shared.dataTask(with: req) { _, resp, err in
        DispatchQueue.main.async {
            if let err = err {
                writeStatus(events: payload.count, error: err.localizedDescription)
                return
            }
            let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
            writeStatus(events: payload.count, error: code == 200 ? "" : "daemon returned \(code)")
        }
    }.resume()
}

final class Delegate: NSObject, NSApplicationDelegate {
    var timer: Timer?

    func applicationDidFinishLaunching(_ notification: Notification) {
        store.requestFullAccessToEvents { granted, _ in
            DispatchQueue.main.async {
                guard granted else {
                    writeStatus(events: 0, error: "calendar access denied")
                    return
                }
                sync()
                NotificationCenter.default.addObserver(forName: .EKEventStoreChanged, object: store, queue: .main) { _ in sync() }
                // The 15-minute tick also loads the new day after midnight.
                self.timer = Timer.scheduledTimer(withTimeInterval: 900, repeats: true) { _ in sync() }
            }
        }
    }
}

let app = NSApplication.shared
let delegate = Delegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
