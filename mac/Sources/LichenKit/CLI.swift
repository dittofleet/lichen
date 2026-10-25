import Foundation

/// The lichen command, which does everything the app changes, so the app
/// and the command line can't disagree.
public struct CLI {
    public var binary: URL
    public var home: String
    public var environment = ProcessInfo.processInfo.environment

    public struct Failure: Error, LocalizedError {
        public var message: String
        public var errorDescription: String? { message }
    }

    /// Finds lichen as its daemon runs it: the same binary, with the
    /// environment it was installed with, as an app opened from the Dock
    /// has launchd's bare one. LICHEN_BIN points at another binary.
    public static func find() -> CLI? {
        var environment = ProcessInfo.processInfo.environment
        // lichen goes by HOME, which Foundation's home directory doesn't.
        let home = environment["HOME"] ?? NSHomeDirectory()
        var candidates = ["\(home)/.local/bin/lichen", "/opt/homebrew/bin/lichen", "/usr/local/bin/lichen"]
        if let data = try? Data(contentsOf: URL(fileURLWithPath: "\(home)/Library/LaunchAgents/dev.lichen.plist")),
           let daemon = try? PropertyListDecoder().decode(Daemon.self, from: data) {
            candidates.insert(contentsOf: daemon.ProgramArguments.prefix(1), at: 0)
            environment.merge(daemon.EnvironmentVariables ?? [:]) { $1 }
        }
        if let bin = environment["LICHEN_BIN"] {
            candidates.insert(bin, at: 0)
        }
        guard let path = candidates.first(where: FileManager.default.isExecutableFile(atPath:)) else {
            return nil
        }
        return CLI(binary: URL(fileURLWithPath: path), home: home, environment: environment)
    }

    /// What of the daemon's launchd job says how to run lichen.
    private struct Daemon: Decodable {
        var ProgramArguments: [String]
        var EnvironmentVariables: [String: String]?
    }

    /// Runs lichen, returning what it prints. A failure carries what it
    /// said went wrong.
    public func run(_ args: [String]) async throws -> Data {
        let binary = binary, environment = environment
        // Waiting on it blocks a thread, so not one of the few async ones.
        return try await withCheckedThrowingContinuation { done in
            DispatchQueue.global().async {
                done.resume(with: Result { try Self.run(binary, args, environment) })
            }
        }
    }

    private static func run(_ binary: URL, _ args: [String], _ environment: [String: String]) throws -> Data {
        let process = Process()
        process.executableURL = binary
        process.arguments = args
        var env = environment
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
        let said = text.split(separator: "\n").map { line in
            line.hasPrefix("lichen: ") ? String(line.dropFirst("lichen: ".count)) : String(line)
        }.joined(separator: "\n")
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
            throw Failure(message: "lichen answered in a way this app doesn't understand. Update lichen (lichen update) and this app to their latest.")
        }
    }

    /// A ~/-relative path, absolute.
    public func expand(_ path: String) -> String {
        path == "~" ? home : path.hasPrefix("~/") ? home + path.dropFirst(1) : path
    }
}
