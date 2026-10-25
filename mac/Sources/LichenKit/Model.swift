import Foundation

/// One source in the sync folder, and every place it goes, as
/// `lichen files --json` lists them.
public struct Source: Codable, Identifiable, Hashable {
    public var name: String
    /// Where it is in the sync folder, absolute.
    public var path: String
    public var dir: Bool
    public var `private`: Bool
    public var outputs: [Output]

    public var id: String { name }
    public var issues: [Issue] { outputs.flatMap(\.issues) }

    enum CodingKeys: String, CodingKey { case name, path, dir, `private`, outputs }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        path = try c.decode(String.self, forKey: .path)
        dir = try c.decodeIfPresent(Bool.self, forKey: .dir) ?? false
        `private` = try c.decodeIfPresent(Bool.self, forKey: .private) ?? false
        outputs = try c.decode([Output].self, forKey: .outputs)
    }
}

/// One place a source is written to.
public struct Output: Codable, Identifiable, Hashable {
    /// ~/-relative.
    public var path: String
    /// The devices and groups it is for. None is every device.
    public var only: [String]
    /// Whether it is for this device.
    public var here: Bool
    public var issues: [Issue]

    public var id: String { path }

    enum CodingKeys: String, CodingKey { case path, only, here, issues }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        only = try c.decodeIfPresent([String].self, forKey: .only) ?? []
        here = try c.decode(Bool.self, forKey: .here)
        issues = try c.decodeIfPresent([Issue].self, forKey: .issues) ?? []
    }
}

/// A file at an output that needs the user.
public struct Issue: Codable, Hashable {
    public enum Kind: String, Codable {
        /// An edit lichen can't place on its own, until `lichen resolve`.
        case held
        /// Sources that don't compile, so it isn't synced.
        case broken
    }

    /// ~/-relative.
    public var path: String
    public var why: String
    public var kind: Kind

    /// What to tell the user about it, under the output at `output`.
    /// lichen words why as a clause to follow a path, so it becomes a
    /// sentence here, naming the file only when it is one inside.
    public func message(at output: String) -> String {
        let file = path == output ? "" : "\(path): "
        switch kind {
        case .held:
            var why = why.prefix(1).uppercased() + why.dropFirst()
            if !".?!".contains(why.last ?? ".") {
                why += "."
            }
            return "\(file)\(why) Settle it with `lichen resolve`."
        case .broken:
            return "\(file)Not synced, \(why)."
        }
    }
}

/// The devices lichen knows, and the groups of them, as
/// `lichen devices --json` lists them.
public struct Devices: Codable, Equatable {
    public struct Device: Codable, Hashable, Identifiable {
        public var name: String
        public var online: Bool
        /// Whether it is this device.
        public var isSelf: Bool
        /// The user's own groups it is in.
        public var groups: [String]

        public var id: String { name }

        enum CodingKeys: String, CodingKey {
            case name, online, groups
            case isSelf = "self"
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            name = try c.decode(String.self, forKey: .name)
            online = try c.decode(Bool.self, forKey: .online)
            isSelf = try c.decodeIfPresent(Bool.self, forKey: .isSelf) ?? false
            groups = try c.decode([String].self, forKey: .groups)
        }
    }

    public var devices: [Device]
    public var groups: [String: [String]]

    public init(devices: [Device] = [], groups: [String: [String]] = [:]) {
        self.devices = devices
        self.groups = groups
    }

    /// Everything a scope can name, in the order to offer it: the OS
    /// groups, the user's own groups, then the devices.
    public var scopeChoices: [String] {
        ["mac", "linux"] + groups.keys.sorted() + devices.map(\.name).sorted()
    }
}

/// How the app says which devices something is for.
public func describe(only: [String]) -> String {
    only.isEmpty ? "Every device" : only.joined(separator: ", ")
}
