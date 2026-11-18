import FennecCore
import SwiftUI

/// Why the cloud computer can't be reached, in the app's own words. The Go
/// client tags its errors with a kind ("[network] EOF"); the raw message is
/// kept only as a detail behind a tap, never as the headline.
struct ConnectionIssue: Equatable {
    enum Kind: Equatable { case network, signedOut, cloudStopped, noComputer, other }

    var kind: Kind
    var detail: String
    /// When this run of failures began.
    var since: Date

    init(message: String, since: Date = .now) {
        kind = switch FennecmobileErrorKind(message) {
        case "network": .network
        case "signed_out": .signedOut
        case "cloud_stopped": .cloudStopped
        case "no_computer": .noComputer
        default: .other
        }
        detail = FennecmobileErrorDetail(message)
        self.since = since
    }

    init(_ error: Error, since: Date = .now) {
        self.init(message: (error as NSError).localizedDescription, since: since)
    }

    /// A dropped link only ever shows "Reconnecting…": reconnecting is
    /// Fennec's job, so there is nothing to explain or press.
    func reconnecting(at date: Date = .now) -> Bool { kind == .network }

    /// Headline for .other, where the context knows better (a conversation).
    var otherTitle: String?

    var title: String {
        switch kind {
        case .network: L10n.text("ui.reconnecting")
        case .signedOut: L10n.text("ios.sign_in_expired")
        case .cloudStopped: L10n.text("ios.cloud_stopped")
        case .noComputer: L10n.text("ios.no_cloud_computer")
        case .other: otherTitle ?? L10n.text("ios.something_wrong")
        }
    }

    var subtitle: String? {
        switch kind {
        case .cloudStopped: L10n.text("ios.cloud_stopped_detail")
        case .noComputer: L10n.text("ios.no_cloud_computer_detail")
        case .network, .signedOut, .other: nil
        }
    }
}

/// A small "Reconnecting…" capsule: what a dropped link looks like at first.
struct ReconnectingPill: View {
    var body: some View {
        HStack(spacing: 8) {
            ProgressView().controlSize(.small)
            Text(L10n.text("ui.reconnecting")).font(.system(size: 13, weight: .medium)).foregroundStyle(Theme.fg2)
        }
        .padding(.horizontal, 14).frame(height: 32)
        .glassEffect(.regular, in: Capsule())
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("reconnecting")
    }
}

/// The issue as a card: plain words and the raw error folded under Details.
/// It never offers to reconnect: Fennec keeps trying on its own.
struct ConnectionIssueCard: View {
    var issue: ConnectionIssue
    var onSignIn: () -> Void
    @State private var showDetail = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: issue.kind == .network ? "wifi.exclamationmark" : "exclamationmark.circle")
                    .foregroundStyle(issue.kind == .network ? Theme.fg2 : Theme.danger)
                VStack(alignment: .leading, spacing: 3) {
                    Text(issue.title).font(.system(size: 14, weight: .medium)).foregroundStyle(Theme.fg)
                    if let subtitle = issue.subtitle {
                        Text(subtitle).font(.system(size: 13)).foregroundStyle(Theme.fg2)
                    }
                }
                Spacer(minLength: 8)
                if issue.kind == .signedOut {
                    Button(L10n.text("ios.sign_in_again"), action: onSignIn).font(.system(size: 13, weight: .medium))
                } else if issue.kind == .network {
                    ProgressView().controlSize(.small)
                }
            }
            if !issue.detail.isEmpty {
                Button { withAnimation { showDetail.toggle() } } label: {
                    HStack(spacing: 4) {
                        Text(L10n.text("ios.error_details"))
                        Image(systemName: showDetail ? "chevron.up" : "chevron.down").font(.system(size: 10, weight: .semibold))
                    }
                    .font(.system(size: 12)).foregroundStyle(Theme.fg3)
                }
                .buttonStyle(.plain)
                .padding(.leading, 28)
                if showDetail {
                    Text(issue.detail).font(.system(size: 12, design: .monospaced)).foregroundStyle(Theme.fg3)
                        .textSelection(.enabled).padding(.leading, 28)
                }
            }
        }
        .padding(12)
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .accessibilityIdentifier("connection-issue")
    }
}

/// Shows nothing, the "Reconnecting…" capsule, or the card, as time passes.
struct ConnectionIssueBanner: View {
    var issue: ConnectionIssue?
    var onSignIn: () -> Void

    var body: some View {
        if let issue {
            TimelineView(.periodic(from: .now, by: 1)) { context in
                if issue.reconnecting(at: context.date) {
                    ReconnectingPill().frame(maxWidth: .infinity)
                } else {
                    ConnectionIssueCard(issue: issue, onSignIn: onSignIn)
                }
            }
        }
    }
}
