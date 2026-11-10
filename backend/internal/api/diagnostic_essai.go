package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// L'essai à blanc du diagnostic (cmd/diagblanc).
//
// Il rejoue, sur les derniers mails déjà arrivés, ce que la veille et les
// trois verrous auraient décidé — avec les mêmes consignes, les mêmes modèles
// et les mêmes fonctions qu'en vrai. Il n'écrit rien : ni sujet, ni
// diagnostic, ni mail marqué lu, et Hugo n'est jamais lancé. C'est la façon
// de juger le filtre sur pièces avant de le laisser tourner.

// essaiLot : les mails jugés ensemble, comme dans une relève.
const essaiLot = veilleMailsLus

// EssaiDiagnostic écrit dans w ce que le filtre aurait fait des `nombre`
// derniers mails de chaque dossier.
func (s *Server) EssaiDiagnostic(ctx context.Context, email string, nombre int, dossiers []string, w io.Writer) error {
	user, err := s.store.UserByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("compte %s : %w", email, err)
	}
	creds, err := s.gandiCreds(ctx, user)
	if err != nil {
		return fmt.Errorf("boîte mail pro : %w", err)
	}
	var mails []gandi.Message
	vus := map[string]bool{}
	for _, d := range dossiers {
		found, err := gandi.Parcourir(ctx, creds, d, nombre, false)
		if err != nil {
			fmt.Fprintf(w, "Dossier %s illisible : %v\n", d, err)
			continue
		}
		for _, f := range found {
			if f.MessageID == "" || vus[f.MessageID] {
				continue
			}
			vus[f.MessageID] = true
			mails = append(mails, f.Message)
		}
	}
	sort.SliceStable(mails, func(i, j int) bool { return mails[i].Date.After(mails[j].Date) })
	items := triage.Mails(triageMails(mails), creds.Email)
	fmt.Fprintf(w, "ESSAI À BLANC — %d mails lus (%s), dont %d qui te sont adressés par une personne.\n", len(mails), strings.Join(dossiers, ", "), len(items))
	fmt.Fprintln(w, "Rien n'est écrit, aucun mail n'est marqué lu, Hugo n'est pas lancé.")
	if n := len(mails) - len(items); n > 0 {
		fmt.Fprintf(w, "%d mails écartés avant tout jugement : copie seule, diffusion ou expéditeur automatique.\n", n)
	}

	tb := s.toolbox(user)
	var total, problemes, remontes int
	for debut := 0; debut < len(items); debut += essaiLot {
		lot := items[debut:min(debut+essaiLot, len(items))]
		s.lireMailsNeufs(ctx, user, lot)
		jctx, cancel := context.WithTimeout(ctx, veilleTimeout)
		out, err := s.jugerVeille(jctx, tb, "mails pro", nil, nil, map[string]int{}, lot)
		cancel()
		if err != nil {
			fmt.Fprintf(w, "\nLot %d : jugement en échec : %v\n", debut/essaiLot+1, err)
			continue
		}
		var j jugement
		if err := json.Unmarshal([]byte(objetJSON(out)), &j); err != nil {
			fmt.Fprintf(w, "\nLot %d : jugement illisible : %v\n", debut/essaiLot+1, err)
			continue
		}
		signales := map[int]bool{}
		for _, suj := range j.Sujets {
			var its []triage.Item
			for _, n := range suj.Messages {
				if n >= 1 && n <= len(lot) {
					its = append(its, lot[n-1])
					signales[n-1] = true
				}
			}
			total++
			fmt.Fprintf(w, "\n— SUJET : %s\n", suj.Titre)
			for _, it := range its {
				fmt.Fprintf(w, "  mail : « %s » · de %s · %s · %s\n", it.Titre, it.De, it.Quand.Format("02/01 15:04"), adresseA(it))
			}
			fmt.Fprintf(w, "  résumé : %s\n", suj.Resume)
			if strings.TrimSpace(suj.Probleme.Quoi) == "" {
				fmt.Fprintln(w, "  → pas un problème pour la veille : sujet signalé, rien pour Hugo.")
				continue
			}
			problemes++
			fmt.Fprintf(w, "  PROBLÈME vu par la veille : %s\n", suj.Probleme.Quoi)
			fmt.Fprintf(w, "    pour : %s", suj.Probleme.Pour)
			if suj.Probleme.AQui != "" {
				fmt.Fprintf(w, " (à %s)", suj.Probleme.AQui)
			}
			fmt.Fprintf(w, " · preuve citée : « %s »\n", suj.Probleme.Preuve)
			cctx, cancel := context.WithTimeout(ctx, controleDuree)
			debutControle := time.Now()
			v := s.trancher(cctx, "mail", remontee{p: suj.Probleme, items: its}, true)
			cancel()
			if v.Remonter {
				remontes++
				fmt.Fprintf(w, "  → REMONTÉ À HUGO (contrôle en %s) : %s\n    preuve : « %s »\n    problème retenu : %s\n", time.Since(debutControle).Round(time.Second), v.Raison, v.Preuve, v.Quoi)
				continue
			}
			fmt.Fprintf(w, "  → NON REMONTÉ (verrou : %s) : %s\n", v.Verrou, v.Raison)
			if v.Preuve != "" {
				fmt.Fprintf(w, "    preuve : « %s »\n", v.Preuve)
			}
		}
		for i, it := range lot {
			if !signales[i] {
				fmt.Fprintf(w, "\n· non signalé par la veille : « %s » · de %s · %s\n", it.Titre, it.De, it.Quand.Format("02/01 15:04"))
			}
		}
	}
	fmt.Fprintf(w, "\nBILAN : %d sujets signalés, %d problèmes vus, %d remontés à Hugo.\n", total, problemes, remontes)
	return nil
}
