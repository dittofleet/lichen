import XCTest
@testable import LichenKit

final class ModelTests: XCTestCase {
    func testDecodesWhatLichenFilesLists() throws {
        let json = """
        [
          {"name": "zshrc", "path": "~/lichen/files/zshrc", "dir": false, "private": false, "outputs": [
            {"path": "~/.zshrc", "only": [], "here": true,
             "issues": [{"path": "~/.zshrc", "why": "which source do the new lines go in?", "kind": "held"}]},
            {"path": "~/.bashrc", "only": ["linux", "servers"], "here": false, "issues": []}
          ]},
          {"name": "claude/commands", "path": "~/lichen/files/claude/commands", "dir": true, "private": true, "outputs": []}
        ]
        """
        let sources = try JSONDecoder().decode([Source].self, from: Data(json.utf8))
        XCTAssertEqual(sources.map(\.name), ["zshrc", "claude/commands"])
        XCTAssertEqual(sources[0].outputs[1].only, ["linux", "servers"])
        XCTAssertEqual(sources[0].issues.map(\.kind), [.held])
        XCTAssertTrue(sources[1].dir)
        XCTAssertTrue(sources[1].private)
    }

    func testDecodesDevicesAndOrdersChoices() throws {
        let json = """
        {"devices": [
          {"name": "lychee", "online": true, "self": true},
          {"name": "hetzner-1", "online": false, "self": false}
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

    func testExpandsPaths() {
        let cli = CLI(binary: URL(fileURLWithPath: "/bin/true"), home: "/Users/x")
        XCTAssertEqual(cli.expand("~/.zshrc"), "/Users/x/.zshrc")
        XCTAssertEqual(cli.expand("~"), "/Users/x")
        XCTAssertEqual(cli.expand("/etc/hosts"), "/etc/hosts")
    }

    func testFailuresSayWhatLichenSaid() {
        let said = CLI.message(Data("lichen: ~/.zshrc already syncs, through zshrc\n".utf8), Data())
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
