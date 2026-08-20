// Commande gandicheck : teste les identifiants IMAP Gandi en isolation.
//
// Utile avant de brancher la boîte mail dans l'app : ni MongoDB, ni OpenAI, ni
// serveur HTTP. Si ça passe ici, ça passera dans Raoul.
//
//	go run ./cmd/gandicheck -email moi@mondomaine.fr
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
)

func main() {
	email := flag.String("email", os.Getenv("GANDI_EMAIL"), "adresse de la boîte mail Gandi")
	host := flag.String("host", gandi.DefaultHost, "serveur IMAP")
	limit := flag.Int("limit", 10, "nombre de mails non lus à afficher")
	flag.Parse()

	if *email == "" {
		fmt.Fprintln(os.Stderr, "Il faut une adresse : -email moi@mondomaine.fr")
		os.Exit(2)
	}

	password, err := readPassword()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Lecture du mot de passe :", err)
		os.Exit(2)
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "Mot de passe vide.")
		os.Exit(2)
	}

	creds := gandi.Credentials{Email: *email, Password: password, Host: *host}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("Connexion à %s en tant que %s…\n", *host, *email)
	if err := gandi.TestConnection(ctx, creds); err != nil {
		fmt.Fprintln(os.Stderr, "\n❌ Échec :", err)
		fmt.Fprintln(os.Stderr, "\nPistes :")
		fmt.Fprintln(os.Stderr, "  • mot de passe d'application requis si la 2FA est active sur la boîte")
		fmt.Fprintln(os.Stderr, "  • l'identifiant est l'adresse complète, pas seulement la partie avant @")
		fmt.Fprintln(os.Stderr, "  • vérifier que l'accès IMAP est activé côté Gandi")
		os.Exit(1)
	}
	fmt.Println("✅ Identifiants acceptés, INBOX accessible.")

	count, err := gandi.UnreadCount(ctx, creds)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Comptage des non lus :", err)
		os.Exit(1)
	}
	fmt.Printf("\n%d mail(s) non lu(s).\n", count)

	if count == 0 {
		fmt.Println("\nRaoul fonctionnera, il n'aura simplement rien à signaler côté mail.")
		return
	}

	messages, err := gandi.Unread(ctx, creds, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Lecture des non lus :", err)
		os.Exit(1)
	}

	fmt.Println("\nCe que Raoul verra (les plus récents d'abord) :")
	for _, m := range messages {
		subject := m.Subject
		if subject == "" {
			subject = "(sans objet)"
		}
		fmt.Printf("  • %s — %s — %s\n", m.Date.Local().Format("02/01 15h04"), m.From, subject)
	}
	fmt.Println("\nSeuls l'expéditeur, l'objet et la date sortent de ta boîte : le corps des")
	fmt.Println("messages n'est jamais lu ni transmis au modèle.")
}

// readPassword privilégie la saisie interactive masquée, pour éviter que le mot
// de passe traîne dans l'historique du shell.
func readPassword() (string, error) {
	if fromEnv := os.Getenv("GANDI_PASSWORD"); fromEnv != "" {
		return fromEnv, nil
	}
	fmt.Print("Mot de passe (masqué) : ")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return strings.TrimSpace(string(raw)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}
