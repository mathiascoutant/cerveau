package hote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
)

// Docker, par son API et non par sa ligne de commande : la sortie de
// « docker ps » est faite pour des yeux, celle de l'API pour un programme.
// La sonde ne fait que des GET — lister, inspecter, lire les journaux et la
// consommation. Aucun appel ne démarre, n'arrête ni ne supprime quoi que ce
// soit, et aucun ne le pourrait : il n'y a pas d'autre méthode dans ce fichier.

var dockerSocket = "/var/run/docker.sock"

func dockerClient() *http.Client {
	return &http.Client{
		Timeout: 25 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", dockerSocket)
			},
		},
	}
}

func dockerGet(ctx context.Context, path string, query url.Values, into any) error {
	raw, err := dockerRaw(ctx, path, query)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func dockerRaw(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := "http://docker" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := dockerClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("Docker injoignable : %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("conteneur introuvable")
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Docker : statut %d : %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return body, nil
}

func dockerVersion(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"Version"`
	}
	if err := dockerGet(ctx, "/version", nil, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

type apiContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Ports   []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
	} `json:"Ports"`
}

type apiInspect struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		StartedAt string `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Tty bool `json:"Tty"`
	} `json:"Config"`
}

// Conteneurs liste tous les conteneurs, arrêtés compris — un service tombé est
// justement celui qu'on cherche.
func Conteneurs(ctx context.Context) ([]sonde.Conteneur, error) {
	var list []apiContainer
	if err := dockerGet(ctx, "/containers/json", url.Values{"all": {"1"}}, &list); err != nil {
		return nil, err
	}
	out := make([]sonde.Conteneur, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		out[i] = fromAPI(c)
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			var in apiInspect
			if err := dockerGet(ctx, "/containers/"+id+"/json", nil, &in); err != nil {
				return
			}
			out[i].Redemarrages = in.RestartCount
			if in.State.Health != nil {
				out[i].Sante = in.State.Health.Status
			}
			if t, err := time.Parse(time.RFC3339Nano, in.State.StartedAt); err == nil && t.Year() > 1 {
				out[i].DemarreLe = t.UTC().Format(time.RFC3339)
			}
		}(i, c.ID)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Nom < out[j].Nom })
	return out, nil
}

func fromAPI(c apiContainer) sonde.Conteneur {
	name := c.ID
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	out := sonde.Conteneur{
		Nom:         name,
		Image:       c.Image,
		Etat:        c.State,
		Statut:      c.Status,
		Revision:    c.Labels["org.opencontainers.image.revision"],
		VersionOCI:  c.Labels["org.opencontainers.image.version"],
		ImageCreeLe: c.Labels["org.opencontainers.image.created"],
		Projet:      c.Labels["com.docker.compose.project"],
	}
	seen := map[string]bool{}
	for _, p := range c.Ports {
		key := fmt.Sprintf("%s:%d:%d", p.IP, p.PublicPort, p.PrivatePort)
		if seen[key] {
			continue
		}
		seen[key] = true
		out.Ports = append(out.Ports, sonde.Port{
			IP: p.IP, Hote: p.PublicPort, Prive: p.PrivatePort,
			Public: p.PublicPort > 0 && (p.IP == "0.0.0.0" || p.IP == "::" || p.IP == ""),
		})
	}
	return out
}

// containerName : les noms de conteneurs que la sonde accepte. Pas de barre,
// pas d'espace — de quoi désigner un conteneur, rien de quoi construire un
// autre chemin d'API.
var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Journaux rend la fin des journaux d'un conteneur, filtrée si on le demande.
//
// Le filtre est appliqué ici et pas par le modèle : rapatrier cinq mille lignes
// pour en garder vingt qui parlent de « login » coûterait cinq mille lignes de
// contexte à chaque question.
func Journaux(ctx context.Context, name string, lines int, filter, since string) (sonde.Journaux, error) {
	if !containerName.MatchString(name) {
		return sonde.Journaux{}, fmt.Errorf("nom de conteneur invalide : %q", name)
	}
	if lines <= 0 {
		lines = 80
	}
	if lines > 500 {
		lines = 500
	}
	var re *regexp.Regexp
	if filter = strings.TrimSpace(filter); filter != "" {
		if len(filter) > 200 {
			return sonde.Journaux{}, errors.New("filtre trop long")
		}
		var err error
		if re, err = regexp.Compile("(?i)" + filter); err != nil {
			return sonde.Journaux{}, fmt.Errorf("filtre invalide : %w", err)
		}
	}

	var in apiInspect
	if err := dockerGet(ctx, "/containers/"+name+"/json", nil, &in); err != nil {
		return sonde.Journaux{}, err
	}

	tail := lines
	if re != nil {
		// Filtrer, c'est chercher plus loin : on lit large pour garder assez
		// de lignes qui correspondent.
		tail = lines * 40
		if tail > 8000 {
			tail = 8000
		}
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(tail)}}
	if since = strings.TrimSpace(since); since != "" {
		ts, err := parseSince(since)
		if err != nil {
			return sonde.Journaux{}, err
		}
		q.Set("since", strconv.FormatInt(ts.Unix(), 10))
	}
	raw, err := dockerRaw(ctx, "/containers/"+name+"/logs", q)
	if err != nil {
		return sonde.Journaux{}, err
	}
	all := splitLogs(raw, in.Config.Tty)

	out := sonde.Journaux{Conteneur: name, Filtre: filter, Parcourues: len(all)}
	for _, line := range all {
		if re == nil || re.MatchString(line) {
			out.Lignes = append(out.Lignes, truncateLine(line, 600))
		}
	}
	if len(out.Lignes) > lines {
		out.Lignes = out.Lignes[len(out.Lignes)-lines:]
	}
	return out, nil
}

// parseSince accepte une durée (« 30m », « 2h », « 7d ») ou un instant RFC 3339.
func parseSince(s string) (time.Time, error) {
	s = sonde.DaysToHours(s)
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("début des journaux illisible : %q (une durée comme 30m ou 7d, ou une date RFC 3339)", s)
}

// splitLogs découpe la réponse de /logs en lignes. Sans terminal, Docker
// multiplexe stdout et stderr par trames : huit octets d'en-tête (le flux,
// trois octets nuls, la longueur), puis le contenu.
func splitLogs(raw []byte, tty bool) []string {
	var text bytes.Buffer
	if tty {
		text.Write(raw)
	} else {
		for len(raw) >= 8 {
			size := int(binary.BigEndian.Uint32(raw[4:8]))
			raw = raw[8:]
			if size > len(raw) {
				size = len(raw)
			}
			text.Write(raw[:size])
			raw = raw[size:]
		}
	}
	var lines []string
	sc := bufio.NewScanner(&text)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if line := strings.TrimRight(sc.Text(), "\r"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func truncateLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Stats relève la consommation des conteneurs en marche. Docker calcule le
// processeur sur un intervalle d'environ une seconde : les relevés partent en
// parallèle pour que la commande ne dure pas une seconde par conteneur.
func Stats(ctx context.Context) ([]sonde.Stat, error) {
	var list []apiContainer
	if err := dockerGet(ctx, "/containers/json", nil, &list); err != nil {
		return nil, err
	}
	out := make([]sonde.Stat, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		out[i].Nom = fromAPI(c).Nom
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			var st struct {
				CPU struct {
					Usage struct {
						Total uint64 `json:"total_usage"`
					} `json:"cpu_usage"`
					System uint64 `json:"system_cpu_usage"`
					Online int    `json:"online_cpus"`
				} `json:"cpu_stats"`
				PreCPU struct {
					Usage struct {
						Total uint64 `json:"total_usage"`
					} `json:"cpu_usage"`
					System uint64 `json:"system_cpu_usage"`
				} `json:"precpu_stats"`
				Memory struct {
					Usage uint64            `json:"usage"`
					Limit uint64            `json:"limit"`
					Stats map[string]uint64 `json:"stats"`
				} `json:"memory_stats"`
			}
			if err := dockerGet(ctx, "/containers/"+id+"/stats", url.Values{"stream": {"false"}}, &st); err != nil {
				return
			}
			cpuDelta := float64(st.CPU.Usage.Total) - float64(st.PreCPU.Usage.Total)
			sysDelta := float64(st.CPU.System) - float64(st.PreCPU.System)
			online := st.CPU.Online
			if online == 0 {
				online = 1
			}
			if cpuDelta > 0 && sysDelta > 0 {
				out[i].CPU = round1(cpuDelta / sysDelta * float64(online) * 100)
			}
			// La mémoire « utilisée » de Docker compte le cache de fichiers,
			// que le noyau rend dès qu'on le lui demande : on le retire, comme
			// le fait docker stats.
			used := float64(st.Memory.Usage) - float64(st.Memory.Stats["inactive_file"])
			if used < 0 {
				used = float64(st.Memory.Usage)
			}
			out[i].MemoireMo = round1(used / (1 << 20))
			if st.Memory.Limit > 0 {
				out[i].LimiteMo = round1(float64(st.Memory.Limit) / (1 << 20))
			}
		}(i, c.ID)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Nom < out[j].Nom })
	return out, nil
}
