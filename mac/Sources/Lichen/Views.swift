import AppKit
import LichenKit
import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var store: Store
    @State private var selection: Source.ID?
    @State private var search = ""

    private var shown: [Source] {
        search.isEmpty ? store.sources : store.sources.filter {
            $0.name.localizedCaseInsensitiveContains(search)
                || $0.outputs.contains { $0.path.localizedCaseInsensitiveContains(search) }
        }
    }

    var body: some View {
        NavigationSplitView {
            List(shown, selection: $selection) { source in
                SourceRow(source: source).tag(source.id)
            }
            .searchable(text: $search, placement: .sidebar, prompt: "Filter")
            .navigationSplitViewColumnWidth(min: 220, ideal: 260)
        } detail: {
            if store.cli == nil {
                Notice(symbol: "terminal", title: "lichen isn't installed") {
                    Text("Install it, then open this again:")
                    Text("curl -fsSL https://raw.githubusercontent.com/dittofleet/.github/main/install.sh | sh -s lichen")
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                }
            } else if let problem = store.problem {
                Notice(symbol: "exclamationmark.triangle", title: "lichen can't list your files") {
                    Text(problem).textSelection(.enabled)
                }
            } else if let source = store.sources.first(where: { $0.id == selection }) {
                SourceDetail(source: source)
            } else if store.loaded && store.sources.isEmpty {
                Notice(symbol: "doc.on.doc", title: "Nothing syncs yet") {
                    Button("Add Files…") { addFiles(store) }
                }
            } else if store.loaded {
                Notice(symbol: "sidebar.left", title: "Choose a file to see where it goes") {}
            }
        }
        .toolbar {
            ToolbarItemGroup {
                if store.busy {
                    ProgressView().controlSize(.small)
                }
                Button { Task { await store.sync() } } label: {
                    Label("Sync Now", systemImage: "arrow.triangle.2.circlepath")
                }
                .help("Sync Now")
                Button { addFiles(store) } label: {
                    Label("Add Files", systemImage: "plus")
                }
                .help("Add Files…")
            }
        }
        .disabled(store.cli == nil)
        .alert("lichen couldn't do that", isPresented: Binding(get: { store.failure != nil }, set: { if !$0 { store.failure = nil } })) {
            Button("OK") {}
        } message: {
            Text(store.failure ?? "")
        }
    }
}

struct SourceRow: View {
    let source: Source

    var body: some View {
        HStack {
            Image(systemName: source.dir ? "folder" : "doc.text")
                .foregroundStyle(.secondary)
                .frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(source.name)
                Text(source.outputs.map(\.path).joined(separator: ", "))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer()
            if !source.issues.isEmpty {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
            }
        }
        .padding(.vertical, 2)
    }
}

struct SourceDetail: View {
    @EnvironmentObject private var store: Store
    let source: Source
    @State private var stopping = false

    var body: some View {
        Form {
            Section {
                LabeledContent("In the sync folder") {
                    HStack {
                        Text(store.cli?.tilde(source.path) ?? source.path)
                            .font(.body.monospaced())
                            .textSelection(.enabled)
                        Button(source.dir ? "Show" : "Open") { store.open(source) }
                    }
                }
                if source.private {
                    Label("Only you can read it, on every device", systemImage: "lock")
                        .foregroundStyle(.secondary)
                }
            }
            Section("Goes to") {
                ForEach(source.outputs) { output in
                    OutputRow(source: source, output: output)
                }
                Button("Add a Place…") { choosePlace() }
            }
            Section {
                Button("Stop Syncing…", role: .destructive) { stopping = true }
            }
        }
        .formStyle(.grouped)
        .navigationTitle(source.name)
        .confirmationDialog("Stop syncing \(source.name)?", isPresented: $stopping) {
            Button("Stop Syncing", role: .destructive) { Task { await store.remove(source.name) } }
        } message: {
            Text("Every device keeps its copy as a plain file, which lichen no longer changes.")
        }
    }

    /// Asks where else the source should go.
    private func choosePlace() {
        let home = store.cli.map { URL(fileURLWithPath: $0.home) }
        let url: URL?
        if source.dir {
            let panel = NSOpenPanel()
            panel.canChooseFiles = false
            panel.canChooseDirectories = true
            panel.canCreateDirectories = true
            panel.showsHiddenFiles = true
            panel.directoryURL = home
            panel.prompt = "Sync Here"
            panel.message = "Choose a folder for \(source.name) to go to as well. It may only hold files \(source.name) has."
            url = panel.runModal() == .OK ? panel.url : nil
        } else {
            let panel = NSSavePanel()
            panel.nameFieldStringValue = (source.name as NSString).lastPathComponent
            panel.canCreateDirectories = true
            panel.showsHiddenFiles = true
            panel.directoryURL = home
            panel.prompt = "Sync Here"
            panel.message = "Choose where \(source.name) goes as well. A file already there is replaced by it."
            url = panel.runModal() == .OK ? panel.url : nil
        }
        if let url {
            Task { await store.addOutput(url, to: source) }
        }
    }
}

struct OutputRow: View {
    @EnvironmentObject private var store: Store
    let source: Source
    let output: Output
    @State private var removing = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(output.path)
                    .font(.body.monospaced())
                    .foregroundStyle(output.here ? .primary : .secondary)
                if !output.here {
                    Text("not on this device")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                ScopeButton(target: output.path, only: output.only)
                Menu {
                    Button("Show in Finder") { store.reveal(output.path) }
                        .disabled(!output.here)
                    Divider()
                    Button("Stop Going Here…", role: .destructive) { removing = true }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
            }
            ForEach(output.issues, id: \.self) { issue in
                IssueLabel(issue: issue, at: output.path)
            }
        }
        .confirmationDialog("Stop writing \(source.name) to \(output.path)?", isPresented: $removing) {
            Button("Stop", role: .destructive) { Task { await store.remove(output.path) } }
        } message: {
            if source.outputs.count == 1 {
                Text("It's the only place \(source.name) goes, so it stops syncing. Every device keeps its copy as a plain file.")
            } else {
                Text("The file there stays as it is.")
            }
        }
    }
}

struct IssueLabel: View {
    let issue: Issue
    let at: String

    var body: some View {
        Label {
            Text(.init(issue.message(at: at))).font(.callout)
        } icon: {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
        }
    }
}

struct ScopeButton: View {
    let target: String
    let only: [String]
    @State private var editing = false

    var body: some View {
        Button(describe(only: only)) { editing = true }
            .popover(isPresented: $editing, arrowEdge: .bottom) {
                ScopeEditor(target: target, only: only) { editing = false }
            }
    }
}

/// Picks the devices an output is for: every one, or some by their
/// system, a group of them, or their name.
struct ScopeEditor: View {
    @EnvironmentObject private var store: Store
    let target: String
    let only: [String]
    let done: () -> Void
    @State private var everywhere: Bool
    @State private var chosen: Set<String>

    init(target: String, only: [String], done: @escaping () -> Void) {
        self.target = target
        self.only = only
        self.done = done
        _everywhere = State(initialValue: only.isEmpty)
        _chosen = State(initialValue: Set(only))
    }

    private var choices: [String] {
        let known = store.devices.scopeChoices
        return known + only.filter { !known.contains($0) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Toggle("Every device", isOn: $everywhere)
                .toggleStyle(.switch)
            if !everywhere {
                Divider()
                group("Systems", ["mac", "linux"])
                group("Groups", store.devices.groups.keys.sorted())
                group("Devices", choices.filter { !["mac", "linux"].contains($0) && store.devices.groups[$0] == nil })
            }
            HStack {
                Spacer()
                Button("Cancel", action: done)
                    .keyboardShortcut(.cancelAction)
                Button("Done", action: apply)
                    .keyboardShortcut(.defaultAction)
                    .disabled(!everywhere && chosen.isEmpty)
            }
        }
        .padding()
        .frame(width: 280)
    }

    @ViewBuilder
    private func group(_ title: String, _ names: [String]) -> some View {
        if !names.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Text(title).font(.caption).foregroundStyle(.secondary)
                ForEach(names, id: \.self) { name in
                    Toggle(isOn: Binding(
                        get: { chosen.contains(name) },
                        set: { if $0 { chosen.insert(name) } else { chosen.remove(name) } }
                    )) {
                        HStack {
                            Text(name)
                            if let note = note(for: name) {
                                Text(note).foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }
        }
    }

    private func note(for name: String) -> String? {
        if let members = store.devices.groups[name] {
            return members.joined(separator: ", ")
        }
        return store.devices.devices.contains { $0.name == name && $0.isSelf } ? "this device" : nil
    }

    private func apply() {
        let new = everywhere ? [] : choices.filter(chosen.contains)
        if new != only {
            Task { await store.scope(target, to: new) }
        }
        done()
    }
}

/// A message filling the detail pane.
struct Notice<Content: View>: View {
    let symbol: String
    let title: String
    @ViewBuilder let content: Content

    var body: some View {
        VStack(spacing: 10) {
            Image(systemName: symbol)
                .font(.system(size: 36))
                .foregroundStyle(.secondary)
            Text(title).font(.title3)
            content
        }
        .multilineTextAlignment(.center)
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
