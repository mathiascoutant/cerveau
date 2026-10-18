// Commande cerveau-sonde : le relevé du VPS, à la demande de Cerveau.
//
// Installée sur l'hôte et attachée, dans ~/.ssh/authorized_keys, à la clé SSH
// de Cerveau :
//
//	restrict,command="/usr/local/bin/cerveau-sonde -registre /home/ubuntu/projects/cerveau/projets.json" ssh-ed25519 AAAA… cerveau-sonde
//
// La clé ne peut alors exécuter qu'elle, sans terminal, sans redirection de
// port. La demande arrive dans SSH_ORIGINAL_COMMAND ; la réponse part en JSON
// sur la sortie standard. En direct, la demande se passe en argument.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde/hote"
)

func main() {
	registry := flag.String("registre", "", "chemin du registre des projets (projets.json)")
	flag.Parse()

	line := os.Getenv("SSH_ORIGINAL_COMMAND")
	if line == "" && flag.NArg() > 0 {
		line = flag.Arg(flag.NArg() - 1)
	}

	data, err := run(line, *registry)
	resp := sonde.Reponse{Version: sonde.Version}
	if err != nil {
		resp.Erreur = err.Error()
	} else {
		resp.Donnees = data
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
	if err != nil {
		os.Exit(1)
	}
}

func run(line, registryPath string) (json.RawMessage, error) {
	req, err := sonde.Decode(line)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	loadRegistry := func() (*registre.Registre, error) {
		if registryPath == "" {
			return nil, fmt.Errorf("la sonde n'a pas de registre (option -registre)")
		}
		return registre.Charger(registryPath)
	}

	var out any
	switch req.Commande {
	case sonde.CmdSysteme:
		out = hote.Systeme(ctx)
	case sonde.CmdConteneurs:
		out, err = hote.Conteneurs(ctx)
	case sonde.CmdJournaux:
		lines, _ := strconv.Atoi(req.Args["lignes"])
		out, err = hote.Journaux(ctx, req.Args["conteneur"], lines, req.Args["filtre"], req.Args["depuis"])
	case sonde.CmdStats:
		out, err = hote.Stats(ctx)
	case sonde.CmdSites:
		out, err = hote.Sites()
	case sonde.CmdMongo:
		var reg *registre.Registre
		if reg, err = loadRegistry(); err == nil {
			out = hote.Mongo(ctx, reg, req.Args["base"])
		}
	case sonde.CmdExpliquer:
		var reg *registre.Registre
		if reg, err = loadRegistry(); err == nil {
			out, err = hote.Expliquer(ctx, reg, req.Args["base"], req.Args["collection"], req.Args["filtre"])
		}
	default:
		return nil, fmt.Errorf("commande inconnue : %q", req.Commande)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
