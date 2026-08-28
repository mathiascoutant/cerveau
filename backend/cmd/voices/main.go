// Commande voices : liste les voix ElevenLabs accessibles au compte.
//
// Elle répond à une question précise et agaçante : « pourquoi Raoul prend-il
// l'accent anglais ? ». Parce que la voix par défaut est anglophone. Aucun
// réglage ne corrige l'accent d'une voix — il faut en prendre une française, et
// pour ça savoir lesquelles le compte propose.
//
//	go run ./cmd/voices
//
// Puis reporter l'identifiant choisi dans ELEVENLABS_VOICE_ID.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"

	"github.com/mathiascoutant/cerveau/backend/internal/tts"
)

func main() {
	_ = godotenv.Load()

	key := os.Getenv("ELEVENLABS_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "ELEVENLABS_API_KEY manquante (.env du backend).")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	voices, err := tts.New(key, "", "", "").Voices(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌", err)
		os.Exit(1)
	}
	if len(voices) == 0 {
		fmt.Println("Aucune voix sur ce compte.")
		return
	}

	current := os.Getenv("ELEVENLABS_VOICE_ID")
	if current == "" {
		current = tts.DefaultVoiceID
	}

	var french, others []tts.Voice
	for _, v := range voices {
		if v.SpeaksFrench() {
			french = append(french, v)
		} else {
			others = append(others, v)
		}
	}

	if len(french) > 0 {
		fmt.Println("Voix françaises — c'est là qu'il faut choisir :")
		show(french, current)
	} else {
		fmt.Println("Aucune voix française sur ce compte.")
		fmt.Println("Va la chercher dans la Voice Library d'ElevenLabs (filtre Language : French),")
		fmt.Println("ajoute-la à ton compte, puis relance cette commande.")
	}

	fmt.Println("\nAutres voix (elles liront le français avec leur accent) :")
	show(others, current)

	fmt.Println("\nReporte l'identifiant choisi dans ELEVENLABS_VOICE_ID, puis redémarre le serveur.")
}

func show(voices []tts.Voice, current string) {
	for _, v := range voices {
		mark := "  "
		if v.ID == current {
			mark = "→ "
		}
		labels := v.Language
		if v.Accent != "" {
			labels += " " + v.Accent
		}
		if labels == "" {
			labels = v.Category
		}
		fmt.Printf("%s%-24s %-28s %s\n", mark, v.ID, v.Name, labels)
	}
}
