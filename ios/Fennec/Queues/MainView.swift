import SwiftUI

/// Home: the queue and its threads, nothing else (FEN-105, FEN-108). A
/// terminal session is reached only from inside a thread (its live card),
/// the same two levels as the desktop app: queue → thread.
struct MainView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        QueuesView().environment(app.queues)
    }
}

enum QueueSettings {
    static let notifyKey = "notifyRequestNeedsYou"
}
