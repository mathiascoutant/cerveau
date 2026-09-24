// Commande account : crée un compte, ou rattache une adresse et un mot de passe
// à un utilisateur existant.
//
// Le rattachement est le cas qui compte. Avant les comptes, l'identité de Raoul
// était le téléphone : Gandi, Slack et WhatsApp ont été branchés sur un
// utilisateur « appareil ». Lui poser une adresse et un mot de passe le
// transforme en compte sans rien refaire — les connexions pointent sur son
// identifiant, qui ne change pas.
//
//	go run ./cmd/account -list
//	go run ./cmd/account -email moi@exemple.fr               # rattache au plus fourni
//	go run ./cmd/account -email moi@exemple.fr -user <id>    # rattache à celui-ci
//	go run ./cmd/account -email moi@exemple.fr -new          # compte vide
//
// Le mot de passe est demandé en saisie masquée ; -password existe pour les
// scripts, au prix de l'historique du shell.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/term"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func main() {
	_ = godotenv.Load()

	email := flag.String("email", "", "adresse du compte")
	password := flag.String("password", "", "mot de passe (sinon demandé en saisie masquée)")
	userID := flag.String("user", "", "identifiant de l'utilisateur existant à rattacher")
	fresh := flag.Bool("new", false, "créer un compte vide au lieu de rattacher un utilisateur existant")
	list := flag.Bool("list", false, "lister les utilisateurs et ce qu'ils ont branché")
	name := flag.String("name", "", "prénom (compte neuf uniquement)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := store.Connect(ctx, os.Getenv("MONGO_URI"), envOr("MONGO_DB", "cerveau"))
	if err != nil {
		fail("connexion MongoDB : %v", err)
	}
	defer st.Close(context.Background())

	candidates, err := st.AccountCandidates(ctx)
	if err != nil {
		fail("lecture des utilisateurs : %v", err)
	}

	if *list {
		printCandidates(candidates)
		return
	}
	if *email == "" {
		fail("-email est requis (ou -list pour voir les utilisateurs)")
	}

	pw := *password
	if pw == "" {
		pw = askPassword()
	}
	if len(pw) < 8 {
		fail("mot de passe trop court (8 caractères minimum)")
	}

	if existing, err := st.UserByEmail(ctx, *email); err == nil {
		// L'adresse a déjà un compte : on change son mot de passe, rien d'autre.
		if err := st.SetCredentials(ctx, existing.ID, *email, pw); err != nil {
			fail("mise à jour : %v", err)
		}
		fmt.Printf("✅ Mot de passe mis à jour pour %s (utilisateur %s).\n", store.NormalizeEmail(*email), existing.ID.Hex())
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		fail("recherche du compte : %v", err)
	}

	if *fresh {
		u, err := st.CreateAccount(ctx, *email, pw, *name, envOr("DEFAULT_TIMEZONE", "Europe/Paris"))
		if err != nil {
			fail("création : %v", err)
		}
		fmt.Printf("✅ Compte créé : %s (utilisateur %s), sans aucune connexion.\n", u.Email, u.ID.Hex())
		return
	}

	target, err := pickTarget(candidates, *userID)
	if err != nil {
		fail("%v", err)
	}
	if err := st.SetCredentials(ctx, target.User.ID, *email, pw); err != nil {
		fail("rattachement : %v", err)
	}
	fmt.Printf("✅ %s rattaché à l'utilisateur %s — connexions reprises : %s\n",
		store.NormalizeEmail(*email), target.User.ID.Hex(), joinOr(target.Connections, "aucune"))
}

// pickTarget choisit l'utilisateur à rattacher : celui demandé, sinon le plus
// fourni en connexions — à condition qu'il n'ait pas déjà une adresse.
func pickTarget(candidates []store.AccountCandidate, id string) (store.AccountCandidate, error) {
	if id != "" {
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			return store.AccountCandidate{}, fmt.Errorf("identifiant invalide : %s", id)
		}
		for _, c := range candidates {
			if c.User.ID == oid {
				return c, nil
			}
		}
		return store.AccountCandidate{}, fmt.Errorf("aucun utilisateur %s", id)
	}
	for _, c := range candidates {
		if c.User.Email == "" && len(c.Connections) > 0 {
			return c, nil
		}
	}
	return store.AccountCandidate{}, errors.New("aucun utilisateur sans compte avec des connexions ; utilise -user <id> ou -new")
}

func printCandidates(candidates []store.AccountCandidate) {
	if len(candidates) == 0 {
		fmt.Println("Aucun utilisateur.")
		return
	}
	for _, c := range candidates {
		fmt.Printf("%s  %-28s  %-10s  vu %s  connexions : %s\n",
			c.User.ID.Hex(), orDash(c.User.Email), orDash(c.User.Name),
			c.User.LastSeen.Format("2006-01-02 15:04"), joinOr(c.Connections, "aucune"))
	}
}

func askPassword() string {
	fmt.Print("Mot de passe : ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		fail("lecture du mot de passe : %v", err)
	}
	return string(raw)
}

func joinOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}
