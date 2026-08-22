import SwiftUI
import WidgetKit

// Widget « Parler à Raoul ».
//
// Injecté dans le projet Xcode par plugins/withRaoulWidget.js — `ios/` étant
// régénéré par prebuild, rien ici ne doit être édité dans le projet généré.
//
// Un widget ne peut pas prendre le micro : iOS réserve l'enregistrement aux
// apps au premier plan, et le code d'un widget ne tourne que le temps de
// dessiner une image. Le rôle de celui-ci est donc d'ouvrir l'app sur un lien
// profond, `raoul://listen`, que l'app traite en démarrant l'écoute. Un appui,
// et Raoul écoute — sans passer par l'icône ni par un onglet.

private let deepLink = URL(string: "raoul://listen")

// Palette reprise de mobile/src/theme.ts : le widget doit se lire comme un
// morceau de l'app, pas comme une pièce rapportée.
private enum Palette {
  static let background = Color(red: 0.031, green: 0.047, blue: 0.067) // #080C11
  static let surface = Color(red: 0.075, green: 0.110, blue: 0.145) // #131C25
  static let accent = Color(red: 0.176, green: 0.831, blue: 0.749) // #2DD4BF
  static let text = Color(red: 0.933, green: 0.953, blue: 0.973) // #EEF3F8
  static let muted = Color(red: 0.486, green: 0.553, blue: 0.612) // #7C8D9C
}

struct RaoulEntry: TimelineEntry {
  let date: Date
}

// Le widget n'affiche aucune donnée : il n'y a donc rien à rafraîchir, et la
// politique `.never` évite de consommer le budget de réveils d'iOS pour rien.
struct RaoulProvider: TimelineProvider {
  func placeholder(in context: Context) -> RaoulEntry {
    RaoulEntry(date: Date())
  }

  func getSnapshot(in context: Context, completion: @escaping (RaoulEntry) -> Void) {
    completion(RaoulEntry(date: Date()))
  }

  func getTimeline(in context: Context, completion: @escaping (Timeline<RaoulEntry>) -> Void) {
    completion(Timeline(entries: [RaoulEntry(date: Date())], policy: .never))
  }
}

struct RaoulWidgetView: View {
  @Environment(\.widgetFamily) private var family

  var body: some View {
    content
      .widgetURL(deepLink)
      .raoulContainerBackground()
  }

  @ViewBuilder
  private var content: some View {
    switch family {
    case .accessoryCircular:
      // Écran verrouillé : pas de couleur, iOS applique son propre rendu.
      // Le fond dédié n'existe qu'à partir d'iOS 16, comme la famille elle-même.
      if #available(iOS 16.0, *) {
        ZStack {
          AccessoryWidgetBackground()
          Image(systemName: "mic.fill").font(.system(size: 20, weight: .semibold))
        }
      } else {
        micro
      }
    default:
      VStack(alignment: .leading, spacing: 10) {
        micro
        Spacer(minLength: 0)
        Text("Parler à Raoul")
          .font(.system(size: 16, weight: .semibold))
          .foregroundColor(Palette.text)
        Text("Touche et parle")
          .font(.system(size: 12, weight: .regular))
          .foregroundColor(Palette.muted)
      }
      .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
      .padding(4)
    }
  }

  private var micro: some View {
    ZStack {
      Circle()
        .fill(Palette.accent.opacity(0.14))
        .frame(width: 44, height: 44)
      Image(systemName: "mic.fill")
        .font(.system(size: 19, weight: .semibold))
        .foregroundColor(Palette.accent)
    }
  }
}

struct RaoulWidget: Widget {
  var body: some WidgetConfiguration {
    StaticConfiguration(kind: "RaoulListenWidget", provider: RaoulProvider()) { _ in
      RaoulWidgetView()
    }
    .configurationDisplayName("Parler à Raoul")
    .description("Ouvre Raoul et lance l'écoute.")
    .supportedFamilies(supportedFamilies)
  }

  private var supportedFamilies: [WidgetFamily] {
    var families: [WidgetFamily] = [.systemSmall]
    if #available(iOS 16.0, *) {
      families.append(.accessoryCircular)
    }
    return families
  }
}

@main
struct RaoulWidgetBundle: WidgetBundle {
  var body: some Widget {
    RaoulWidget()
  }
}

private extension View {
  // iOS 17 exige que le fond passe par containerBackground : posé autrement,
  // il est ignoré et le widget s'affiche sur un fond système.
  @ViewBuilder
  func raoulContainerBackground() -> some View {
    if #available(iOS 17.0, *) {
      containerBackground(for: .widget) { Palette.background }
    } else {
      background(Palette.background)
    }
  }
}
