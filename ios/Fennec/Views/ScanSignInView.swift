import AVFoundation
import CoreImage
import PhotosUI
import SwiftUI

/// Native sign-in choices, with QR pairing available on demand.
struct ScanSignInView: View {
    @Environment(RuntaAuth.self) private var auth
    @Environment(AppModel.self) private var app
    @State private var web = WebSignIn()
    @State private var photo: PhotosPickerItem?
    @State private var notice: String?
    @State private var camera = CameraAccess.unknown
    @State private var mode = "choices"
    @State private var email = ""
    @State private var code = ""
    @State private var resendAt = Date.distantPast

    var body: some View {
        ZStack {
            (mode == "scan" ? Color.black : Theme.bg).ignoresSafeArea()
            if mode == "scan", camera == .granted {
                QRScannerView { scanned($0) }.ignoresSafeArea()
            }
            if mode == "scan" { Reticle().frame(width: 220, height: 220).offset(y: -40) }
            VStack(spacing: 0) {
                topBar
                if let notice = notice ?? failure {
                    Label(notice, systemImage: "exclamationmark.circle")
                        .font(.system(size: 14, weight: .medium))
                        .padding(.horizontal, 14).frame(height: 36)
                        .glassEffect(.regular, in: Capsule())
                        .padding(.top, 10)
                        .transition(.opacity)
                }
                Spacer()
                if auth.pairing && auth.isAuthorizing { waitingPanel }
                else if mode == "scan" { scanPanel }
                else {
                    Image("fox").resizable().scaledToFit().frame(width: 72, height: 72)
                    Text(L10n.text("ios.welcome"))
                        .font(.system(size: 28, weight: .medium)).padding(.top, 20)
                    Spacer()
                    if mode == "email" { emailPanel } else { choicesPanel }
                    Spacer().frame(height: 48)
                }
            }
            .foregroundStyle(mode == "scan" ? .white : Theme.fg)
        }
        .task(id: mode) { if mode == "scan" { camera = await CameraAccess.request() } }
        .onChange(of: photo) { _, item in Task { await importPhoto(item) } }
        .onChange(of: auth.verificationURL) { _, url in
            if let url {
                let epoch = auth.epoch
                web.start(url, onCallback: { auth.handleCallback($0, epoch: epoch) }, onCancel: { auth.cancelSignIn() })
            } else { web.finish() }
        }
        .onDisappear { web.finish() }
    }

    private var failure: String? {
        if case .failed(let message) = auth.status { return message }
        return nil
    }

    private var topBar: some View {
        ZStack {
            HStack(spacing: 8) {
                FoxMark(size: 26).environmentSwitchGesture()
                Text("Fennec").font(.system(size: 17, weight: .semibold))
                EnvironmentBadge(environment: app.environment)
            }
            HStack {
                if mode != "choices" {
                    Button { auth.cancelSignIn(); mode = "choices"; code = "" } label: {
                        Image(systemName: "chevron.left").frame(width: 44, height: 44)
                    }.accessibilityLabel(L10n.text("ios.back"))
                }
                Spacer()
                if mode == "scan" { PhotosPicker(selection: $photo, matching: .images) {
                    Image(systemName: "photo.on.rectangle").font(.system(size: 17, weight: .medium))
                        .frame(width: 44, height: 44)
                }
                .glassEffect(.regular.interactive(), in: Circle())
                .accessibilityLabel(L10n.text("ios.choose_photo"))
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 8)
    }

    private var scanPanel: some View {
        VStack(spacing: 10) {
            Text(L10n.text("ios.scan_mac")).font(.system(size: 18, weight: .semibold)).lineLimit(1)
            Label(L10n.text("ui.get_fennec_ios_app"), systemImage: "laptopcomputer")
                .font(.system(size: 13)).padding(.horizontal, 11).frame(height: 28)
                .background(.white.opacity(0.1), in: Capsule())
            if camera == .denied {
                Button(L10n.text("ios.camera_settings")) {
                    if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                }
                .font(.system(size: 14, weight: .medium))
            }
            if auth.isAuthorizing {
                Button(L10n.text("ui.cancel_sign_in")) { auth.cancelSignIn() }
                    .font(.system(size: 14, weight: .medium))
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 20).padding(.bottom, 24)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 30, style: .continuous))
        .padding(.horizontal, 10)
    }

    private var choicesPanel: some View {
        VStack(spacing: 12) {
            loginButton(L10n.text("ios.google_signin"), asset: "auth-google", method: "google")
            loginButton(L10n.text("ios.github_signin"), asset: "auth-github", method: "github")
            Button { mode = "scan" } label: { loginLabel(L10n.text("ios.scan"), symbol: "qrcode.viewfinder") }
                .disabled(auth.isAuthorizing)
            Button { mode = "email" } label: {
                Label(L10n.text("ios.email_signin"), systemImage: "envelope")
                    .font(.system(size: 15, weight: .medium))
                    .foregroundStyle(Theme.fg2)
                    .frame(minHeight: 44)
            }
            .disabled(auth.isAuthorizing)
            .padding(.top, 8)
            if auth.isAuthorizing {
                ProgressView().padding(.top, 8)
                Button(L10n.text("ui.cancel_sign_in")) { auth.cancelSignIn() }
            }
        }.buttonStyle(.plain).padding(.horizontal, 32)
    }

    private func loginButton(_ title: String, asset: String, method: String) -> some View {
        Button { auth.signIn(method: method) } label: {
            HStack(spacing: 12) {
                Image(asset).resizable().scaledToFit().frame(width: 22, height: 22)
                Text(title).font(.system(size: 16, weight: .medium))
            }.frame(maxWidth: .infinity, minHeight: 54)
                .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16))
        }.disabled(auth.isAuthorizing).accessibilityIdentifier("login-" + method)
    }

    private func loginLabel(_ title: String, symbol: String) -> some View {
        Label(title, systemImage: symbol).font(.system(size: 16, weight: .medium))
            .frame(maxWidth: .infinity, minHeight: 54)
            .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16))
    }

    private var emailPanel: some View {
        VStack(spacing: 16) {
            TextField(L10n.text("ios.email_address"), text: $email)
                .keyboardType(.emailAddress).textContentType(.emailAddress)
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                .padding(16).background(Theme.surface, in: RoundedRectangle(cornerRadius: 16))
                .disabled(auth.isAuthorizing)
                .accessibilityIdentifier("login-email-address")
            if auth.emailCode {
                Text(L10n.text("ios.enter_email_code")).font(.subheadline).foregroundStyle(Theme.fg2)
                TextField(L10n.text("ios.verification_code"), text: $code)
                    .keyboardType(.numberPad).textContentType(.oneTimeCode)
                    .padding(16).background(Theme.surface, in: RoundedRectangle(cornerRadius: 16))
                    .accessibilityIdentifier("login-email-code")
                if auth.emailError { Text(L10n.text("ios.invalid_email_code")).foregroundStyle(Theme.danger) }
                Button { auth.verifyEmail(code) } label: { loginLabel(L10n.text("ios.verify_code"), symbol: "arrow.right") }
                    .disabled(code.count < 6 || auth.emailVerifying)
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    Button(L10n.text("ios.resend_code")) {
                        auth.restartEmail(email)
                        resendAt = .now.addingTimeInterval(60)
                        code = ""
                    }.disabled(context.date < resendAt || auth.emailVerifying)
                }
            } else {
                Button {
                    auth.signInEmail(email)
                    resendAt = .now.addingTimeInterval(60)
                } label: { loginLabel(L10n.text("ios.send_code"), symbol: "envelope") }
                    .disabled(email.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || auth.isAuthorizing)
            }
            if auth.isAuthorizing && (!auth.emailCode || auth.emailVerifying) { ProgressView() }
        }.buttonStyle(.plain).padding(.horizontal, 32)
    }

    private var waitingPanel: some View {
        VStack(spacing: 10) {
            ProgressView().tint(Theme.accent)
            Text(L10n.text("ios.approve_mac")).font(.system(size: 18, weight: .semibold))
            Text(L10n.text("ui.authorize_in_browser")).font(.system(size: 14)).foregroundStyle(.white.opacity(0.7))
            Button(L10n.text("ui.cloud_cancel")) { auth.cancelSignIn() }
                .font(.system(size: 14, weight: .medium))
                .padding(.top, 6)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 22).padding(.bottom, 24)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 30, style: .continuous))
        .padding(.horizontal, 10)
    }

    private func scanned(_ value: String) {
        guard !auth.isAuthorizing else { return }
        show(app.signIn(pairing: value))
    }

    private func importPhoto(_ item: PhotosPickerItem?) async {
        guard let item, let data = try? await item.loadTransferable(type: Data.self) else { return }
        photo = nil
        guard let code = QRDecoder.firstCode(in: data) else {
            show(L10n.text("ios.no_qr"))
            return
        }
        scanned(code)
    }

    private func show(_ message: String?) {
        guard let message else { return }
        withAnimation { notice = message }
        Task {
            try? await Task.sleep(for: .seconds(3))
            withAnimation { if notice == message { notice = nil } }
        }
    }
}

enum CameraAccess {
    case unknown, granted, denied

    static func request() async -> CameraAccess {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized: return .granted
        case .notDetermined: return await AVCaptureDevice.requestAccess(for: .video) ? .granted : .denied
        default: return .denied
        }
    }
}

enum QRDecoder {
    static func firstCode(in data: Data) -> String? {
        guard let image = CIImage(data: data),
              let detector = CIDetector(ofType: CIDetectorTypeQRCode, context: nil, options: [CIDetectorAccuracy: CIDetectorAccuracyHigh]) else { return nil }
        return detector.features(in: image).compactMap { ($0 as? CIQRCodeFeature)?.messageString }.first
    }
}

/// Corner brackets in Runta orange.
private struct Reticle: View {
    var body: some View {
        GeometryReader { geometry in
            let size = geometry.size.width, arm: CGFloat = 36
            Path { path in
                for (x, y, dx, dy) in [(0.0, 0.0, 1.0, 1.0), (size, 0, -1, 1), (0, size, 1, -1), (size, size, -1, -1)] {
                    path.move(to: CGPoint(x: x, y: y + dy * arm))
                    path.addLine(to: CGPoint(x: x, y: y))
                    path.addLine(to: CGPoint(x: x + dx * arm, y: y))
                }
            }
            .stroke(Theme.accent, style: StrokeStyle(lineWidth: 4, lineCap: .round, lineJoin: .round))
        }
        .accessibilityHidden(true)
    }
}

/// Live camera preview that reports QR codes.
struct QRScannerView: UIViewRepresentable {
    var onCode: (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    func makeUIView(context: Context) -> PreviewView {
        let view = PreviewView()
        let session = context.coordinator.session
        view.previewLayer.session = session
        view.previewLayer.videoGravity = .resizeAspectFill
        guard let device = AVCaptureDevice.default(for: .video),
              let input = try? AVCaptureDeviceInput(device: device), session.canAddInput(input) else { return view }
        session.addInput(input)
        let output = AVCaptureMetadataOutput()
        if session.canAddOutput(output) {
            session.addOutput(output)
            output.setMetadataObjectsDelegate(context.coordinator, queue: .main)
            output.metadataObjectTypes = [.qr]
        }
        DispatchQueue.global(qos: .userInitiated).async { session.startRunning() }
        return view
    }

    func updateUIView(_ view: PreviewView, context: Context) {}

    static func dismantleUIView(_ view: PreviewView, coordinator: Coordinator) {
        let session = coordinator.session
        DispatchQueue.global(qos: .userInitiated).async { session.stopRunning() }
    }

    final class Coordinator: NSObject, AVCaptureMetadataOutputObjectsDelegate {
        let session = AVCaptureSession()
        let onCode: (String) -> Void
        private var last: (String, Date)?
        init(onCode: @escaping (String) -> Void) { self.onCode = onCode }

        func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput objects: [AVMetadataObject], from connection: AVCaptureConnection) {
            guard let value = (objects.first as? AVMetadataMachineReadableCodeObject)?.stringValue else { return }
            // The same code stays in view for many frames; report it once every few seconds.
            if let last, last.0 == value, Date().timeIntervalSince(last.1) < 4 { return }
            last = (value, Date())
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
            onCode(value)
        }
    }

    final class PreviewView: UIView {
        override class var layerClass: AnyClass { AVCaptureVideoPreviewLayer.self }
        var previewLayer: AVCaptureVideoPreviewLayer { layer as! AVCaptureVideoPreviewLayer }
    }
}
