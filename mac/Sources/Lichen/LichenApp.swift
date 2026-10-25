import AppKit
import SwiftUI

@main
struct LichenApp: App {
    @StateObject private var store = Store()

    var body: some Scene {
        // One window, and closing it quits.
        Window("lichen", id: "main") {
            ContentView()
                .environmentObject(store)
                .frame(minWidth: 720, minHeight: 420)
                .task { await store.reload() }
                // What another device synced shows up when you come back.
                // Not at launch, which .task covers, nor during a change,
                // which reloads when it is done.
                .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
                    if store.loaded && !store.busy {
                        Task { await store.reload() }
                    }
                }
        }
        .commands {
            CommandMenu("Files") {
                Group {
                    Button("Add Files…") { addFiles(store) }
                        .keyboardShortcut("o")
                    Button("Sync Now") { Task { await store.sync() } }
                        .keyboardShortcut("s", modifiers: [.command, .shift])
                    Button("Reload") { Task { await store.reload() } }
                        .keyboardShortcut("r")
                }
                .disabled(store.cli == nil)
            }
        }
    }
}

/// Asks for files or directories under the home directory, and syncs them.
@MainActor
func addFiles(_ store: Store) {
    let panel = NSOpenPanel()
    panel.canChooseFiles = true
    panel.canChooseDirectories = true
    panel.allowsMultipleSelection = true
    panel.showsHiddenFiles = true
    panel.prompt = "Sync"
    panel.message = "Choose files or folders in your home folder to sync to your other devices."
    if let home = store.cli?.home {
        panel.directoryURL = URL(fileURLWithPath: home)
    }
    guard panel.runModal() == .OK, !panel.urls.isEmpty else { return }
    let urls = panel.urls
    Task { await store.add(urls) }
}
