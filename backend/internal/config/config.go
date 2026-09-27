package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config regroupe toute la configuration du serveur, lue depuis l'environnement.
type Config struct {
	Addr     string
	MongoURI string
	MongoDB  string

	// Clé de chiffrement des secrets utilisateurs (hex, 32 octets => 64 caractères).
	MasterKeyHex string

	OpenAIAPIKey string
	// OpenAIModel : l'étage des outils, celui de la plupart des questions.
	OpenAIModel string
	// Effort de raisonnement : none/minimal/low/medium/high. « low » est le bon
	// compromis pour du vocal, où la latence compte autant que la finesse.
	OpenAIEffort string
	// Modèle et effort de l'étage rapide — conversation sans outils (voir
	// assistant/router.go). Vide, l'étage tourne sur le modèle principal.
	OpenAIFastModel  string
	OpenAIFastEffort string
	// Modèle et effort du débrief approfondi et de l'étage fort du routage
	// (voir assistant/debrief.go). Vides, gpt-5.4 en effort medium.
	OpenAIDeepModel  string
	OpenAIDeepEffort string

	// Tuleap : l'instance et le tracker des tickets CSP. La clé d'accès est
	// personnelle, elle se saisit dans l'app et se range chiffrée en base.
	TuleapBaseURL string
	// Sélection « cycles CSP », celle de PXFeed (providers/tuleap/csp.go) :
	// le tracker, la release du planning, et les compagnies gardées. Les
	// identifiants ont pour défaut ceux de PXFeed ; vides, ils s'appliquent.
	TuleapCSPTrackerID  int
	TuleapCSPReleaseID  int
	TuleapCSPPlanningID int
	TuleapCSPProjectID  int
	TuleapCSPAirlines   []string
	// Sélection générique, quand on ne veut PAS la lecture façon PXFeed :
	// une requête JSON (« query ») ou TQL (« expert_query ») sur le tracker.
	// Renseigner l'une des deux bascule dans ce mode.
	TuleapCSPQuery       string
	TuleapCSPExpertQuery string
	// TuleapCSPAssignedToMe : ne garder que les tickets assignés à l'utilisateur.
	TuleapCSPAssignedToMe bool
	// TuleapAccessKey : une clé d'accès posée sur le serveur, comme le fait
	// PXFeed-UI (TULEAP_KEY). Elle sert à qui n'a pas saisi la sienne dans
	// l'app ; une clé personnelle, quand elle existe, l'emporte.
	TuleapAccessKey string

	// Speech-to-text (endpoint compatible OpenAI /v1/audio/transcriptions :
	// soit api.openai.com, soit un whisper.cpp / faster-whisper auto-hébergé sur le VPS).
	STTBaseURL string
	STTAPIKey  string
	STTModel   string

	// Synthèse vocale ElevenLabs. Sans clé, l'app retombe sur la voix système.
	ElevenLabsAPIKey  string
	ElevenLabsVoiceID string
	ElevenLabsModel   string
	// Langue imposée à la synthèse. Sans elle le modèle devine, et un seul mot
	// anglais dans une phrase française lui fait prendre l'accent.
	ElevenLabsLanguage string

	// WhatsAppSessionDB : fichier SQLite où whatsmeow garde la session de
	// l'appareil lié. Le seul état de Cerveau qui ne vit pas dans Mongo — la
	// bibliothèque ne sait stocker ses clés que dans du SQL. À sauvegarder :
	// le perdre oblige à refaire l'appairage. Vide = WhatsApp désactivé.
	WhatsAppSessionDB string

	// Slack OAuth : évite le copier-coller manuel du token xoxp-.
	SlackClientID     string
	SlackClientSecret string

	// URL publique du backend, pour construire le redirect_uri OAuth.
	PublicBaseURL string

	DefaultTimezone string
	// AllowSignup ouvre l'inscription depuis l'app. Fermée par défaut : chaque
	// compte consomme les clés du serveur.
	AllowSignup bool
	// Prénom utilisé quand l'utilisateur n'en a pas encore enregistré depuis
	// l'app. Pratique pour un déploiement mono-utilisateur.
	DefaultUserName string
}

func Load() (Config, error) {
	c := Config{
		Addr:                  env("ADDR", ":8080"),
		MongoURI:              env("MONGO_URI", ""),
		MongoDB:               env("MONGO_DB", "cerveau"),
		MasterKeyHex:          env("MASTER_KEY", ""),
		OpenAIAPIKey:          env("OPENAI_API_KEY", ""),
		OpenAIModel:           env("OPENAI_MODEL", "gpt-5.4-mini"),
		OpenAIEffort:          env("OPENAI_EFFORT", "low"),
		OpenAIFastModel:       env("OPENAI_FAST_MODEL", ""),
		OpenAIFastEffort:      env("OPENAI_FAST_EFFORT", "none"),
		OpenAIDeepModel:       env("OPENAI_DEEP_MODEL", ""),
		OpenAIDeepEffort:      env("OPENAI_DEEP_EFFORT", ""),
		TuleapBaseURL:         strings.TrimSuffix(env("TULEAP_BASE_URL", env("TULEAP_URL", "")), "/"),
		TuleapCSPTrackerID:    envInt("TULEAP_CSP_TRACKER_ID"),
		TuleapCSPReleaseID:    envInt("TULEAP_CSP_RELEASE_ID"),
		TuleapCSPPlanningID:   envInt("TULEAP_CSP_PLANNING_ID"),
		TuleapCSPProjectID:    envInt("TULEAP_CSP_PROJECT_ID"),
		TuleapCSPAirlines:     envList("TULEAP_CSP_AIRLINES"),
		TuleapCSPQuery:        env("TULEAP_CSP_QUERY", ""),
		TuleapCSPExpertQuery:  env("TULEAP_CSP_EXPERT_QUERY", ""),
		TuleapCSPAssignedToMe: env("TULEAP_CSP_ASSIGNED_TO_ME", "") == "true",
		TuleapAccessKey:       env("TULEAP_ACCESS_KEY", env("TULEAP_KEY", "")),
		STTBaseURL:            strings.TrimSuffix(env("STT_BASE_URL", "https://api.openai.com/v1"), "/"),
		STTAPIKey:             env("STT_API_KEY", ""),
		STTModel:              env("STT_MODEL", "whisper-1"),
		ElevenLabsAPIKey:      env("ELEVENLABS_API_KEY", ""),
		ElevenLabsVoiceID:     env("ELEVENLABS_VOICE_ID", ""),
		ElevenLabsModel:       env("ELEVENLABS_MODEL", ""),
		ElevenLabsLanguage:    env("ELEVENLABS_LANGUAGE", ""),
		WhatsAppSessionDB:     env("WHATSAPP_SESSION_DB", "data/whatsapp.db"),
		SlackClientID:         env("SLACK_CLIENT_ID", ""),
		SlackClientSecret:     env("SLACK_CLIENT_SECRET", ""),
		PublicBaseURL:         strings.TrimSuffix(env("PUBLIC_BASE_URL", ""), "/"),
		DefaultTimezone:       env("DEFAULT_TIMEZONE", "Europe/Paris"),
		DefaultUserName:       env("DEFAULT_USER_NAME", ""),
		AllowSignup:           env("ALLOW_SIGNUP", "") == "true",
	}

	var missing []string
	if c.MongoURI == "" {
		missing = append(missing, "MONGO_URI")
	}
	if c.MasterKeyHex == "" {
		missing = append(missing, "MASTER_KEY")
	}
	if c.OpenAIAPIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("variables d'environnement manquantes : %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// SlackOAuthEnabled : le flux n'est proposé que si tout est renseigné.
func (c Config) SlackOAuthEnabled() bool {
	return c.SlackClientID != "" && c.SlackClientSecret != "" && c.PublicBaseURL != ""
}

// SlackRedirectURI doit correspondre exactement à l'URL déclarée dans
// « Redirect URLs » côté Slack, sinon l'échange échoue en bad_redirect_uri.
func (c Config) SlackRedirectURI() string {
	return c.PublicBaseURL + "/oauth/slack/callback"
}

// TuleapEnabled : l'onglet Jobs ne propose la carte CSP que si le serveur
// sait où est Tuleap. Le tracker a un défaut, celui de PXFeed.
func (c Config) TuleapEnabled() bool {
	return c.TuleapBaseURL != ""
}

// TuleapGenericQuery : une requête explicite a été donnée, on lit le tracker
// avec elle au lieu de la sélection « cycles CSP » de PXFeed.
func (c Config) TuleapGenericQuery() bool {
	return c.TuleapCSPQuery != "" || c.TuleapCSPExpertQuery != ""
}

func envList(key string) []string {
	var out []string
	for _, item := range strings.Split(os.Getenv(key), ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envInt(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return 0
	}
	return n
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
