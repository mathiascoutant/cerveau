// Commande diagblanc : l'essai à blanc du diagnostic.
//
// Rejoue sur les derniers mails de la boîte pro ce que la veille et les trois
// verrous du diagnostic auraient décidé, et l'écrit. Lecture seule : aucun
// sujet, aucun diagnostic, aucun mail marqué lu, et Hugo n'est pas lancé.
//
// Elle se lance là où le serveur a ses réglages (la base, la clé de
// chiffrement, l'atelier, la clé Claude) :
//
//	diagblanc -n 40 -dossiers "Réception,CSP"
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/mathiascoutant/cerveau/backend/internal/api"
	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/cryptoutil"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func main() {
	email := flag.String("email", "", "le compte Cerveau (défaut : le premier de VEILLE_COMPTES)")
	nombre := flag.Int("n", 40, "derniers mails lus par dossier")
	dossiers := flag.String("dossiers", "Réception,CSP", "dossiers de la boîte, séparés par des virgules")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		fatal("configuration invalide", err)
	}
	if *email == "" && len(cfg.VeilleComptes) > 0 {
		*email = cfg.VeilleComptes[0]
	}
	if *email == "" {
		fatal("aucun compte", fmt.Errorf("donne -email ou règle VEILLE_COMPTES"))
	}
	cipher, err := cryptoutil.New(cfg.MasterKeyHex)
	if err != nil {
		fatal("clé de chiffrement invalide", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	st, err := store.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		fatal("connexion MongoDB impossible", err)
	}
	defer func() { _ = st.Close(context.Background()) }()

	// Ni WhatsApp, ni équipe, ni veille : seulement de quoi lire et juger.
	app := api.NewServer(cfg, st, cipher, nil)
	var list []string
	for _, d := range strings.Split(*dossiers, ",") {
		if d = strings.TrimSpace(d); d != "" {
			list = append(list, d)
		}
	}
	if err := app.EssaiDiagnostic(ctx, *email, *nombre, list, os.Stdout); err != nil {
		fatal("essai interrompu", err)
	}
}

func fatal(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+" :", err)
	os.Exit(1)
}
