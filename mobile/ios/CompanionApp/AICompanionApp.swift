import CompanionCore
import SwiftUI

@main
struct AICompanionApp: App {
    var body: some Scene {
        WindowGroup {
            HomeView()
        }
    }
}

private struct HomeModule: Identifiable {
    let id: String
    let title: String
    let subtitle: String
    let color: Color
}

private struct HomeView: View {
    private let modules = [
        HomeModule(id: "companion", title: "情感陪伴", subtitle: "温暖倾听 · 心灵陪伴", color: .pink),
        HomeModule(id: "life", title: "生活助手", subtitle: "日程提醒 · 生活帮手", color: .orange),
        HomeModule(id: "work", title: "工作伙伴", subtitle: "高效协作 · 智能办公", color: .indigo),
    ]

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    Text("伴AI · M0")
                        .font(.headline)
                        .foregroundStyle(.purple)
                    Text("今天想从哪里开始？")
                        .font(.largeTitle.bold())
                    Text("懂陪伴，也能一起把事情做好。")
                        .foregroundStyle(.secondary)

                    ForEach(modules) { module in
                        VStack(alignment: .leading, spacing: 8) {
                            Text(module.title)
                                .font(.title2.bold())
                                .foregroundStyle(module.color)
                            Text(module.subtitle)
                                .foregroundStyle(.secondary)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(24)
                        .background(.white, in: RoundedRectangle(cornerRadius: 28))
                    }
                }
                .padding(24)
            }
            .background(Color(red: 1, green: 0.98, blue: 0.95))
        }
    }
}

