import Foundation

struct HuginnChannel: Codable, Identifiable, Equatable {
    let sessionID: String
    let project: String
    let lastActiveEpoch: TimeInterval
    let lastLine: String

    var id: String { sessionID }

    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case project
        case lastActiveEpoch = "last_active_epoch"
        case lastLine = "last_line"
    }
}

struct SpokenLine: Codable, Identifiable, Equatable {
    let id: String
    let sessionID: String
    let project: String
    let text: String
    let spokenAtEpoch: TimeInterval
    private let roleRaw: String?

    // Older transcript entries predate the role field — treat them as Claude.
    var isUser: Bool { roleRaw == "user" }

    enum CodingKeys: String, CodingKey {
        case id
        case sessionID = "session_id"
        case project
        case text
        case spokenAtEpoch = "spoken_at_epoch"
        case roleRaw = "role"
    }
}

private struct Selection: Codable {
    let mode: String
    let sessionID: String?

    enum CodingKeys: String, CodingKey {
        case mode
        case sessionID = "session_id"
    }
}

private struct ChannelsResponse: Codable {
    let channels: [HuginnChannel]
    let selection: Selection
}

private struct TranscriptResponse: Codable {
    let lines: [SpokenLine]
}

@MainActor
final class HuginnAPI: ObservableObject {
    @Published private(set) var channels: [HuginnChannel] = []
    @Published private(set) var transcript: [SpokenLine] = []
    @Published private(set) var selectionMode = "follow"
    @Published private(set) var selectedSessionID: String?
    @Published private(set) var errorText: String?

    private let baseURL = URL(string: "http://100.64.0.1:8080")!
    private let session: URLSession
    private var etags: [String: String] = [:]

    init() {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 4
        configuration.timeoutIntervalForResource = 6
        session = URLSession(configuration: configuration)
    }

    var currentChannel: HuginnChannel? {
        guard let selectedSessionID else { return nil }
        return channels.first { $0.sessionID == selectedSessionID }
    }

    var channelLabel: String {
        let project = currentChannel?.project
        if selectionMode == "follow" {
            return project.map { "Following · \($0)" } ?? "Following active session"
        }
        return project ?? "Pinned session"
    }

    func refreshChannels() async {
        do {
            guard let data = try await get(path: "/channels") else { return }
            let response = try JSONDecoder().decode(ChannelsResponse.self, from: data)
            channels = response.channels
            selectionMode = response.selection.mode
            selectedSessionID = response.selection.sessionID
            errorText = nil
        } catch {
            errorText = "Channels unavailable"
        }
    }

    func refreshTranscript() async {
        do {
            guard let data = try await get(path: "/transcript?limit=50") else { return }
            transcript = try JSONDecoder().decode(TranscriptResponse.self, from: data).lines
            errorText = nil
        } catch {
            errorText = "Transcript unavailable"
        }
    }

    func pin(_ channel: HuginnChannel) async {
        await setSelection(mode: "pinned", sessionID: channel.sessionID)
    }

    func followActiveSession() async {
        await setSelection(mode: "follow", sessionID: nil)
    }

    private func setSelection(mode: String, sessionID: String?) async {
        var request = URLRequest(url: baseURL.appendingPathComponent("active"))
        request.httpMethod = "POST"
        request.timeoutInterval = 4
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONEncoder().encode(Selection(mode: mode, sessionID: sessionID))

        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            let selection = try JSONDecoder().decode(Selection.self, from: data)
            selectionMode = selection.mode
            selectedSessionID = selection.sessionID
            etags.removeValue(forKey: "/channels")
            errorText = nil
            await refreshChannels()
        } catch {
            errorText = "Could not change channel"
        }
    }

    private let logURL: URL = FileManager.default
        .urls(for: .documentDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("EarPlayback.log")

    /// Ship only new log bytes (since a persisted offset) to the Mac, so both
    /// sides of the pipeline land in one place for `diagnose.py`.
    func uploadLog() async {
        let key = "logUploadOffset"
        let offset = UInt64(max(0, UserDefaults.standard.integer(forKey: key)))
        guard let handle = try? FileHandle(forReadingFrom: logURL) else { return }
        defer { try? handle.close() }
        do {
            try handle.seek(toOffset: offset)
            let data = try handle.readToEnd() ?? Data()
            guard !data.isEmpty else { return }
            let lines = String(decoding: data, as: UTF8.self)
                .split(separator: "\n", omittingEmptySubsequences: true)
                .map(String.init)
            guard !lines.isEmpty else { return }
            var request = URLRequest(url: baseURL.appendingPathComponent("log"))
            request.httpMethod = "POST"
            request.timeoutInterval = 5
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            let payload: [String: Any] = ["device": "iphone", "lines": Array(lines.suffix(500))]
            request.httpBody = try JSONSerialization.data(withJSONObject: payload)
            let (_, response) = try await session.data(for: request)
            if let http = response as? HTTPURLResponse, http.statusCode == 200 {
                UserDefaults.standard.set(Int(offset + UInt64(data.count)), forKey: key)
            }
        } catch {}
    }

    private func get(path: String) async throws -> Data? {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw URLError(.badURL)
        }
        var request = URLRequest(url: url)
        request.cachePolicy = .reloadIgnoringLocalCacheData
        if let etag = etags[path] {
            request.setValue(etag, forHTTPHeaderField: "If-None-Match")
        }

        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw URLError(.badServerResponse)
        }
        if http.statusCode == 304 { return nil }
        guard http.statusCode == 200 else { throw URLError(.badServerResponse) }
        if let etag = http.value(forHTTPHeaderField: "ETag") {
            etags[path] = etag
        }
        return data
    }
}
