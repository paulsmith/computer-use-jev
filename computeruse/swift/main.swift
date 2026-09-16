// herbie computer-use worker: a persistent macOS Accessibility backend.
// Reads newline-delimited JSON requests from stdin, writes newline-delimited
// JSON responses to stdout. One request in flight at a time; request ids are
// positive, strictly increasing integers echoed in the response.
//
// Requires the Accessibility (and, for screenshots, Screen Recording) TCC
// grant held by the responsible terminal application. Requires macOS 14+.

import AppKit
import ApplicationServices
import CoreGraphics
import Foundation
import ScreenCaptureKit

// MARK: - Bounds

let maxElements = 2000
let maxDepth = 32
let maxTextBytes = 48 * 1024
let maxAttrChars = 200
let traversalTimeout: TimeInterval = 10
let axMessagingTimeout: TimeInterval = 10
let activateVerifyTimeout: TimeInterval = 2
let maxAppRefs = 256
let maxWindowRefs = 256
let maxPayloadBytes = 64 * 1024
let maxKeyBytes = 128

// MARK: - State

struct AppIdentity: Equatable {
    var pid: pid_t
    var bundleID: String
    var launchEpoch: Double
    var name: String
}

var appTokens: [String: AppIdentity] = [:]
var windowTokens: [String: AXUIElement] = [:]
var elementTokens: [String: AXUIElement] = [:]
var nextAppSeq = 0
var nextWindowSeq = 0
var initialized = false
var lastID = 0

let systemWide = AXUIElementCreateSystemWide()

// MARK: - Errors

struct WorkerError: Error {
    var code: String
    var message: String
}

func fail(_ code: String, _ message: String) throws -> Never {
    throw WorkerError(code: code, message: message)
}

// MARK: - AX helpers

func axSetTimeout(_ element: AXUIElement) {
    AXUIElementSetMessagingTimeout(element, Float(axMessagingTimeout))
}

func copyAttribute(_ element: AXUIElement, _ attribute: String) -> (AXError, CFTypeRef?) {
    axSetTimeout(element)
    var value: CFTypeRef?
    let error = AXUIElementCopyAttributeValue(element, attribute as CFString, &value)
    return (error, value)
}

func attributeString(_ element: AXUIElement, _ attribute: String) -> String? {
    let (error, value) = copyAttribute(element, attribute)
    guard error == .success, let value, let string = value as? String else { return nil }
    return string
}

func attributeBool(_ element: AXUIElement, _ attribute: String) -> Bool? {
    let (error, value) = copyAttribute(element, attribute)
    guard error == .success, let value else { return nil }
    return value as? Bool
}

func attributePoint(_ element: AXUIElement, _ attribute: String) -> CGPoint? {
    let (error, value) = copyAttribute(element, attribute)
    guard error == .success, let value else { return nil }
    var point = CGPoint.zero
    guard AXValueGetValue(value as! AXValue, .cgPoint, &point) else { return nil }
    return point
}

func attributeSize(_ element: AXUIElement, _ attribute: String) -> CGSize? {
    let (error, value) = copyAttribute(element, attribute)
    guard error == .success, let value else { return nil }
    var size = CGSize.zero
    guard AXValueGetValue(value as! AXValue, .cgSize, &size) else { return nil }
    return size
}

func attributeElements(_ element: AXUIElement, _ attribute: String) -> [AXUIElement]? {
    let (error, value) = copyAttribute(element, attribute)
    guard error == .success, let value, let array = value as? [AXUIElement] else { return nil }
    return array
}

func appName(_ pid: pid_t) -> String {
    NSRunningApplication(processIdentifier: pid)?.localizedName ?? "pid \(pid)"
}

func elementPID(_ element: AXUIElement) -> pid_t {
    var pid: pid_t = 0
    AXUIElementGetPid(element, &pid)
    return pid
}

// MARK: - Secure-field classification

enum SecurityClassification: String {
    case secure
    case notSecure = "not_secure"
    case unknown
}

// classifyEditableSecurity reads the subrole twice and requires agreement:
// "secure" means the subrole is exactly AXSecureTextField. "not_secure"
// covers an ordinary subrole, a missing value, or an element without the
// subrole attribute at all (it cannot report AXSecureTextField, so it cannot
// be a secure field by the platform convention). Anything unreadable,
// non-string, or flapping is "unknown". Value reads only happen for editable
// elements classified not_secure.
func classifyEditableSecurity(_ element: AXUIElement) -> SecurityClassification {
    let first = copyAttribute(element, kAXSubroleAttribute)
    let second = copyAttribute(element, kAXSubroleAttribute)
    guard first.0 == second.0 else { return .unknown }
    if first.0 == .noValue || first.0 == .attributeUnsupported { return .notSecure }
    guard first.0 == .success, let value = first.1 else { return .unknown }
    guard let subrole = value as? String else { return .unknown }
    guard let repeatSubrole = second.1 as? String, repeatSubrole == subrole else { return .unknown }
    return subrole == "AXSecureTextField" ? .secure : .notSecure
}

func isEditableByRoleOrSettable(_ element: AXUIElement, role: String) -> Bool {
    if role == "AXTextField" || role == "AXTextArea" || role == "AXComboBox" { return true }
    axSetTimeout(element)
    var settable = DarwinBoolean(false)
    return AXUIElementIsAttributeSettable(element, kAXValueAttribute as CFString, &settable) == .success && settable.boolValue
}

// MARK: - Token resolution

func resolveAppToken(_ token: String) throws -> AppIdentity {
    guard let identity = appTokens[token] else {
        try fail("unknown_token", "unknown app token \(token); list apps again with the apps action")
    }
    // Verify the pid still names the same application: a bare liveness check
    // would accept a recycled pid pointing at a different app.
    guard let running = NSRunningApplication(processIdentifier: identity.pid),
          running.bundleIdentifier ?? "" == identity.bundleID else {
        try fail("stale_target", "application \(identity.name) is no longer running; list apps again")
    }
    return identity
}

func resolveWindowToken(_ token: String) throws -> AXUIElement {
    guard let window = windowTokens[token] else {
        try fail("unknown_token", "unknown window token \(token); list windows again with the windows action")
    }
    try requireLive(window, kind: "window", token: token)
    return window
}

func resolveElementToken(_ token: String) throws -> AXUIElement {
    guard let element = elementTokens[token] else {
        try fail("unknown_token", "unknown element target \(token); take a new snapshot")
    }
    try requireLive(element, kind: "element", token: token)
    return element
}

func requireLive(_ element: AXUIElement, kind: String, token: String) throws {
    let (error, _) = copyAttribute(element, kAXRoleAttribute)
    if error == .cannotComplete {
        try fail("ax_timeout", "the application did not respond to an Accessibility request within \(Int(axMessagingTimeout)) seconds")
    }
    guard error == .success else {
        try fail("stale_target", "\(kind) \(token) is no longer valid; rediscover it")
    }
}


func asAXElement(_ value: CFTypeRef?) -> AXUIElement? {
    guard let value, CFGetTypeID(value) == AXUIElementGetTypeID() else { return nil }
    return unsafeDowncast(value, to: AXUIElement.self)
}

// refreshWorkspace performs one runloop pass so NSWorkspace receives pending
// application lifecycle notifications. Without it, runningApplications and
// frontmostApplication report a snapshot frozen at process start because the
// worker never blocks in the runloop otherwise.
func refreshWorkspace() {
    _ = RunLoop.current.run(mode: .default, before: Date().addingTimeInterval(0.05))
}

// MARK: - Actions

func handleApps() throws -> [String: Any] {
    refreshWorkspace()
    var result: [[String: Any]] = []
    for app in NSWorkspace.shared.runningApplications {
        guard app.activationPolicy == .regular, app.processIdentifier > 0, result.count < maxAppRefs else { continue }
        let bundleID = app.bundleIdentifier ?? ""
        let launchEpoch = app.launchDate?.timeIntervalSince1970 ?? 0
        let identity = AppIdentity(
            pid: app.processIdentifier, bundleID: bundleID, launchEpoch: launchEpoch, name: app.localizedName ?? bundleID
        )
        var token = ""
        for (existing, prior) in appTokens where prior == identity {
            token = existing
            break
        }
        if token.isEmpty {
            nextAppSeq += 1
            token = "a\(nextAppSeq)"
            appTokens[token] = identity
        }
        result.append(["token": token, "pid": Int(identity.pid), "bundle_id": bundleID, "name": identity.name])
    }
    return ["apps": result]
}

func handleWindows(_ params: [String: Any]) throws -> [String: Any] {
    guard let token = params["app"] as? String, !token.isEmpty else {
        try fail("invalid_params", "windows requires an app token")
    }
    let identity = try resolveAppToken(token)
    let appElement = AXUIElementCreateApplication(identity.pid)
    guard let windows = attributeElements(appElement, kAXWindowsAttribute) else {
        try fail("ax_error", "could not read windows of \(identity.name)")
    }
    windowTokens = [:]
    var result: [[String: Any]] = []
    for window in windows where result.count < maxWindowRefs {
        // The role read doubles as a liveness guard for the retained window.
        guard attributeString(window, kAXRoleAttribute) != nil else { continue }
        nextWindowSeq += 1
        let token = "w\(nextWindowSeq)"
        windowTokens[token] = window
        result.append([
            "token": token,
            "title": attributeString(window, kAXTitleAttribute) ?? "",
            "main": attributeBool(window, kAXMainAttribute) ?? false,
            "focused": attributeBool(window, kAXFocusedAttribute) ?? false,
            "minimized": attributeBool(window, kAXMinimizedAttribute) ?? false,
        ])
    }
    return ["windows": result]
}

func handleActivate(_ params: [String: Any]) throws -> [String: Any] {
    guard let token = params["app"] as? String, !token.isEmpty else {
        try fail("invalid_params", "activate requires an app token")
    }
    let identity = try resolveAppToken(token)
    guard let app = NSRunningApplication(processIdentifier: identity.pid) else {
        try fail("stale_target", "application \(identity.name) is no longer running")
    }
    app.activate()
    var windowTitle = ""
    if let windowToken = params["window"] as? String, !windowToken.isEmpty {
        let window = try resolveWindowToken(windowToken)
        windowTitle = attributeString(window, kAXTitleAttribute) ?? ""
        AXUIElementSetAttributeValue(window, kAXMainAttribute as CFString, kCFBooleanTrue)
        AXUIElementSetAttributeValue(window, kAXFocusedAttribute as CFString, kCFBooleanTrue)
    }
    // Activation is asynchronous: verify it took effect within a bounded window.
    refreshWorkspace()
    let deadline = Date().addingTimeInterval(activateVerifyTimeout)
    while NSWorkspace.shared.frontmostApplication?.processIdentifier != identity.pid {
        if Date() >= deadline {
            try fail("ax_error", "\(identity.name) did not become frontmost within \(Int(activateVerifyTimeout)) seconds")
        }
        refreshWorkspace()
        Thread.sleep(forTimeInterval: 0.05)
    }
    return ["app": identity.name, "window_title": windowTitle]
}

func frontmostFocusedWindow() throws -> AXUIElement {
    let (error, value) = copyAttribute(systemWide, kAXFocusedApplicationAttribute)
    guard error == .success, let application = asAXElement(value) else {
        // Focused-state reads through the system-wide element can fail with
        // kAXErrorCannotComplete when the calling process is not attached to
        // the GUI session (for example an SSH-spawned context) even though
        // application-scoped reads succeed and AXIsProcessTrusted is true.
        if error == .cannotComplete {
            try fail(
                "ax_error",
                "could not determine the frontmost application from this process context; pass a window token from the windows action instead")
        }
        try fail("no_frontmost", "no frontmost application; is there a GUI session?")
    }
    let (windowError, windowValue) = copyAttribute(application, kAXFocusedWindowAttribute)
    guard windowError == .success, let window = asAXElement(windowValue) else {
        try fail("no_window", "the frontmost application has no focused window")
    }
    return window
}

// requireFrontmost verifies that the target application is the frontmost
// application. Key equivalents and keyboard event delivery only work
// reliably for the active application, so keyboard actions refuse to post
// into a background application instead of being silently swallowed.
func requireFrontmost(_ pid: pid_t, name: String) throws {
    refreshWorkspace()
    guard NSWorkspace.shared.frontmostApplication?.processIdentifier == pid else {
        try fail("not_frontmost", "\(name) is not the frontmost application; activate it first")
    }
}

func scopedWindow(_ params: [String: Any]) throws -> AXUIElement {
    if let token = params["window"] as? String, !token.isEmpty {
        return try resolveWindowToken(token)
    }
    return try frontmostFocusedWindow()
}

// MARK: - Snapshot

func describeLine(role: String, title: String, description: String, flags: [String], value: String) -> String {
    var line = "- \(role)"
    let label = !title.isEmpty ? title : description
    if !label.isEmpty {
        line += " \"\(escape(label))\""
    }
    if !flags.isEmpty {
        line += " [" + flags.joined(separator: ", ") + "]"
    }
    if !value.isEmpty {
        line += " value=\"\(escape(value))\""
    }
    return line
}

func escape(_ text: String) -> String {
    var out = ""
    for character in text {
        switch character {
        case "\\": out += "\\\\"
        case "\"": out += "\\\""
        case "\n": out += "\\n"
        case "\r": out += "\\r"
        case "\t": out += "\\t"
        default: out.append(character)
        }
    }
    return out
}

func cap(_ text: String) -> String {
    String(text.prefix(maxAttrChars))
}

func handleSnapshot(_ params: [String: Any]) throws -> [String: Any] {
    let root: AXUIElement
    if let token = params["target"] as? String, !token.isEmpty {
        root = try resolveElementToken(token)
    } else {
        root = try scopedWindow(params)
    }
    let deadline = Date().addingTimeInterval(traversalTimeout)
    var stack: [(AXUIElement, Int)] = [(root, 0)]
    var lines: [String] = []
    var newTokens: [String: AXUIElement] = [:]
    var seq = 0
    var visited = 0
    var maxSeenDepth = 0
    var textBytes = 0
    var truncated = false

    while let (element, depth) = stack.popLast() {
        if Date() >= deadline || visited >= maxElements {
            truncated = true
            break
        }
        if depth > maxDepth {
            truncated = true
            continue
        }
        maxSeenDepth = max(maxSeenDepth, depth)
        visited += 1
        let role = attributeString(element, kAXRoleAttribute) ?? ""
        if role.isEmpty { continue }
        let editable = isEditableByRoleOrSettable(element, role: role)
        let classification = editable ? classifyEditableSecurity(element) : .notSecure
        var flags: [String] = []
        if attributeBool(element, kAXEnabledAttribute) == false { flags.append("disabled") }
        if attributeBool(element, kAXFocusedAttribute) == true { flags.append("focused") }
        if attributeBool(element, kAXSelectedAttribute) == true { flags.append("selected") }
        if editable { flags.append(classification == .secure ? "secure" : "editable") }
        if classification == .unknown { flags.append("unknown-editable") }
        var value = ""
        if editable && classification == .notSecure {
            let (error, raw) = copyAttribute(element, kAXValueAttribute)
            if error == .success, let raw {
                if let string = raw as? String {
                    value = cap(string)
                } else if let number = raw as? NSNumber {
                    value = cap(number.stringValue)
                }
            }
        } else if !editable {
            // Non-editable values are rendered text (labels, results), not
            // user input, so reading them cannot leak typed content.
            if let string = attributeString(element, kAXValueAttribute) {
                value = cap(string)
            }
        }
        seq += 1
        let token = "e\(seq)"
        newTokens[token] = element
        flags.append(token)
        let line = String(repeating: "  ", count: depth) + describeLine(
            role: role,
            title: cap(attributeString(element, kAXTitleAttribute) ?? ""),
            description: cap(attributeString(element, kAXDescriptionAttribute) ?? ""),
            flags: flags,
            value: value
        )
        let lineBytes = line.utf8.count
        if textBytes + lineBytes + 1 > maxTextBytes {
            truncated = true
            break
        }
        lines.append(line)
        textBytes += lineBytes + 1
        if let children = attributeElements(element, kAXChildrenAttribute) {
            for child in children.reversed() {
                stack.append((child, depth + 1))
            }
        }
    }
    elementTokens = newTokens
    return [
        "text": lines.joined(separator: "\n"),
        "elements": visited,
        "depth": maxSeenDepth,
        "truncated": truncated,
    ]
}

// MARK: - Mutations

func elementSummary(_ element: AXUIElement) -> [String: Any] {
    let title = attributeString(element, kAXTitleAttribute) ?? attributeString(element, kAXDescriptionAttribute) ?? ""
    return ["role": attributeString(element, kAXRoleAttribute) ?? "", "title": cap(title)]
}

func requireEditableNonSecure(_ element: AXUIElement) throws {
    let role = attributeString(element, kAXRoleAttribute) ?? ""
    guard isEditableByRoleOrSettable(element, role: role) else {
        try fail("not_editable", "target element does not accept text")
    }
    switch classifyEditableSecurity(element) {
    case .secure:
        try fail("secure_field", "target element is a secure field; computer_use does not operate secure fields")
    case .unknown:
        try fail("secure_field", "target element could not be classified as non-secure; refusing to operate it")
    case .notSecure:
        break
    }
}

func readPayload(_ params: [String: Any], _ field: String) throws -> String {
    guard let value = params[field] as? String else {
        try fail("invalid_params", "\(field) must be a string")
    }
    guard value.utf8.count <= maxPayloadBytes else {
        try fail("payload_oversized", "\(field) exceeds \(maxPayloadBytes) bytes")
    }
    return value
}

func handleClick(_ params: [String: Any]) throws -> [String: Any] {
    guard let token = params["target"] as? String, !token.isEmpty else {
        try fail("invalid_params", "click requires a target")
    }
    let element = try resolveElementToken(token)
    axSetTimeout(element)
    let error = AXUIElementPerformAction(element, kAXPressAction as CFString)
    guard error == .success else {
        try fail("ax_error", "press failed on the target element: \(errorDescription(error))")
    }
    return elementSummary(element)
}

func handleFill(_ params: [String: Any]) throws -> [String: Any] {
    guard let token = params["target"] as? String, !token.isEmpty else {
        try fail("invalid_params", "fill requires a target")
    }
    let text = try readPayload(params, "text")
    let element = try resolveElementToken(token)
    try requireEditableNonSecure(element)
    axSetTimeout(element)
    let error = AXUIElementSetAttributeValue(element, kAXValueAttribute as CFString, text as CFString)
    guard error == .success else {
        try fail("ax_error", "setting the value failed: \(errorDescription(error))")
    }
    return elementSummary(element)
}

func handleType(_ params: [String: Any]) throws -> [String: Any] {
    guard let token = params["target"] as? String, !token.isEmpty else {
        try fail("invalid_params", "type requires a target")
    }
    let text = try readPayload(params, "text")
    let element = try resolveElementToken(token)
    try requireEditableNonSecure(element)
    let pid = elementPID(element)
    try requireFrontmost(pid, name: appName(pid))
    axSetTimeout(element)
    let focusError = AXUIElementSetAttributeValue(element, kAXFocusedAttribute as CFString, kCFBooleanTrue)
    guard focusError == .success, attributeBool(element, kAXFocusedAttribute) == true else {
        try fail("ax_error", "could not focus the target element for typing")
    }
    for scalar in text.unicodeScalars {
        var units = Array(String(scalar).utf16)
        for down in [true, false] {
            guard let event = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: down) else {
                try fail("ax_error", "could not create a keyboard event")
            }
            event.keyboardSetUnicodeString(stringLength: units.count, unicodeString: &units)
            event.postToPid(pid)
        }
    }
    return elementSummary(element)
}

// MARK: - Key press

let keyCodes: [String: CGKeyCode] = [
    "a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05, "z": 0x06, "x": 0x07,
    "c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C, "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10,
    "t": 0x11, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17, "9": 0x19,
    "7": 0x1A, "8": 0x1C, "0": 0x1D, "o": 0x1F, "u": 0x20, "i": 0x22, "p": 0x23, "l": 0x25,
    "j": 0x26, "k": 0x28, "n": 0x2D, "m": 0x2E,
    "return": 0x24, "escape": 0x35, "tab": 0x30, "space": 0x31, "delete": 0x33,
    "home": 0x73, "end": 0x77, "pageup": 0x74, "pagedown": 0x79,
    "left": 0x7B, "right": 0x7C, "down": 0x7D, "up": 0x7E,
    "f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60, "f6": 0x61, "f7": 0x62,
    "f8": 0x64, "f9": 0x65, "f10": 0x6D, "f11": 0x67, "f12": 0x6F,
]

let modifierFlags: [String: UInt64] = [
    "cmd": 1 << 20, "command": 1 << 20, "ctrl": 1 << 12, "control": 1 << 12,
    "alt": 1 << 19, "option": 1 << 19, "shift": 1 << 17,
]

func handlePress(_ params: [String: Any]) throws -> [String: Any] {
    guard let token = params["app"] as? String, !token.isEmpty else {
        try fail("invalid_params", "press requires an app token")
    }
    guard let key = params["key"] as? String, !key.isEmpty else {
        try fail("invalid_params", "press requires a key")
    }
    guard key.utf8.count <= maxKeyBytes else {
        try fail("invalid_params", "key exceeds \(maxKeyBytes) bytes")
    }
    let identity = try resolveAppToken(token)
    try requireFrontmost(identity.pid, name: identity.name)
    let parts = key.lowercased().split(separator: "+", omittingEmptySubsequences: false).map(String.init)
    guard parts.count >= 1 else {
        try fail("invalid_params", "key is empty")
    }
    var flags: UInt64 = 0
    for part in parts.dropLast() {
        guard let flag = modifierFlags[part] else {
            try fail("unsupported_key", "unknown modifier \"\(part)\" in key \"\(key)\"")
        }
        flags |= flag
    }
    guard let code = keyCodes[parts.last!] else {
        try fail("unsupported_key", "unknown key \"\(parts.last!)\" in key \"\(key)\"")
    }
    for down in [true, false] {
        guard let event = CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: down) else {
            try fail("ax_error", "could not create a keyboard event")
        }
        if flags != 0 {
            event.flags = CGEventFlags(rawValue: flags)
        }
        event.postToPid(identity.pid)
    }
    return ["key": key]
}

// MARK: - Screenshot

// runAsync bridges an async closure into the worker's synchronous dispatch.
func runAsync<T>(_ body: @escaping () async throws -> T) throws -> T {
    let done = DispatchSemaphore(value: 0)
    let box = ResultBox<T>()
    Task {
        do {
            box.store(.success(try await body()))
        } catch {
            box.store(.failure(error))
        }
        done.signal()
    }
    done.wait()
    return try box.load()
}

final class ResultBox<T> {
    private var result: Result<T, Error>?
    func store(_ value: Result<T, Error>) { result = value }
    func load() throws -> T {
        guard let result else { throw WorkerError(code: "internal", message: "async result unavailable") }
        return try result.get()
    }
}

func handleScreenshot(_ params: [String: Any]) throws -> [String: Any] {
    guard CGPreflightScreenCaptureAccess() else {
        try fail(
            "screen_recording_denied",
            "Screen Recording permission is not granted to the terminal running herbie; grant it in System Settings > Privacy & Security > Screen Recording and restart the terminal")
    }
    let window = try scopedWindow(params)
    let pid = elementPID(window)
    guard let position = attributePoint(window, kAXPositionAttribute),
          let size = attributeSize(window, kAXSizeAttribute) else {
        try fail("ax_error", "could not read the window bounds")
    }
    // Map the AX window to a CoreGraphics window ID by owner pid and exact
    // bounds (both are top-left, in points). The CG window list needs no
    // Screen Recording grant; only the capture itself does.
    guard let cgWindows = CGWindowListCopyWindowInfo(.optionAll, kCGNullWindowID) as? [[String: Any]] else {
        try fail("capture_failed", "could not enumerate windows")
    }
    var windowIDs: [CGWindowID] = []
    for info in cgWindows {
        guard (info[kCGWindowOwnerPID as String] as? Int32) == pid else { continue }
        guard let bounds = info[kCGWindowBounds as String] as? [String: Any] else { continue }
        guard let x = (bounds["X"] as? NSNumber)?.doubleValue,
              let y = (bounds["Y"] as? NSNumber)?.doubleValue,
              let width = (bounds["Width"] as? NSNumber)?.doubleValue,
              let height = (bounds["Height"] as? NSNumber)?.doubleValue else { continue }
        if x == position.x, y == position.y, width == size.width, height == size.height {
            if let windowID = (info[kCGWindowNumber as String] as? NSNumber)?.uint32Value {
                windowIDs.append(windowID)
            }
        }
    }
    guard windowIDs.count == 1 else {
        if windowIDs.isEmpty {
            try fail("capture_failed", "no on-screen window matches the target bounds; the window may be minimized or off-screen")
        }
        try fail("capture_failed", "multiple on-screen windows match the target bounds")
    }
    let windowID = windowIDs[0]
    // Join the CG window ID to the ScreenCaptureKit window by exact identity
    // and capture it.
    let image = try runAsync { () -> CGImage in
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        guard let scWindow = content.windows.first(where: { $0.windowID == windowID }) else {
            throw WorkerError(code: "capture_failed", message: "the window is not capturable; it may be minimized")
        }
        let configuration = SCStreamConfiguration()
        configuration.showsCursor = false
        return try await SCScreenshotManager.captureImage(
            contentFilter: SCContentFilter(desktopIndependentWindow: scWindow), configuration: configuration)
    }
    let data = NSMutableData()
    guard let destination = CGImageDestinationCreateWithData(data, "public.png" as CFString, 1, nil) else {
        try fail("capture_failed", "could not create a PNG encoder")
    }
    CGImageDestinationAddImage(destination, image, nil)
    guard CGImageDestinationFinalize(destination) else {
        try fail("capture_failed", "could not encode the window image")
    }
    return [
        "png_base64": (data as Data).base64EncodedString(),
        "width": image.width,
        "height": image.height,
    ]
}

// MARK: - Error descriptions

func errorDescription(_ error: AXError) -> String {
    switch error {
    case .success: return "success"
    case .apiDisabled: return "accessibility is not enabled for this application (grant Accessibility to the terminal in System Settings)"
    case .invalidUIElement: return "the element is no longer valid"
    case .invalidUIElementObserver: return "invalid element observer"
    case .cannotComplete: return "the operation could not complete (application busy or unresponsive)"
    case .attributeUnsupported: return "the element does not support this attribute"
    case .actionUnsupported: return "the element does not support this action"
    case .notEnoughPrecision: return "not enough precision"
    case .notImplemented: return "not implemented"
    case .noValue: return "no value"
    case .failure: return "the operation failed"
    case .illegalArgument, .notificationUnsupported, .notificationAlreadyRegistered, .notificationNotRegistered,
         .parameterizedAttributeUnsupported: return "unsupported operation"
    @unknown default: return "unknown error"
    }
}

// MARK: - Dispatch

func dispatch(_ request: [String: Any]) throws -> [String: Any] {
    guard let id = request["id"] as? Int, id > 0 else {
        try fail("invalid_id", "id must be a positive integer")
    }
    guard id > lastID else {
        try fail("out_of_order_id", "id \(id) is not greater than \(lastID)")
    }
    lastID = id
    guard let method = request["method"] as? String else {
        try fail("invalid_method", "method must be a string")
    }
    let params = (request["params"] as? [String: Any]) ?? [:]
    if method == "initialize" {
        guard !initialized else {
            try fail("already_initialized", "worker is already initialized")
        }
        initialized = true
        return ["server": "herbie-computer-use"]
    }
    guard initialized else {
        try fail("not_initialized", "worker is not initialized")
    }
    switch method {
    case "apps": return try handleApps()
    case "windows": return try handleWindows(params)
    case "activate": return try handleActivate(params)
    case "snapshot": return try handleSnapshot(params)
    case "click": return try handleClick(params)
    case "fill": return try handleFill(params)
    case "type": return try handleType(params)
    case "press": return try handlePress(params)
    case "screenshot": return try handleScreenshot(params)
    default: try fail("unknown_method", "unknown method: \(method)")
    }
}

// MARK: - Main loop

let output = FileHandle.standardOutput

func write(_ object: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]) else { return }
    output.write(data)
    output.write(Data([0x0A]))
}

while let line = readLine() {
    if line.utf8.count > 1024 * 1024 {
        write(["id": 0, "ok": false, "error": ["code": "oversized_frame", "message": "request exceeds 1 MiB"]])
        exit(1)
    }
    guard let data = line.data(using: .utf8),
          let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
          let id = object["id"] as? Int else {
        write(["id": 0, "ok": false, "error": ["code": "invalid_json", "message": "request is not a JSON object with an id"]])
        exit(1)
    }
    do {
        let result = try dispatch(object)
        write(["id": id, "ok": true, "result": result])
    } catch let error as WorkerError {
        write(["id": id, "ok": false, "error": ["code": error.code, "message": error.message]])
    } catch {
        write(["id": id, "ok": false, "error": ["code": "internal", "message": String(describing: error)]])
    }
}
