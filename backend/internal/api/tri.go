package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/tri"
)

// Le tri des mails pro : à chaque relève, l'agent mail pro range les mails
// arrivés dans la réception depuis la dernière fois — les cycles de contenu
// des CSP dans « CSP », les mails de marques et de services dans « Autres ».
// Le reste ne bouge pas. Les dossiers sont ceux de la boîte IMAP : Thunderbird
// les montre tels quels.
//
// Le tri passe après le jugement de la veille, dans la même relève : un mail
// rangé quitte la réception, et la veille doit l'avoir vu avant.
//
// La première fois, le tri note la fin de la boîte sans rien ranger : il
// s'occupe de ce qui arrive, pas de l'existant.

const (
	triParReleve = 20
	triTimeout   = 2 * time.Minute
)

func (s *Server) trier(ctx context.Context, user *store.User, agent equipe.Membre) error {
	creds, err := s.gandiCreds(ctx, user)
	if err != nil {
		return err
	}
	// Les dossiers du tri existent dès le départ, une fois par lancement du
	// serveur : Mathias les voit dans Thunderbird sans attendre un mail.
	if _, deja := s.triDossiers.LoadOrStore(user.ID.Hex(), true); !deja {
		if err := gandi.CreerDossiers(ctx, creds, tri.Dossiers); err != nil {
			s.triDossiers.Delete(user.ID.Hex())
			slog.Warn("tri : dossiers non créés", "err", err)
		}
	}
	etat, ok, err := s.store.TriEtat(ctx, user.ID)
	if err != nil {
		return err
	}
	rel, arrivees, err := gandi.Arrivees(ctx, creds, etat.UIDValidity, etat.DernierUID, triParReleve)
	if err != nil {
		return err
	}
	if !ok || etat.UIDValidity != rel.UIDValidity {
		// Première relève, ou boîte renumérotée : on repart de sa fin.
		return s.store.TriNoter(ctx, user.ID, store.TriEtat{UIDValidity: rel.UIDValidity, DernierUID: rel.DernierUID})
	}
	if len(arrivees) == 0 {
		return nil
	}

	demande := fmt.Sprintf("Tri de %d nouveau%s mail%s", len(arrivees), plural(len(arrivees), "x", ""), plural(len(arrivees), "s", ""))
	run := s.runs.begin(ctx, user.ID, agent.ID, store.RunSourceVeille, demande, nil)
	run.Etape(assistant.Event{Type: "status", Tool: "tri", Label: "Tri des nouveaux mails"})
	started := time.Now()

	mails := make([]tri.Mail, len(arrivees))
	for i, a := range arrivees {
		de := a.From
		if a.FromAddr != "" && !strings.EqualFold(a.From, a.FromAddr) {
			de += " <" + a.FromAddr + ">"
		}
		mails[i] = tri.Mail{Adresse: a.FromAddr, De: de, Objet: a.Subject, A: a.To, PiecesJointes: a.PiecesJointes, Masse: a.Bulk, Texte: a.Body}
	}
	decisions, err := s.trieur.Classer(ctx, mails)
	run.Etape(assistant.Event{Type: "status", Tool: "tri", Ms: max(1, time.Since(started).Milliseconds())})
	if err != nil {
		// L'état n'avance pas : ces mails seront repris à la relève suivante.
		run.Fin(assistant.Result{}, err)
		return err
	}

	parDossier := map[string][]uint32{}
	var lignes []string
	for i, d := range decisions {
		if d.Dossier == tri.Reception {
			continue
		}
		parDossier[d.Dossier] = append(parDossier[d.Dossier], arrivees[i].UID)
		lignes = append(lignes, fmt.Sprintf("« %s » → %s", arrivees[i].Subject, d.Dossier))
		slog.Info("tri : mail rangé", "dossier", d.Dossier, "objet", arrivees[i].Subject, "raison", d.Raison)
	}
	if len(parDossier) > 0 {
		if err := gandi.Ranger(ctx, creds, rel.UIDValidity, parDossier); err != nil {
			run.Fin(assistant.Result{}, err)
			return err
		}
	}
	// Un mail de cycle qui apporte son Excel : l'équipe propose de lancer le
	// process CSP (csp.go). Mathias décide dans HQ.
	s.proposerApresTri(user, mailsDuTri(arrivees, decisions))
	dernier := etat.DernierUID
	for _, a := range arrivees {
		dernier = max(dernier, a.UID)
	}
	if err := s.store.TriNoter(ctx, user.ID, store.TriEtat{UIDValidity: rel.UIDValidity, DernierUID: dernier}); err != nil {
		slog.Warn("tri : avancement non noté", "err", err)
	}

	reply := fmt.Sprintf("%d mail%s laissé%s dans la réception.", len(arrivees), plural(len(arrivees), "s", ""), plural(len(arrivees), "s", ""))
	if len(lignes) > 0 {
		reply = fmt.Sprintf("%d mail%s rangé%s : %s.", len(lignes), plural(len(lignes), "s", ""), plural(len(lignes), "s", ""), strings.Join(lignes, " ; "))
	}
	run.Fin(assistant.Result{Reply: reply}, nil)
	return nil
}
