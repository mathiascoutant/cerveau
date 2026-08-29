// Commande cerveau-server : l'API de Raoul.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/mathiascoutant/cerveau/backend/internal/api"
	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/cryptoutil"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func main() {
	// LOG_LEVEL=debug fait parler whatsmeow : c'est le seul moyen de suivre un
	// appairage qui échoue, la liaison se jouant entre le téléphone et les
	// serveurs de WhatsApp sans que le nôtre en voie grand-chose.
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	// .env est optionnel : en production sur le VPS, systemd fournit l'environnement.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration invalide", "err", err)
		os.Exit(1)
	}

	cipher, err := cryptoutil.New(cfg.MasterKeyHex)
	if err != nil {
		slog.Error("clé de chiffrement invalide", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		slog.Error("connexion MongoDB impossible", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.Close(shutdownCtx)
	}()

	// WhatsApp : une connexion permanente, montée avant le serveur HTTP. Un
	// appareil lié qui n'est pas connecté ne reçoit rien, et ce qu'il n'a pas
	// reçu pendant l'arrêt ne se rattrape pas.
	// Un magasin illisible n'arrête pas le serveur : mails, Slack, agenda et
	// liste à faire n'ont rien à voir avec WhatsApp, et les priver de service
	// pour un dossier non inscriptible serait une punition disproportionnée.
	// L'écran Accès dira que la liaison est impossible.
	wa, err := whatsapp.NewManager(ctx, cfg.WhatsAppSessionDB, api.NewWhatsAppJournal(st, cipher))
	if err != nil {
		slog.Error("WhatsApp indisponible, le reste démarre quand même", "err", err)
	}
	defer wa.Close()
	if wa.Enabled() {
		if err := wa.Start(ctx); err != nil {
			// Pas fatal : le reste de Raoul marche sans, et l'app permet de
			// relier le compte.
			slog.Error("WhatsApp : reconnexion des comptes", "err", err)
		}
	} else {
		slog.Info("WhatsApp désactivé (WHATSAPP_SESSION_DB vide)")
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewServer(cfg, st, cipher, wa).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		slog.Info("cerveau démarré", "addr", cfg.Addr, "db", cfg.MongoDB, "modele", cfg.OpenAIModel)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serveur HTTP", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("arrêt en cours…")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("arrêt du serveur", "err", err)
	}
}
