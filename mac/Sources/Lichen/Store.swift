import AppKit
import LichenKit

/// What the window shows, as lichen last listed it, and the changes made
/// from it, each through the lichen command.
@MainActor
final class Store: ObservableObject {
    @Published var sources: [Source] = []
    @Published var devices = Devices()
    @Published var loaded = false
    /// Why there is nothing to show, when lichen couldn't list anything.
    @Published var problem: String?
    @Published var busy = false
    /// What went wrong with the last change.
    @Published var failure: String?

    let cli = CLI.find()

    func reload() async {
        guard let cli else { return }
        do {
            async let s = cli.sources()
            async let d = cli.devices()
            (sources, devices) = try await (s, d)
            problem = nil
        } catch {
            problem = error.localizedDescription
        }
        loaded = true
    }

    /// Runs one change, then shows what it made of things.
    func change(_ args: [String]) async {
        guard let cli else { return }
        busy = true
        defer { busy = false }
        do {
            _ = try await cli.run(args)
        } catch {
            failure = error.localizedDescription
        }
        await reload()
    }

    func add(_ urls: [URL]) async {
        await change(["files", "add"] + urls.map(\.path))
    }

    func addOutput(_ url: URL, to source: Source) async {
        await change(["files", "add", url.path, "--to", source.name])
    }

    /// Stops a source going to one place, or anywhere.
    func remove(_ target: String) async {
        await change(["files", "remove", target])
    }

    /// Scopes one output (by its path) or every output of a source (by its
    /// name) to devices and groups. None is every device.
    func scope(_ target: String, to only: [String]) async {
        await change(["files", "only", target] + only)
    }

    func sync() async {
        await change(["sync"])
    }

    func open(_ source: Source) {
        let url = URL(fileURLWithPath: source.path)
        if source.dir {
            NSWorkspace.shared.activateFileViewerSelecting([url])
        } else {
            NSWorkspace.shared.open(url)
        }
    }

    func reveal(_ path: String) {
        guard let cli else { return }
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: cli.expand(path))])
    }
}
