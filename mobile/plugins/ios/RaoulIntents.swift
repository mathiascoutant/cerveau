import AppIntents
import Foundation
import Security

// Intégration Siri de Raoul.
//
// Ce fichier est injecté dans le projet Xcode par plugins/withSiriIntents.js —
// `ios/` étant régénéré par prebuild, rien ici ne doit être édité à la main
// dans le projet généré.
//
// Il ne dépend NI de React Native NI du moteur JS : c'est ce qui permet à Siri
// de l'exécuter app fermée et écran verrouillé. L'intent lit les identifiants
// dans le trousseau, appelle le backend, et rend la réponse à Siri qui la
// prononce.

// MARK: - Identifiants

/// Ce que l'app dépose dans le trousseau pour que Siri puisse travailler seule.
struct RaoulCredentials {
  let apiURL: URL
  let token: String

  /// Clé et service écrits côté JS (voir mobile/src/api.ts).
  private static let key = "cerveau.siri"
  private static let service = "raoul.siri"

  static func load() -> RaoulCredentials? {
    // expo-secure-store suffixe le service selon l'option
    // `requireAuthentication`, et lisait historiquement un service nu. On
    // essaie les deux formes, exactement comme le fait son propre code de
    // lecture, pour ne pas casser à la prochaine montée de version.
    for candidate in ["\(service):no-auth", service] {
      guard let data = read(service: candidate) else { continue }
      guard
        let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
        let urlString = json["apiUrl"] as? String,
        let url = URL(string: urlString),
        let token = json["token"] as? String,
        !token.isEmpty
      else { continue }
      return RaoulCredentials(apiURL: url, token: token)
    }
    return nil
  }

  private static func read(service: String) -> Data? {
    let account = Data(key.utf8)
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: service,
      kSecAttrAccount as String: account,
      kSecMatchLimit as String: kSecMatchLimitOne,
      kSecReturnData as String: kCFBooleanTrue as Any,
    ]
    var item: CFTypeRef?
    guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess else { return nil }
    return item as? Data
  }
}

// MARK: - Appel du backend

enum RaoulAPI {
  /// Siri coupe un intent qui traîne. Le backend s'autorise 110 s pour une
  /// question outillée : on tranche bien avant, quitte à rendre un échec
  /// explicite plutôt que de se faire tuer sans un mot.
  private static let timeout: TimeInterval = 25

  struct Answer {
    let reply: String
    let actionCount: Int
  }

  static func ask(_ text: String, using creds: RaoulCredentials) async throws -> Answer {
    var request = URLRequest(url: creds.apiURL.appendingPathComponent("api/v1/assistant/ask"))
    request.httpMethod = "POST"
    request.timeoutInterval = timeout
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.setValue("Bearer \(creds.token)", forHTTPHeaderField: "Authorization")
    request.httpBody = try JSONSerialization.data(withJSONObject: [
      "text": text,
      "timezone": TimeZone.current.identifier,
    ])

    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForRequest = timeout
    let (data, response) = try await URLSession(configuration: configuration).data(for: request)

    guard let http = response as? HTTPURLResponse else {
      throw RaoulError.unreachable
    }
    guard (200..<300).contains(http.statusCode) else {
      throw http.statusCode == 401 ? RaoulError.unauthorized : RaoulError.server(http.statusCode)
    }

    let json = try JSONSerialization.jsonObject(with: data) as? [String: Any]
    let reply = (json?["reply"] as? String) ?? ""
    let actions = (json?["actions"] as? [Any])?.count ?? 0
    guard !reply.isEmpty else { throw RaoulError.emptyReply }
    return Answer(reply: reply, actionCount: actions)
  }
}

enum RaoulError: Error {
  case unreachable
  case unauthorized
  case server(Int)
  case emptyReply

  var spoken: String {
    switch self {
    case .unreachable: return "Je n'arrive pas à joindre le serveur."
    case .unauthorized: return "Ma connexion a expiré. Ouvre l'app Raoul une fois pour la renouveler."
    case .server(let code): return "Le serveur a répondu une erreur \(code)."
    case .emptyReply: return "Je n'ai rien à te répondre là-dessus."
    }
  }
}

// MARK: - Exécution partagée

@available(iOS 16.0, *)
private func run(_ text: String) async -> IntentDialog {
  guard let creds = RaoulCredentials.load() else {
    return "Ouvre l'app Raoul une première fois, que je puisse me connecter."
  }
  do {
    let answer = try await RaoulAPI.ask(text, using: creds)
    guard answer.actionCount > 0 else {
      return IntentDialog(stringLiteral: answer.reply)
    }
    // Poser un événement dans EventKit ou ouvrir Waze demande l'app au premier
    // plan. Le dire vaut mieux que de laisser croire que c'est fait.
    return IntentDialog(stringLiteral: answer.reply + " Ouvre l'app pour que je termine.")
  } catch let error as RaoulError {
    return IntentDialog(stringLiteral: error.spoken)
  } catch {
    return "Je n'arrive pas à joindre le serveur."
  }
}

// MARK: - Intents

@available(iOS 16.0, *)
struct ReadLastMailIntent: AppIntent {
  static var title: LocalizedStringResource = "Lire mon dernier mail"
  static var description = IntentDescription("Raoul ouvre ton dernier mail et te le lit.")
  /// Faux : l'intent tourne en tâche de fond, sans lancer l'interface. C'est
  /// toute la raison d'être de cette intégration.
  static var openAppWhenRun: Bool = false

  func perform() async throws -> some IntentResult & ProvidesDialog {
    .result(dialog: await run("Lis-moi mon dernier mail."))
  }
}

@available(iOS 16.0, *)
struct DigestIntent: AppIntent {
  static var title: LocalizedStringResource = "Ce que j'ai raté"
  static var description = IntentDescription("Le point sur tes mails, Slack et WhatsApp.")
  static var openAppWhenRun: Bool = false

  func perform() async throws -> some IntentResult & ProvidesDialog {
    .result(dialog: await run("Qu'est-ce que j'ai raté ?"))
  }
}

@available(iOS 16.0, *)
struct AskRaoulIntent: AppIntent {
  static var title: LocalizedStringResource = "Demander à Raoul"
  static var description = IntentDescription("Pose une question à Raoul.")
  static var openAppWhenRun: Bool = false

  @Parameter(title: "Demande", requestValueDialog: "Qu'est-ce que je vais chercher ?")
  var request: String

  func perform() async throws -> some IntentResult & ProvidesDialog {
    .result(dialog: await run(request))
  }
}

// MARK: - Phrases Siri

/// Les phrases sont disponibles dès l'installation, sans que l'utilisateur ait
/// à ouvrir l'app Raccourcis. Chacune DOIT contenir `\(.applicationName)` —
/// c'est une exigence d'AppIntents, pas un choix de rédaction.
@available(iOS 16.0, *)
struct RaoulShortcuts: AppShortcutsProvider {
  static var appShortcuts: [AppShortcut] {
    AppShortcut(
      intent: ReadLastMailIntent(),
      phrases: [
        "Demande à \(.applicationName) de lire mon dernier mail",
        "Mon dernier mail avec \(.applicationName)",
        "\(.applicationName) mon dernier mail",
      ],
      shortTitle: "Dernier mail",
      systemImageName: "envelope.open"
    )
    AppShortcut(
      intent: DigestIntent(),
      phrases: [
        "Demande à \(.applicationName) ce que j'ai raté",
        "\(.applicationName) quoi de neuf",
      ],
      shortTitle: "Ce que j'ai raté",
      systemImageName: "tray.full"
    )
    AppShortcut(
      intent: AskRaoulIntent(),
      phrases: [
        "Demande à \(.applicationName)",
        "Parle à \(.applicationName)",
      ],
      shortTitle: "Demander",
      systemImageName: "bubble.left.and.bubble.right"
    )
  }
}
