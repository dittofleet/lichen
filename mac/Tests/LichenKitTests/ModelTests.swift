import XCTest
@testable import LichenKit

final class ModelTests: XCTestCase {
    func testDecodesWhatLichenFilesLists() throws {
        let json = """
        [
          {"name": "zshrc", "path": "/Users/x/lichen/files/zshrc", "outputs": [
            {"path": "~/.zshrc", "here": true,
             "issues": [{"path": "~/.zshrc", "why": "which source do the new lines go in?", "kind": "held"}]},
            {"path": "~/.bashrc", "only": ["linux", "servers"], "here": false}
          ]},
          {"name": "claude/commands", "path": "/Users/x/lichen/files/claude/commands", "dir": true, "private": true, "outputs": []}
        ]
        """
        let sources = try JSONDecoder().decode([Source].self, from: Data(json.utf8))
        XCTAssertEqual(sources.map(\.name), ["zshrc", "claude/commands"])
        XCTAssertEqual(sources[0].outputs[1].only, ["linux", "servers"])
        XCTAssertEqual(sources[0].outputs[0].only, [])
        XCTAssertEqual(sources[0].issues.map(\.kind), [.held])
        XCTAssertTrue(sources[1].dir)
        XCTAssertTrue(sources[1].private)
        XCTAssertFalse(sources[0].dir)
    }

    func testDecodesDevicesAndOrdersChoices() throws {
        let json = """
        {"devices": [
          {"name": "lychee", "online": true, "self": true, "groups": []},
          {"name": "hetzner-1", "online": false, "groups": ["servers"]}
        ], "groups": {"servers": ["hetzner-1"], "home": ["lychee"]}}
        """
        let d = try JSONDecoder().decode(Devices.self, from: Data(json.utf8))
        XCTAssertEqual(d.devices.map(\.isSelf), [true, false])
        XCTAssertEqual(d.scopeChoices, ["mac", "linux", "home", "servers", "hetzner-1", "lychee"])
    }

    func testDescribesScopes() {
        XCTAssertEqual(describe(only: []), "Every device")
        XCTAssertEqual(describe(only: ["mac", "servers"]), "mac, servers")
    }

    func testIssuesReadAsSentences() {
        let held = Issue(path: "~/.zshrc", why: "edited here, and it no longer inserts zsh/linux", kind: .held)
        XCTAssertEqual(held.message(at: "~/.zshrc"), "Edited here, and it no longer inserts zsh/linux. Settle it with `lichen resolve`.")
        let asked = Issue(path: "~/.claude/commands/a.md", why: "which source do the new lines go in?", kind: .held)
        XCTAssertEqual(asked.message(at: "~/.claude/commands"), "~/.claude/commands/a.md: Which source do the new lines go in? Settle it with `lichen resolve`.")
        let broken = Issue(path: "~/.zshrc", why: "zshrc inserts itself", kind: .broken)
        XCTAssertEqual(broken.message(at: "~/.zshrc"), "Not synced, zshrc inserts itself.")
    }

    func testTildePaths() {
        let cli = CLI(binary: URL(fileURLWithPath: "/bin/true"), home: "/Users/x")
        XCTAssertEqual(cli.tilde("/Users/x/.zshrc"), "~/.zshrc")
        XCTAssertEqual(cli.tilde("/Users/xy/.zshrc"), "/Users/xy/.zshrc")
        XCTAssertEqual(cli.expand("~/.zshrc"), "/Users/x/.zshrc")
        XCTAssertEqual(cli.expand("~"), "/Users/x")
    }

    func testFailuresSayWhatLichenSaid() {
        let said = CLI.message(Data("waiting for the lichen daemon to finish its current sync...\nlichen: ~/.zshrc already syncs, through zshrc\n".utf8), Data())
        XCTAssertEqual(said, "~/.zshrc already syncs, through zshrc")
    }

    func testRunsLichenAndReportsFailures() async throws {
        let ok = CLI(binary: URL(fileURLWithPath: "/bin/echo"), home: "/")
        let out = try await ok.run(["hi"])
        XCTAssertEqual(String(decoding: out, as: UTF8.self), "hi\n")
        let failing = CLI(binary: URL(fileURLWithPath: "/bin/sh"), home: "/")
        do {
            _ = try await failing.run(["-c", "echo 'lichen: no' >&2; exit 1"])
            XCTFail("a failing run succeeded")
        } catch let f as CLI.Failure {
            XCTAssertEqual(f.message, "no")
        }
    }
}
