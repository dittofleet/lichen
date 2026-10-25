import Foundation

/// The lichen command, which does everything the app changes, so the app
/// and the command line can't disagree.
public struct CLI {
    public var binary: URL
    public var home: String

    public struct Failure: Error, LocalizedError {
        public var message: String
        public var errorDescription: String? { message }
    }

    /// Finds lichen where it gets installed. LICHEN_BIN points at another.
    public static func find(environment: [String: String] = ProcessInfo.processInfo.environment) -> CLI? {
        let home = environment["HOME"] ?? NSHomeDirectory()
        var candidates = ["\(home)/.local/bin/lichen", "/opt/homebrew/bin/lichen", "/usr/local/bin/lichen"]
        if let bin = environment["LICHEN_BIN"] {
            candidates.insert(bin, at: 0)
        }
        guard let path = candidates.first(where: FileManager.default.isExecutableFile(atPath:)) else {
            return nil
        }
        return CLI(binary: URL(fileURLWithPath: path), home: home)
    }

    /// Runs lichen, returning what it prints. A failure carries what it
    /// said went wrong.
    public func run(_ args: [String]) async throws -> Data {
        let binary = binary
        return try await Task.detached { try Self.run(binary, args) }.value
    }

    private static func run(_ binary: URL, _ args: [String]) throws -> Data {
        let process = Process()
        process.executableURL = binary
        process.arguments = args
        var env = ProcessInfo.processInfo.environment
        env["NO_COLOR"] = "1"
        process.environment = env
        let out = Pipe(), err = Pipe()
        process.standardOutput = out
        process.standardError = err
        process.standardInput = FileHandle.nullDevice
        do {
            try process.run()
        } catch {
            throw Failure(message: "Couldn't run \(binary.path): \(error.localizedDescription)")
        }
        // Both are read while it runs: a full pipe would stop it.
        let stderr = Box()
        let reading = DispatchGroup()
        reading.enter()
        DispatchQueue.global().async {
            stderr.data = err.fileHandleForReading.readDataToEndOfFile()
            reading.leave()
        }
        let stdout = out.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        reading.wait()
        guard process.terminationStatus == 0 else {
            throw Failure(message: message(stderr.data, stdout))
        }
        return stdout
    }

    private final class Box: @unchecked Sendable {
        var data = Data()
    }

    /// What a failed run said, without lichen's own name in front.
    static func message(_ stderr: Data, _ stdout: Data) -> String {
        let text = String(decoding: stderr.isEmpty ? stdout : stderr, as: UTF8.self)
        let lines = text.split(separator: "\n").map { line -> String in
            line.hasPrefix("lichen: ") ? String(line.dropFirst("lichen: ".count)) : String(line)
        }
        // The lock wait notice isn't the error.
        let said = lines.filter { !$0.hasPrefix("waiting for the lichen daemon") }.joined(separator: "\n")
        return said.isEmpty ? "lichen failed" : said
    }

    public func sources() async throws -> [Source] {
        try decode(await run(["files", "--json"]))
    }

    public func devices() async throws -> Devices {
        try decode(await run(["devices", "--json"]))
    }

    private func decode<T: Decodable>(_ data: Data) throws -> T {
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw Failure(message: "lichen answered in a way this app doesn't understand. Update both to the latest (lichen update).")
        }
    }

    /// An absolute path as lichen shows it, ~/-relative.
    public func tilde(_ path: String) -> String {
        path == home ? "~" : path.hasPrefix(home + "/") ? "~" + path.dropFirst(home.count) : path
    }

    /// A ~/-relative path, absolute.
    public func expand(_ path: String) -> String {
        path == "~" ? home : path.hasPrefix("~/") ? home + path.dropFirst(1) : path
    }
}
