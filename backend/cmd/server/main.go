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
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/projets"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
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
	// Une exécution d'agent restée ouverte au dernier arrêt ne finira jamais :
	// on la clôt, sinon son personnage resterait « au travail » pour toujours.
	if n, err := st.AbandonStaleRuns(ctx); err != nil {
		slog.Warn("exécutions d'agents non closes", "err", err)
	} else if n > 0 {
		slog.Info("exécutions d'agents interrompues par le redémarrage", "nombre", n)
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

	perso := loadPerso(ctx, cfg, st)
	if perso != nil && perso.Sonde != nil {
		defer perso.Sonde.Close()
	}

	app := api.NewServer(cfg, st, cipher, wa).WithPerso(perso)
	// La veille des messageries tourne en fond, tant que le serveur vit.
	app.StartVeille(ctx)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Routes(),
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

// loadPerso monte le suivi des projets perso : le registre, la sonde du VPS,
// GitHub, et le savoir de départ de l'équipe. Rien de tout ça n'est vital —
// un registre illisible ou une sonde mal configurée se signalent dans les
// journaux, et Raoul démarre sans l'espace perso plutôt que pas du tout.
func loadPerso(ctx context.Context, cfg config.Config, st *store.Store) *projets.Service {
	if cfg.ProjetsRegistre == "" {
		slog.Info("espace perso désactivé (PROJETS_REGISTRE vide)")
		return nil
	}
	reg, err := registre.Charger(cfg.ProjetsRegistre)
	if err != nil {
		slog.Error("registre des projets illisible, espace perso désactivé", "err", err)
		return nil
	}
	probe, err := sonde.NewClient(sonde.Config{
		Cible: cfg.SondeSSH, Cle: cfg.SondeCle, CleHote: cfg.SondeCleHote,
		KnownHosts: cfg.SondeKnownHosts, Commande: cfg.SondeCommande,
	})
	if err != nil {
		slog.Error("sonde du VPS mal configurée, on s'en passe", "err", err)
		probe = nil
	}
	gh := projets.NewGitHub(cfg.GitHubToken, cfg.GitHubTokens)
	svc := projets.New(reg, probe, gh)

	if cfg.EquipeAmorce != "" {
		n, err := equipe.Amorcer(ctx, st, cfg.EquipeAmorce)
		if err != nil {
			slog.Error("amorce de l'équipe", "err", err)
		} else {
			slog.Info("amorce de l'équipe", "fiches_mises_a_jour", n)
		}
	}
	slog.Info("espace perso actif", "projets", len(reg.Projets), "sonde", probe != nil, "github_authentifie", gh.Authentifie())
	go checkPerso(svc)
	return svc
}

// checkPerso vérifie au démarrage ce qui, sinon, ne se verrait qu'en ouvrant
// l'écran Projets : la sonde répond-elle, et les jetons GitHub ouvrent-ils
// chacun des dépôts déclarés. Le résultat va dans les journaux — c'est la
// première chose à lire après un déploiement.
func checkPerso(svc *projets.Service) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if svc.Sonde != nil {
		if sys, err := svc.Sonde.Systeme(ctx); err != nil {
			slog.Error("sonde du VPS injoignable", "err", err)
		} else {
			slog.Info("sonde du VPS joignable", "hote", sys.Hote, "systeme", sys.OS)
		}
	}
	readable, failed := svc.GitHub.Verifier(ctx, svc.Registre.Depots())
	for repo, reason := range failed {
		slog.Warn("GitHub : dépôt illisible", "depot", repo, "raison", reason)
	}
	slog.Info("GitHub : dépôts vérifiés", "lisibles", len(readable), "illisibles", len(failed))
}
