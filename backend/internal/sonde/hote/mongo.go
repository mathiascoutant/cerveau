package hote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
)

// Les bases des projets, examinées sur place.
//
// L'URI de chaque base reste dans le fichier .env de son projet, sur l'hôte. La
// sonde l'y lit, se connecte, et ne rend que ce qu'elle a constaté — jamais
// l'URI, jamais un identifiant. Et elle ne fait que lire : ping, statistiques,
// liste des collections et de leurs index, plan d'une recherche. Aucune
// écriture, aucune commande d'administration.

const mongoTimeout = 12 * time.Second

// target lit l'URI d'une base et le nom de la base à examiner.
func target(b registre.Base) (uri, dbName string, err error) {
	uri, err = envValue(b.FichierEnv, b.Variable)
	if err != nil {
		return "", "", err
	}
	dbName = b.NomBase
	if dbName == "" && b.VariableBase != "" {
		if v, err := envValue(b.FichierEnv, b.VariableBase); err == nil {
			dbName = v
		}
	}
	return uri, dbName, nil
}

// Mongo examine une base du registre.
func Mongo(ctx context.Context, reg *registre.Registre, id string) sonde.Mongo {
	out := sonde.Mongo{Base: id, Etat: "erreur"}
	b, ok := reg.Base(id)
	if !ok {
		out.Erreur = fmt.Sprintf("base %q absente du registre", id)
		return out
	}
	uri, dbName, err := target(b)
	if err != nil {
		out.Erreur = err.Error()
		return out
	}
	out.Hotes = uriHosts(uri)

	ctx, cancel := context.WithTimeout(ctx, mongoTimeout)
	defer cancel()

	started := time.Now()
	client, db, err := connect(ctx, uri, dbName)
	if err != nil {
		out.Erreur = cleanMongoError(err, uri)
		return out
	}
	out.NomBase = db.Name()
	defer func() { _ = client.Disconnect(context.Background()) }()

	// Le premier ping ouvre la connexion — TCP, TLS, authentification — et
	// mesure donc ce que paie une application qui démarre. Le second mesure
	// l'aller-retour seul : ce que coûte chaque requête une fois connecté,
	// le chiffre qui explique qu'un login à trois requêtes prenne une
	// seconde et demie.
	if err := client.Ping(ctx, nil); err != nil {
		out.Erreur = cleanMongoError(err, uri)
		return out
	}
	out.ConnexionMs = time.Since(started).Milliseconds()
	out.Etat = "ok"
	pingStart := time.Now()
	if err := client.Ping(ctx, nil); err == nil {
		out.PingMs = time.Since(pingStart).Milliseconds()
	}

	var build struct {
		Version string `bson:"version"`
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&build); err == nil {
		out.Version = build.Version
	}

	var stats struct {
		DataSize    float64 `bson:"dataSize"`
		StorageSize float64 `bson:"storageSize"`
		IndexSize   float64 `bson:"indexSize"`
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&stats); err == nil {
		out.DonneesMo = round1(stats.DataSize / (1 << 20))
		out.StockageMo = round1(stats.StorageSize / (1 << 20))
		out.IndexMo = round1(stats.IndexSize / (1 << 20))
	}

	// serverStatus est souvent refusé sur les offres partagées d'Atlas : son
	// absence n'est pas une panne, on s'en passe.
	var status struct {
		Connections struct {
			Current   int `bson:"current"`
			Available int `bson:"available"`
		} `bson:"connections"`
	}
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&status); err == nil && status.Connections.Current > 0 {
		out.Connexions = &sonde.Connexions{Actuelles: status.Connections.Current, Disponibles: status.Connections.Available}
	}

	names, err := db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		out.Erreur = "collections illisibles : " + cleanMongoError(err, uri)
		return out
	}
	sort.Strings(names)
	if len(names) > 60 {
		names = names[:60]
	}
	for _, name := range names {
		coll := db.Collection(name)
		info := sonde.Collection{Nom: name}
		if n, err := coll.EstimatedDocumentCount(ctx); err == nil {
			info.Documents = n
		}
		if specs, err := coll.Indexes().ListSpecifications(ctx); err == nil {
			for _, s := range specs {
				label := s.Name
				if s.Unique != nil && *s.Unique {
					label += " (unique)"
				}
				info.Index = append(info.Index, label)
			}
		}
		out.Collections = append(out.Collections, info)
	}
	return out
}

// Expliquer rend le plan d'exécution d'une recherche : est-ce qu'elle passe par
// un index, combien de documents elle a dû lire pour en rendre combien. C'est
// la question qu'on se pose devant une connexion lente — un find sur l'adresse
// mail qui parcourt toute la collection des utilisateurs.
func Expliquer(ctx context.Context, reg *registre.Registre, id, collection, filter string) (sonde.Explication, error) {
	out := sonde.Explication{Base: id, Collection: collection, Filtre: filter}
	if collection == "" || strings.ContainsAny(collection, "$\x00") || len(collection) > 120 {
		return out, errors.New("nom de collection invalide")
	}
	b, ok := reg.Base(id)
	if !ok {
		return out, fmt.Errorf("base %q absente du registre", id)
	}
	if strings.TrimSpace(filter) == "" {
		filter = "{}"
	}
	if len(filter) > 2000 {
		return out, errors.New("filtre trop long")
	}
	var query bson.D
	if err := bson.UnmarshalExtJSON([]byte(filter), false, &query); err != nil {
		return out, fmt.Errorf("filtre JSON invalide : %w", err)
	}
	uri, dbName, err := target(b)
	if err != nil {
		return out, err
	}

	ctx, cancel := context.WithTimeout(ctx, mongoTimeout)
	defer cancel()
	client, db, err := connect(ctx, uri, dbName)
	if err != nil {
		return out, errors.New(cleanMongoError(err, uri))
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	// Lu en document brut plutôt qu'en carte : le pilote décode les
	// sous-documents d'une carte en listes ordonnées, et le plan se lit en
	// descendant de document en document.
	res, err := db.RunCommand(ctx, bson.D{
		{Key: "explain", Value: bson.D{
			{Key: "find", Value: collection},
			{Key: "filter", Value: query},
			{Key: "limit", Value: 50},
		}},
		{Key: "verbosity", Value: "executionStats"},
	}).Raw()
	if err != nil {
		return out, errors.New(cleanMongoError(err, uri))
	}

	if winning, ok := res.Lookup("queryPlanner", "winningPlan").DocumentOK(); ok {
		stages, index := planStages(winning)
		out.Etapes = strings.Join(stages, " > ")
		out.Index = index
		for _, s := range stages {
			if s == "COLLSCAN" {
				out.ParcoursComplet = true
			}
		}
	}
	out.Rendus = rawInt(res.Lookup("executionStats", "nReturned"))
	out.DocumentsExamines = rawInt(res.Lookup("executionStats", "totalDocsExamined"))
	out.ClesExaminees = rawInt(res.Lookup("executionStats", "totalKeysExamined"))
	out.DureeMs = rawInt(res.Lookup("executionStats", "executionTimeMillis"))
	if n, err := db.Collection(collection).EstimatedDocumentCount(ctx); err == nil {
		out.DocumentsCollection = n
	}
	return out, nil
}

// planStages descend le plan gagnant de la racine à la feuille. Les versions
// récentes enveloppent le plan dans queryPlan, les anciennes non ; MongoDB 8
// sert les recherches simples par une étape unique (EXPRESS_IXSCAN).
func planStages(plan bson.Raw) ([]string, string) {
	if inner, ok := plan.Lookup("queryPlan").DocumentOK(); ok {
		plan = inner
	}
	var stages []string
	index := ""
	for depth := 0; plan != nil && depth < 12; depth++ {
		if stage, ok := plan.Lookup("stage").StringValueOK(); ok {
			stages = append(stages, stage)
		}
		if name, ok := plan.Lookup("indexName").StringValueOK(); ok && index == "" {
			index = name
		}
		next, ok := plan.Lookup("inputStage").DocumentOK()
		if !ok {
			if arr, ok := plan.Lookup("inputStages").ArrayOK(); ok {
				if first, err := arr.IndexErr(0); err == nil {
					next, _ = first.DocumentOK()
				}
			}
		}
		plan = next
	}
	return stages, index
}

// rawInt lit un compteur, quel que soit le type numérique que le serveur a
// choisi pour lui.
func rawInt(v bson.RawValue) int64 {
	switch v.Type {
	case bson.TypeInt32:
		return int64(v.Int32())
	case bson.TypeInt64:
		return v.Int64()
	case bson.TypeDouble:
		return int64(v.Double())
	}
	return 0
}

func connect(ctx context.Context, uri, dbName string) (*mongo.Client, *mongo.Database, error) {
	opts := options.Client().ApplyURI(uri).
		SetServerSelectionTimeout(8 * time.Second).
		SetConnectTimeout(8 * time.Second).
		SetAppName("cerveau-sonde")
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, nil, err
	}
	if dbName == "" {
		dbName = uriDatabase(uri)
	}
	if dbName == "" {
		_ = client.Disconnect(context.Background())
		return nil, nil, errors.New("aucune base désignée : renseigne « base » dans le registre")
	}
	return client, client.Database(dbName), nil
}

// envValue lit une variable dans un fichier .env, sans l'exporter ni la
// garder : elle sert le temps de la connexion.
func envValue(path, key string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("fichier de configuration du projet illisible : %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if v == "" {
			break
		}
		return v, nil
	}
	return "", fmt.Errorf("variable %s absente du fichier de configuration du projet", key)
}

// cleanMongoError retire de l'erreur tout ce qui pourrait porter un secret : le
// pilote cite parfois l'URI, identifiants compris.
func cleanMongoError(err error, uri string) string {
	msg := err.Error()
	if uri != "" {
		msg = strings.ReplaceAll(msg, uri, "<uri>")
	}
	if pass := uriPassword(uri); pass != "" {
		msg = strings.ReplaceAll(msg, pass, "<secret>")
		if decoded, err := url.PathUnescape(pass); err == nil && decoded != pass {
			msg = strings.ReplaceAll(msg, decoded, "<secret>")
		}
	}
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

// uriAuthority rend ce qui sépare le schéma du chemin : « user:pass@h1,h2 ».
// À la main plutôt que par url.Parse : une URI Mongo peut lister plusieurs
// hôtes séparés par des virgules, ce que net/url refuse ; et le pilote, lui,
// résoudrait l'enregistrement SRV — un appel réseau pour lire une chaîne.
func uriAuthority(uri string) string {
	_, rest, ok := strings.Cut(uri, "://")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// uriPassword rend le mot de passe de l'URI, tel qu'il y est écrit.
func uriPassword(uri string) string {
	auth := uriAuthority(uri)
	at := strings.LastIndex(auth, "@")
	if at < 0 {
		return ""
	}
	_, pass, _ := strings.Cut(auth[:at], ":")
	return pass
}

// uriHosts rend les hôtes du cluster, sans identifiants.
func uriHosts(uri string) []string {
	auth := uriAuthority(uri)
	if at := strings.LastIndex(auth, "@"); at >= 0 {
		auth = auth[at+1:]
	}
	var out []string
	for _, h := range strings.Split(auth, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// uriDatabase rend la base nommée dans le chemin de l'URI, s'il y en a une.
func uriDatabase(uri string) string {
	_, rest, ok := strings.Cut(uri, "://")
	if !ok {
		return ""
	}
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	db := rest[slash+1:]
	if i := strings.Index(db, "?"); i >= 0 {
		db = db[:i]
	}
	return db
}
