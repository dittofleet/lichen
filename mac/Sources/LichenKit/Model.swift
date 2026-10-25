import Foundation

/// One source in the sync folder, and every place it goes, as
/// `lichen files --json` lists them.
public struct Source: Decodable, Identifiable {
    public var name: String
    /// Where it is in the sync folder, ~/-relative.
    public var path: String
    public var dir: Bool
    public var `private`: Bool
    public var outputs: [Output]

    public var id: String { name }
    public var issues: [Issue] { outputs.flatMap(\.issues) }
}

/// One place a source is written to.
public struct Output: Decodable, Identifiable {
    /// ~/-relative.
    public var path: String
    /// The devices and groups it is for. None is every device.
    public var only: [String]
    /// Whether it is for this device.
    public var here: Bool
    public var issues: [Issue]

    public var id: String { path }
}

/// A file at an output that needs the user.
public struct Issue: Decodable, Hashable {
    public enum Kind: String, Decodable {
        /// An edit lichen can't place on its own, until `lichen resolve`.
        case held
        /// Sources that don't compile, so it isn't synced.
        case broken
    }

    /// ~/-relative.
    public var path: String
    public var why: String
    public var kind: Kind
}

/// The devices lichen knows, and the groups of them, as
/// `lichen devices --json` lists them.
public struct Devices: Decodable {
    public struct Device: Decodable {
        public var name: String
        /// Whether it is this device.
        public var isSelf: Bool

        enum CodingKeys: String, CodingKey {
            case name
            case isSelf = "self"
        }
    }

    public var devices: [Device]
    public var groups: [String: [String]]

    public init(devices: [Device] = [], groups: [String: [String]] = [:]) {
        self.devices = devices
        self.groups = groups
    }

    /// The groups every device is in by its OS.
    public static let systems = ["mac", "linux"]

    /// Everything a scope can name, in the order to offer it.
    public var scopeChoices: [String] {
        Self.systems + groups.keys.sorted() + devices.map(\.name).sorted()
    }
}

/// How the app says which devices something is for.
public func describe(only: [String]) -> String {
    only.isEmpty ? "Every device" : only.joined(separator: ", ")
}
