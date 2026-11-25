import SwiftUI

/// 34 Queue settings: the desktop's settings as a grouped list.
struct QueueSettingsView: View {
    @Environment(QueueStore.self) private var queues
    @Environment(\.dismiss) private var dismiss
    var projectID: String
    @Binding var path: [QueueRoute]
    @State private var name = ""
    @State private var summary = ""
    @State private var repository = ""
    @State private var confirmDelete = false

    var body: some View {
        if let project = queues.project(projectID) {
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    GroupHeader(title: L10n.text("desktop.general")).padding(.horizontal, -18)
                    ListCard {
                        field(L10n.text("desktop.name"), $name) { save(project, ["name": name.trimmingCharacters(in: .whitespaces)]) }
                        ListRow(title: L10n.text("desktop.key")) {
                            Text(project.key).font(.system(size: 15, design: .monospaced)).foregroundStyle(Theme.fg3)
                        }
                        field(L10n.text("desktop.description"), $summary) { save(project, ["description": summary]) }
                        field(L10n.text("desktop.repository"), $repository, last: true, placeholder: "owner/repo") {
                            save(project, ["repository": repository.trimmingCharacters(in: .whitespaces)])
                        }
                    }
                    GroupHeader(title: L10n.text("desktop.queue")).padding(.horizontal, -18)
                    ListCard {
                        ListRow(title: L10n.text("desktop.at_once"), subtitle: L10n.text("desktop.at_once_hint")) {
                            Stepper(value: Binding(get: { project.queue }, set: { v in save(project, ["queue": v]) }), in: 1...10) {
                                Text("\(project.queue)").monospacedDigit()
                            }
                            .fixedSize()
                            .accessibilityIdentifier("queue-limit")
                        }
                        ListRow(title: L10n.text("desktop.paused"), subtitle: L10n.text("desktop.pause_hint")) {
                            Toggle("", isOn: Binding(get: { project.paused }, set: { v in Task { await queues.setPaused(project.id, v) } }))
                                .labelsHidden().tint(Theme.accent)
                        }
                        Button { path.append(.workers(project.id)) } label: {
                            ListRow(title: L10n.text("desktop.agents")) { trailing(project.workers.filter(\.on).compactMap { $0.kind?.displayName }.joined(separator: ", ")) }
                        }.buttonStyle(.plain)
                        Button { path.append(.tags(project.id)) } label: {
                            ListRow(title: L10n.text("desktop.tags"), showDivider: false) { trailing("\(project.tags.count)") }
                        }.buttonStyle(.plain)
                    }
                    ListCard {
                        Button { path.append(.knowledge(project.id)) } label: {
                            ListRow(icon: "book", title: L10n.text("desktop.knowledge"), showDivider: false) { trailing("") }
                        }.buttonStyle(.plain)
                    }
                    ListCard {
                        Button { confirmDelete = true } label: {
                            ListRow(icon: "trash", iconColor: Theme.danger, title: L10n.text("desktop.delete_queue"), titleColor: Theme.danger, showDivider: false)
                        }.buttonStyle(.plain)
                        .accessibilityIdentifier("delete-queue")
                    }
                }
                .padding(16)
            }
            .background(Theme.bg)
            .navigationTitle(L10n.text("desktop.project_settings"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar(.hidden, for: .tabBar)
            .onAppear { name = project.name; summary = project.description ?? ""; repository = project.repository ?? "" }
            .alert(L10n.text("desktop.delete_queue_title", values: ["name": project.name]), isPresented: $confirmDelete) {
                Button(L10n.text("desktop.delete_queue"), role: .destructive) {
                    Task { if await queues.delete(project.id) { path.removeAll() } }
                }
                Button(L10n.text("desktop.cancel"), role: .cancel) {}
            } message: { Text(L10n.text("desktop.delete_queue_detail")) }
            .queueActionAlert()
        }
    }

    private func trailing(_ text: String) -> some View {
        HStack(spacing: 4) {
            Text(text).lineLimit(1).foregroundStyle(Theme.fg3)
            Image(systemName: "chevron.right").font(.system(size: 12)).foregroundStyle(Theme.fg3)
        }
        .font(.system(size: 14))
    }

    private func field(_ title: String, _ value: Binding<String>, last: Bool = false, placeholder: String = "", commit: @escaping () -> Void) -> some View {
        ListRow(title: title, showDivider: !last) {
            TextField(placeholder, text: value)
                .multilineTextAlignment(.trailing)
                .foregroundStyle(Theme.fg2)
                .submitLabel(.done)
                .onSubmit(commit)
                .textInputAutocapitalization(.never)
        }
    }

    private func save(_ project: BoardProject, _ change: [String: Any]) {
        Task {
            if !(await queues.update(project.id, change)) {
                name = project.name; summary = project.description ?? ""; repository = project.repository ?? ""
            }
        }
    }
}

/// 35 The queue's agents, in order; the first enabled one takes new requests.
struct QueueWorkersView: View {
    @Environment(QueueStore.self) private var queues
    var projectID: String

    var body: some View {
        if let project = queues.project(projectID) {
            List {
                Section {
                    ForEach(Array(project.workers.enumerated()), id: \.offset) { index, worker in
                        HStack(spacing: 12) {
                            if let kind = worker.kind { AgentIcon(agent: kind, size: 24) }
                            VStack(alignment: .leading, spacing: 1) {
                                Text(worker.kind?.displayName ?? worker.agent).font(.system(size: 15.5))
                                Text(worker.model.isEmpty ? L10n.text("desktop.default_model") : worker.model)
                                    .font(.caption2Muted).foregroundStyle(Theme.fg3)
                            }
                            Spacer()
                            Toggle("", isOn: Binding(get: { worker.on }, set: { on in
                                var workers = project.workers
                                workers[index].on = on
                                guard workers.contains(where: \.on) else { return }
                                Task { await queues.setWorkers(project.id, workers) }
                            }))
                            .labelsHidden().tint(Theme.accent)
                        }
                        .listRowBackground(Theme.surface)
                    }
                    .onMove { from, to in
                        var workers = project.workers
                        workers.move(fromOffsets: from, toOffset: to)
                        Task { await queues.setWorkers(project.id, workers) }
                    }
                    .onDelete { offsets in
                        var workers = project.workers
                        workers.remove(atOffsets: offsets)
                        guard workers.contains(where: \.on) else { return }
                        Task { await queues.setWorkers(project.id, workers) }
                    }
                } footer: {
                    Text(L10n.text("desktop.agents_hint")).font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                }
                Section {
                    Menu {
                        ForEach(AgentKind.allCases.filter { kind in !project.workers.contains { $0.agent == kind.launcherID } }) { kind in
                            Button { Task { await queues.setWorkers(project.id, project.workers + [BoardWorker(agent: kind.launcherID)]) } } label: {
                                Label { Text(kind.displayName) } icon: { AgentIcon(agent: kind, size: 20) }
                            }
                        }
                    } label: { Label(L10n.text("desktop.add_worker"), systemImage: "plus").foregroundStyle(Theme.accent) }
                    .disabled(project.workers.count >= 12)
                    .listRowBackground(Theme.surface)
                }
            }
            .scrollContentBackground(.hidden)
            .background(Theme.bg)
            .environment(\.editMode, .constant(.active))
            .navigationTitle(L10n.text("desktop.agents"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar(.hidden, for: .tabBar)
            .queueActionAlert()
        }
    }
}

/// 35b Tags: add, rename, delete, reorder.
struct QueueTagsView: View {
    @Environment(QueueStore.self) private var queues
    var projectID: String
    @State private var adding = ""
    @State private var renaming: BoardTag?
    @State private var newLabel = ""

    var body: some View {
        if let project = queues.project(projectID) {
            List {
                Section {
                    ForEach(project.tags) { tag in
                        Button { renaming = tag; newLabel = tag.label } label: { Text(tag.label).foregroundStyle(Theme.fg) }
                            .listRowBackground(Theme.surface)
                    }
                    .onMove { from, to in
                        var tags = project.tags; tags.move(fromOffsets: from, toOffset: to)
                        Task { await queues.setTags(project.id, tags) }
                    }
                    .onDelete { offsets in
                        var tags = project.tags; tags.remove(atOffsets: offsets)
                        guard !tags.isEmpty else { return }
                        Task { await queues.setTags(project.id, tags) }
                    }
                    HStack {
                        Image(systemName: "plus").foregroundStyle(Theme.fg3)
                        TextField(L10n.text("ios.add_tag"), text: $adding).submitLabel(.done).onSubmit { add(project) }
                    }
                    .listRowBackground(Theme.surface)
                    .disabled(project.tags.count >= 12)
                } footer: {
                    Text(L10n.text("desktop.tags_hint")).font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                }
                Section {
                    Button(L10n.text("desktop.reset_tags")) {
                        Task { await queues.setTags(project.id, [BoardTag(id: "bug", label: L10n.text("desktop.tag_bug")),
                                                                 BoardTag(id: "feature", label: L10n.text("desktop.tag_feature")),
                                                                 BoardTag(id: "discussion", label: L10n.text("desktop.tag_discussion"))]) }
                    }
                    .foregroundStyle(Theme.accent)
                    .listRowBackground(Theme.surface)
                }
            }
            .scrollContentBackground(.hidden)
            .background(Theme.bg)
            .environment(\.editMode, .constant(.active))
            .navigationTitle(L10n.text("desktop.tags"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar(.hidden, for: .tabBar)
            .alert(L10n.text("ios.rename"), isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
                TextField("", text: $newLabel)
                Button(L10n.text("ios.save")) {
                    guard let renaming, !newLabel.trimmingCharacters(in: .whitespaces).isEmpty else { return }
                    let tags = project.tags.map { $0.id == renaming.id ? BoardTag(id: $0.id, label: String(newLabel.prefix(24))) : $0 }
                    Task { await queues.setTags(project.id, tags) }
                }
                Button(L10n.text("desktop.cancel"), role: .cancel) {}
            }
            .queueActionAlert()
        }
    }

    private func add(_ project: BoardProject) {
        let label = String(adding.trimmingCharacters(in: .whitespaces).prefix(24))
        guard !label.isEmpty else { return }
        let id = Self.tagID(label, taken: Set(project.tags.map(\.id)))
        adding = ""
        Task { await queues.setTags(project.id, project.tags + [BoardTag(id: id, label: label)]) }
    }

    /// A board tag id (`^[a-z0-9][a-z0-9-]{0,23}$`) from a label.
    static func tagID(_ label: String, taken: Set<String>) -> String {
        var base = label.lowercased().map { $0.isASCII && ($0.isLetter || $0.isNumber) ? String($0) : "-" }.joined()
        base = base.split(separator: "-").joined(separator: "-")
        if base.isEmpty { base = "tag" }
        base = String(base.prefix(20))
        var id = base
        var i = 2
        while taken.contains(id) { id = "\(base)-\(i)"; i += 1 }
        return id
    }
}

/// 36 New queue.
struct NewQueueSheet: View {
    @Environment(QueueStore.self) private var queues
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var key = ""
    @State private var keyEdited = false
    @State private var repository = ""
    @State private var org = ""
    @State private var agent = AgentKind.claudeCode
    @State private var creating = false

    var body: some View {
        let orgs = Array(Set(queues.projects.compactMap(\.org).filter { !$0.isEmpty })).sorted()
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    ListCard {
                        ListRow(title: L10n.text("desktop.name")) {
                            TextField("", text: $name).multilineTextAlignment(.trailing)
                                .onChange(of: name) { _, v in if !keyEdited { key = Self.suggestKey(v) } }
                                .accessibilityIdentifier("new-queue-name")
                        }
                        ListRow(title: L10n.text("desktop.key")) {
                            TextField("", text: Binding(get: { key }, set: { key = String($0.uppercased().filter { $0.isASCII && ($0.isLetter || $0.isNumber) }.prefix(6)); keyEdited = true }))
                                .multilineTextAlignment(.trailing).font(.system(size: 15, design: .monospaced))
                                .textInputAutocapitalization(.characters)
                        }
                        ListRow(title: L10n.text("desktop.repository"), showDivider: !orgs.isEmpty || true) {
                            TextField(L10n.text("ios.repository_optional"), text: $repository).multilineTextAlignment(.trailing)
                                .textInputAutocapitalization(.never).autocorrectionDisabled()
                        }
                        if orgs.count > 0 {
                            ListRow(title: L10n.text("desktop.organization")) {
                                Picker("", selection: $org) {
                                    Text("—").tag("")
                                    ForEach(orgs, id: \.self) { Text($0).tag($0) }
                                }.labelsHidden().tint(Theme.fg2)
                            }
                        }
                        ListRow(title: L10n.text("desktop.default_agent"), showDivider: false) {
                            Picker("", selection: $agent) {
                                ForEach(AgentKind.allCases) { Text($0.displayName).tag($0) }
                            }.labelsHidden().tint(Theme.fg2)
                        }
                    }
                    if !key.isEmpty {
                        Text(L10n.text("desktop.key_hint", values: ["key": key])).font(.system(size: 12.5)).foregroundStyle(Theme.fg3).padding(.horizontal, 4)
                    }
                }
                .padding(16)
            }
            .background(Theme.bg)
            .navigationTitle(L10n.text("desktop.new_project"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button(L10n.text("desktop.cancel")) { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L10n.text("desktop.create")) {
                        creating = true
                        Task {
                            defer { creating = false }
                            if await queues.create(name: name.trimmingCharacters(in: .whitespaces), key: key,
                                                   repository: repository.trimmingCharacters(in: .whitespaces), org: org, agent: agent.launcherID) != nil {
                                dismiss()
                            }
                        }
                    }
                    .disabled(creating || name.trimmingCharacters(in: .whitespaces).isEmpty)
                    .accessibilityIdentifier("create-queue")
                }
            }
            .queueActionAlert()
        }
        .onAppear { org = queues.selected?.org ?? "" }
    }

    /// Initials of the words, or the first letters: "Errand" → "ERR".
    static func suggestKey(_ name: String) -> String {
        let words = name.uppercased().split { !($0.isASCII && ($0.isLetter || $0.isNumber)) }
        if words.count > 1 { return String(words.compactMap(\.first).prefix(6)) }
        return String((words.first ?? "").prefix(3))
    }
}

/// 37 Knowledge: what agents learned working on the queue.
struct KnowledgeView: View {
    @Environment(QueueStore.self) private var queues
    var projectID: String
    @Binding var path: [QueueRoute]
    @State private var query = ""

    private static let order = ["decision", "architecture", "glossary", "runbook"]

    var body: some View {
        let concepts = (queues.knowledge[projectID] ?? []).filter {
            query.isEmpty || $0.title.localizedCaseInsensitiveContains(query) || ($0.body ?? "").localizedCaseInsensitiveContains(query)
        }
        let groups = Dictionary(grouping: concepts) { $0.type ?? "" }
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                Text(L10n.text("desktop.knowledge_hint", values: ["name": queues.project(projectID)?.name ?? ""]))
                    .font(.system(size: 13)).foregroundStyle(Theme.fg3)
                if queues.knowledge[projectID]?.isEmpty == true {
                    Text(L10n.text("desktop.knowledge_empty")).font(.rowBody).foregroundStyle(Theme.fg2).padding(.top, 30)
                }
                ForEach(groups.keys.sorted { (Self.order.firstIndex(of: $0) ?? 9, $0) < (Self.order.firstIndex(of: $1) ?? 9, $1) }, id: \.self) { type in
                    let items = (groups[type] ?? []).sorted { ($0.status == "deprecated" ? 1 : 0, $0.title) < ($1.status == "deprecated" ? 1 : 0, $1.title) }
                    GroupHeader(title: Self.typeLabel(type), count: items.count).padding(.horizontal, -18)
                    ListCard {
                        ForEach(Array(items.enumerated()), id: \.element.id) { index, concept in
                            Button { path.append(.concept(projectID, concept.id)) } label: {
                                ListRow(title: concept.title, subtitle: concept.description, showDivider: index < items.count - 1)
                                    .opacity(concept.status == "deprecated" ? 0.5 : 1)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .padding(16)
        }
        .background(Theme.bg)
        .searchable(text: $query)
        .navigationTitle(L10n.text("desktop.knowledge"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .task { await queues.loadKnowledge(projectID) }
    }

    static func typeLabel(_ type: String) -> String {
        switch type {
        case "decision": L10n.text("desktop.section_decisions")
        case "architecture": L10n.text("desktop.section_architecture")
        case "glossary": L10n.text("desktop.section_glossary")
        case "runbook": L10n.text("desktop.section_runbooks")
        default: type.capitalized
        }
    }
}

/// 37b One knowledge entry.
struct ConceptView: View {
    @Environment(QueueStore.self) private var queues
    var projectID: String
    var conceptID: String
    @Binding var path: [QueueRoute]

    var body: some View {
        let concept = queues.knowledge[projectID]?.first { $0.id == conceptID }
        ScrollView {
            if let concept {
                VStack(alignment: .leading, spacing: 12) {
                    Text(concept.title).font(.system(size: 22, weight: .semibold))
                    HStack(spacing: 6) {
                        pill(KnowledgeView.typeLabel(concept.type ?? ""))
                        if let status = concept.status, !status.isEmpty { pill(status.capitalized) }
                    }
                    if let by = concept.generated_by, !by.isEmpty {
                        Text("\(L10n.text("desktop.written_by")) \(by)" + ((concept.sources ?? []).isEmpty ? "" : " · " + (concept.sources ?? []).joined(separator: ", ")))
                            .font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                    }
                    MarkdownText(text: Self.linked(concept.body ?? ""))
                        .environment(\.openURL, OpenURLAction { url in
                            guard url.scheme == "fennec-concept" else { return .systemAction }
                            let name = url.host() ?? ""
                            if let target = queues.knowledge[projectID]?.first(where: { $0.id.hasSuffix("/" + name) || $0.id == name }) {
                                path.append(.concept(projectID, target.id))
                            }
                            return .handled
                        })
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(Theme.bg)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .toolbar {
            if let concept {
                ToolbarItem(placement: .topBarTrailing) {
                    ShareLink(item: "# \(concept.title)\n\n\(concept.body ?? "")") { Image(systemName: "square.and.arrow.up") }
                }
            }
        }
    }

    private func pill(_ text: String) -> some View {
        Text(text).font(.system(size: 11)).foregroundStyle(Theme.fg2)
            .padding(.horizontal, 7).padding(.vertical, 2)
            .background(Theme.surface2, in: Capsule())
    }

    /// [[name]] links between entries become tappable links.
    static func linked(_ body: String) -> String {
        guard let regex = try? NSRegularExpression(pattern: #"\[\[([A-Za-z0-9_./-]+)\]\]"#) else { return body }
        let range = NSRange(body.startIndex..., in: body)
        return regex.stringByReplacingMatches(in: body, range: range, withTemplate: "[$1](fennec-concept://$1)")
    }
}
