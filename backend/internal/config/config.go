package config

import (
	"fmt"
	"os"
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
	OpenAIModel  string
	// Effort de raisonnement : none/minimal/low/medium/high. « low » est le bon
	// compromis pour du vocal, où la latence compte autant que la finesse.
	OpenAIEffort string

	// Speech-to-text (endpoint compatible OpenAI /v1/audio/transcriptions :
	// soit api.openai.com, soit un whisper.cpp / faster-whisper auto-hébergé sur le VPS).
	STTBaseURL string
	STTAPIKey  string
	STTModel   string

	// WhatsApp Business Cloud API (Meta).
	WhatsAppVerifyToken string
	WhatsAppAppSecret   string

	DefaultTimezone string
}

func Load() (Config, error) {
	c := Config{
		Addr:                env("ADDR", ":8080"),
		MongoURI:            env("MONGO_URI", ""),
		MongoDB:             env("MONGO_DB", "cerveau"),
		MasterKeyHex:        env("MASTER_KEY", ""),
		OpenAIAPIKey:        env("OPENAI_API_KEY", ""),
		OpenAIModel:         env("OPENAI_MODEL", "gpt-5.4-mini"),
		OpenAIEffort:        env("OPENAI_EFFORT", "low"),
		STTBaseURL:          strings.TrimSuffix(env("STT_BASE_URL", "https://api.openai.com/v1"), "/"),
		STTAPIKey:           env("STT_API_KEY", ""),
		STTModel:            env("STT_MODEL", "whisper-1"),
		WhatsAppVerifyToken: env("WHATSAPP_VERIFY_TOKEN", ""),
		WhatsAppAppSecret:   env("WHATSAPP_APP_SECRET", ""),
		DefaultTimezone:     env("DEFAULT_TIMEZONE", "Europe/Paris"),
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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
