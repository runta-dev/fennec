import FennecCore
import SwiftUI

/// The Mac panel's Account tab on the phone: the cloud computer's live
/// meters (sampled over the phone's own SSH link) and Runta billing.
@MainActor
@Observable
final class AccountUsage {
    /// Every field optional: Go leaves out empty ones (omitempty), and a
    /// missing key would fail the whole decode.
    struct Metrics: Decodable {
        var cpu_percent: Double?
        var vcpus: Int?
        var memory_used_bytes: Double?
        var memory_used_mib: Double?
        var memory_max_mib: Double?
        var disk_used_bytes: Double?
        var disk_total_bytes: Double?
        var net_rx_bytes: Double?
        var net_tx_bytes: Double?
        var cpu_history: [Double]?
        var mem_history: [Double]?

        var memoryBytes: Double { memory_used_bytes ?? (memory_used_mib ?? 0) * 1_048_576 }
    }

    struct Billing: Decodable {
        var currency: String?
        var balance: Int64?
        var spend: Int64?
        var cpu: String?
        var memory: String?
        var disk: String?
        var web_only: Bool?
        var reauth_required: Bool?
    }

    private(set) var metrics: Metrics?
    private(set) var billing: Billing?
    private(set) var billingFailed = false

    func loadBilling(_ core: FennecmobileAuth) async {
        let json = await Task.detached { try? goCall { core.billing($0) } }.value
        if let data = json?.data(using: .utf8), let value = try? JSONDecoder().decode(Billing.self, from: data) {
            billing = value
            billingFailed = false
        } else if billing == nil {
            billingFailed = true
        }
    }

    /// Example meters and balance for the demo (ReviewDemo) and previews.
    func showDemo() {
        let cpu: [Double] = [8, 12, 31, 18, 9, 22, 41, 27, 14, 11, 19, 24]
        metrics = Metrics(cpu_percent: 14.2, vcpus: 4, memory_used_bytes: 2.4 * 1_073_741_824, memory_max_mib: 8192,
                          disk_used_bytes: 18.6 * 1_073_741_824, disk_total_bytes: 64 * 1_073_741_824,
                          net_rx_bytes: 1.31 * 1_073_741_824, net_tx_bytes: 286 * 1_048_576,
                          cpu_history: cpu, mem_history: cpu.map { 26 + $0 / 4 })
        billing = Billing(currency: "USD", balance: 2500, spend: 418, cpu: "61.40 vCPU-h", memory: "122.80 GiB-h", disk: "1,190.40 GiB-h")
        billingFailed = false
    }

    /// Samples every two seconds until the page goes away (the task is cancelled).
    func watchMetrics(_ core: FennecmobileSessions) async {
        while !Task.isCancelled {
            let json = await Task.detached { try? goCall { core.metrics($0) } }.value
            if let data = json?.data(using: .utf8), let value = try? JSONDecoder().decode(Metrics.self, from: data) {
                metrics = value
            }
            try? await Task.sleep(for: .seconds(2))
        }
    }
}

private func gigabytes(_ bytes: Double) -> String {
    bytes >= 1_073_741_824 ? String(format: "%.1f GB", bytes / 1_073_741_824) : String(format: "%.0f MB", bytes / 1_048_576)
}

private func traffic(_ bytes: Double) -> String {
    if bytes >= 1_073_741_824 { return String(format: "%.2f GB", bytes / 1_073_741_824) }
    if bytes >= 1_048_576 { return String(format: "%.1f MB", bytes / 1_048_576) }
    return String(format: "%.0f KB", bytes / 1024)
}

struct CloudComputerCard: View {
    var metrics: AccountUsage.Metrics?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 10) {
                Image(systemName: "cloud").foregroundStyle(Theme.fg2)
                Text(L10n.text("ui.cloud_computer")).font(.system(size: 15.5, weight: .medium))
                Spacer(minLength: 8)
                if let metrics, let vcpus = metrics.vcpus, vcpus > 0 {
                    Text((metrics.memory_max_mib ?? 0) > 0
                         ? "\(vcpus) vCPU · \(Int(((metrics.memory_max_mib ?? 0) / 1024).rounded())) GiB"
                         : "\(vcpus) vCPU")
                        .font(.system(size: 12.5)).foregroundStyle(Theme.fg2).monospacedDigit()
                        .padding(.horizontal, 8).padding(.vertical, 3)
                        .background(Theme.surface2, in: Capsule())
                }
            }
            Divider().overlay(Theme.line)
            HStack(alignment: .top, spacing: 16) {
                meter(icon: "cpu", title: "CPU", value: metrics.map { String(format: "%.1f%%", $0.cpu_percent ?? 0) },
                      history: metrics?.cpu_history ?? [])
                meter(icon: "memorychip", title: L10n.text("ui.memory"), value: metrics.map { gigabytes($0.memoryBytes) },
                      history: metrics?.mem_history ?? [])
            }
            HStack(spacing: 10) {
                Label(L10n.text("ui.network"), systemImage: "globe").foregroundStyle(Theme.fg2)
                Spacer(minLength: 8)
                if let metrics {
                    Text("↑ \(traffic(metrics.net_tx_bytes ?? 0))   ↓ \(traffic(metrics.net_rx_bytes ?? 0))").monospacedDigit()
                } else {
                    SkeletonBar(width: 120, height: 10)
                }
            }
            .font(.system(size: 13.5))
            VStack(spacing: 8) {
                HStack {
                    Label(L10n.text("ui.disk"), systemImage: "internaldrive").foregroundStyle(Theme.fg2)
                    Spacer(minLength: 8)
                    if let metrics, let total = metrics.disk_total_bytes, total > 0 {
                        Text("\(gigabytes(metrics.disk_used_bytes ?? 0)) / \(gigabytes(total))").monospacedDigit()
                    } else {
                        SkeletonBar(width: 110, height: 10)
                    }
                }
                .font(.system(size: 13.5))
                GeometryReader { geometry in
                    let fraction = metrics.flatMap { m in m.disk_total_bytes.map { $0 > 0 ? min(1, (m.disk_used_bytes ?? 0) / $0) : 0 } } ?? 0
                    ZStack(alignment: .leading) {
                        Capsule().fill(Theme.accent.opacity(0.15))
                        Capsule().fill(Theme.accent).frame(width: geometry.size.width * fraction)
                    }
                }
                .frame(height: 3)
            }
        }
        .padding(16)
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).strokeBorder(Theme.line))
    }

    private func meter(icon: String, title: String, value: String?, history: [Double]) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Label(title, systemImage: icon).foregroundStyle(Theme.fg2)
                Spacer(minLength: 4)
                if let value { Text(value).monospacedDigit() } else { SkeletonBar(width: 44, height: 10) }
            }
            .font(.system(size: 13.5))
            Sparkline(values: history).frame(height: 28)
        }
        .frame(maxWidth: .infinity)
    }
}

/// The Mac panel's orange line with a soft fill, scaled to its own range.
private struct Sparkline: View {
    var values: [Double]

    var body: some View {
        GeometryReader { geometry in
            if values.count > 1 {
                let low = values.min() ?? 0, high = values.max() ?? 1
                let span = max(high - low, max(abs(high) * 0.1, 0.5))
                let points = values.enumerated().map { index, value in
                    CGPoint(x: geometry.size.width * CGFloat(index) / CGFloat(values.count - 1),
                            y: geometry.size.height * (1 - CGFloat((value - low) / span) * 0.85) - 1)
                }
                let line = Path { path in path.addLines(points) }
                Path { path in
                    path.addLines(points)
                    path.addLine(to: CGPoint(x: geometry.size.width, y: geometry.size.height))
                    path.addLine(to: CGPoint(x: 0, y: geometry.size.height))
                    path.closeSubpath()
                }
                .fill(LinearGradient(colors: [Theme.accent.opacity(0.18), Theme.accent.opacity(0)], startPoint: .top, endPoint: .bottom))
                line.stroke(Theme.accent, style: StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round))
            } else {
                Rectangle().fill(Theme.line).frame(height: 1).frame(maxHeight: .infinity, alignment: .bottom)
            }
        }
    }
}

struct BillingCard: View {
    var billing: AccountUsage.Billing?
    var failed: Bool
    var openDashboard: () -> Void

    private func money(_ minor: Int64?) -> String? {
        guard let minor else { return nil }
        let code = billing?.currency ?? "USD"
        // "$26.11" as on the Mac, not "US$26.11" under a non-US region.
        return (Double(minor) / 100).formatted(.currency(code: code).locale(Locale(identifier: code == "USD" ? "en_US" : Locale.current.identifier)))
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                // The email is already in the page header; this row names the plan.
                Image(systemName: "creditcard").foregroundStyle(Theme.fg2).frame(width: 20)
                Text(L10n.text("ios.billing")).font(.system(size: 15.5, weight: .medium))
                Spacer(minLength: 8)
                Text(L10n.text("ui.pay_as_you_go")).font(.system(size: 12.5, weight: .medium)).foregroundStyle(Theme.accent)
                    .padding(.horizontal, 8).padding(.vertical, 3)
                    .background(Theme.accent.opacity(0.12), in: Capsule())
            }
            .padding(.horizontal, 16).padding(.vertical, 13)
            if billing?.web_only == true || billing?.reauth_required == true || (failed && billing == nil) {
                Divider().overlay(Theme.line)
                Button(action: openDashboard) {
                    HStack {
                        Text(L10n.text("ios.billing")).foregroundStyle(Theme.fg2)
                        Spacer()
                        Text(L10n.text("ios.open_dashboard")).foregroundStyle(Theme.accent)
                        Image(systemName: "arrow.up.right").font(.system(size: 12)).foregroundStyle(Theme.accent)
                    }
                    .font(.system(size: 15)).padding(.horizontal, 16).padding(.vertical, 13)
                }
                .buttonStyle(.plain)
            } else {
                row(icon: "wallet.bifold", title: L10n.text("ui.available_balance"), value: money(billing?.balance))
                row(icon: "chart.line.uptrend.xyaxis", title: L10n.text("ui.last_30_days_spend"), value: money(billing?.spend))
                Divider().overlay(Theme.line)
                HStack(spacing: 0) {
                    usage(icon: "cpu", title: "CPU", value: billing?.cpu)
                    Divider().overlay(Theme.line)
                    usage(icon: "memorychip", title: L10n.text("ui.memory"), value: billing?.memory)
                    Divider().overlay(Theme.line)
                    usage(icon: "internaldrive", title: L10n.text("ui.disk"), value: billing?.disk)
                }
                .fixedSize(horizontal: false, vertical: true)
            }
        }
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).strokeBorder(Theme.line))
    }

    private func row(icon: String, title: String, value: String?) -> some View {
        VStack(spacing: 0) {
            Divider().overlay(Theme.line)
            HStack(spacing: 12) {
                Image(systemName: icon).foregroundStyle(Theme.fg2).frame(width: 20)
                Text(title)
                Spacer(minLength: 8)
                if let value { Text(value).monospacedDigit() } else { SkeletonBar(width: 56, height: 10) }
            }
            .font(.system(size: 15)).padding(.horizontal, 16).padding(.vertical, 13)
        }
    }

    private func usage(icon: String, title: String, value: String?) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(title, systemImage: icon).font(.system(size: 13)).foregroundStyle(Theme.fg2)
            if let value {
                Text(value).font(.system(size: 13.5)).monospacedDigit().lineLimit(1).minimumScaleFactor(0.8)
            } else {
                SkeletonBar(width: 60, height: 10)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12).padding(.vertical, 12)
    }
}
