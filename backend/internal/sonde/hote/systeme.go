// Package hote est la sonde côté machine : ce qu'elle sait relever sur le VPS,
// et rien d'autre. Chaque fonction est une lecture ; aucune n'écrit, ne
// redémarre ni ne modifie quoi que ce soit.
package hote

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
)

// Chemins lus. Variables plutôt que constantes : les tests les font pointer
// sur des fichiers à eux.
var (
	osRelease     = "/etc/os-release"
	distroInfo    = "/usr/share/distro-info/ubuntu.csv"
	procUptime    = "/proc/uptime"
	procLoadavg   = "/proc/loadavg"
	procMeminfo   = "/proc/meminfo"
	procCPUInfo   = "/proc/cpuinfo"
	procOSRelease = "/proc/sys/kernel/osrelease"
	bootDir       = "/boot"
	aptLists      = "/var/lib/apt/lists"
	rebootFlag    = "/var/run/reboot-required"
	rebootPkgs    = "/var/run/reboot-required.pkgs"
	disksToReport = []string{"/"}
)

// Systeme relève l'état de la machine. Ce qui échoue est dit dans les
// avertissements plutôt que d'arrêter le relevé : un apt qui ne répond pas ne
// doit pas cacher un disque plein.
func Systeme(ctx context.Context) sonde.Systeme {
	var s sonde.Systeme
	warn := func(format string, args ...any) {
		s.Avertissements = append(s.Avertissements, fmt.Sprintf(format, args...))
	}

	s.Hote, _ = os.Hostname()

	if rel, err := readKeyValues(osRelease); err == nil {
		s.OS = rel["PRETTY_NAME"]
		s.Version = rel["VERSION_ID"]
		s.NomCode = rel["VERSION_CODENAME"]
		s.LTS = strings.Contains(rel["VERSION"], "LTS")
	} else {
		warn("version du système illisible : %v", err)
	}
	if s.NomCode != "" {
		if eol, err := endOfSupport(distroInfo, s.NomCode); err == nil && eol != "" {
			s.FinSupport = eol
			if day, err := time.Parse("2006-01-02", eol); err == nil && time.Now().After(day) {
				s.HorsSupport = true
			}
		}
	}

	if raw, err := os.ReadFile(procOSRelease); err == nil {
		s.Noyau = strings.TrimSpace(string(raw))
	}
	s.NoyauInstalle = latestKernel(bootDir)

	if raw, err := os.ReadFile(procUptime); err == nil {
		if f := strings.Fields(string(raw)); len(f) > 0 {
			if secs, err := strconv.ParseFloat(f[0], 64); err == nil {
				s.AllumeDepuisS = int64(secs)
			}
		}
	}
	if raw, err := os.ReadFile(procLoadavg); err == nil {
		fields := strings.Fields(string(raw))
		for i := 0; i < 3 && i < len(fields); i++ {
			v, _ := strconv.ParseFloat(fields[i], 64)
			s.Charge = append(s.Charge, v)
		}
	}
	s.Processeurs = countCPUs(procCPUInfo)
	s.Memoire = readMemory(procMeminfo)

	for _, point := range disksToReport {
		var st syscall.Statfs_t
		if err := syscall.Statfs(point, &st); err != nil {
			warn("disque %s illisible : %v", point, err)
			continue
		}
		total := float64(st.Blocks) * float64(st.Bsize)
		free := float64(st.Bavail) * float64(st.Bsize)
		used := total - float64(st.Bfree)*float64(st.Bsize)
		pct := 0
		if used+free > 0 {
			pct = int(used / (used + free) * 100)
		}
		s.Disques = append(s.Disques, sonde.Disque{
			Point: point, TotalGo: round1(total / 1e9), LibreGo: round1(free / 1e9), Pct: pct,
		})
	}

	s.MisesAJour, s.MisesAJourSecurite = -1, -1
	if pkgs, err := upgradable(ctx); err == nil {
		s.MisesAJour = len(pkgs)
		s.MisesAJourSecurite = 0
		for _, p := range pkgs {
			if strings.Contains(p, "-security") {
				s.MisesAJourSecurite++
			}
		}
		sort.Strings(pkgs)
		if len(pkgs) > 40 {
			pkgs = pkgs[:40]
		}
		s.Paquets = pkgs
	} else {
		warn("mises à jour en attente inconnues : %v", err)
	}
	if fi, err := os.Stat(aptLists); err == nil {
		s.IndexAPT = fi.ModTime().UTC().Format(time.RFC3339)
	}

	if _, err := os.Stat(rebootFlag); err == nil {
		s.RedemarrageRequis = true
		if raw, err := os.ReadFile(rebootPkgs); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					s.RaisonRedemarrage = append(s.RaisonRedemarrage, line)
				}
			}
		}
	}

	if v, err := dockerVersion(ctx); err == nil {
		s.Docker = v
	}
	return s
}

// readKeyValues lit un fichier au format os-release : CLÉ=valeur, guillemets
// facultatifs.
func readKeyValues(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return out, sc.Err()
}

// endOfSupport cherche la fin du support standard d'une version dans la table
// que la machine tient elle-même à jour (paquet distro-info-data). Mieux
// qu'une table recopiée ici : elle suit les annonces de Canonical, et elle
// répond pour la version qui tourne, quelle qu'elle soit.
func endOfSupport(path, codename string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return "", err
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	series, okS := col["series"]
	eol, okE := col["eol"]
	if !okS || !okE {
		return "", fmt.Errorf("colonnes inattendues dans %s", path)
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if len(rec) > eol && len(rec) > series && rec[series] == codename {
			return strings.TrimSpace(rec[eol]), nil
		}
	}
}

// latestKernel rend la version du noyau le plus récent installé dans /boot.
func latestKernel(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "vmlinuz-*"))
	var versions []string
	for _, m := range matches {
		versions = append(versions, strings.TrimPrefix(filepath.Base(m), "vmlinuz-"))
	}
	if len(versions) == 0 {
		return ""
	}
	sort.Slice(versions, func(i, j int) bool { return kernelLess(versions[i], versions[j]) })
	return versions[len(versions)-1]
}

// kernelLess compare deux versions de noyau Ubuntu (« 6.14.0-37-generic »)
// nombre par nombre : « 6.14.0-9 » est plus vieux que « 6.14.0-37 », ce
// qu'un tri alphabétique dirait à l'envers.
func kernelLess(a, b string) bool {
	na, nb := kernelNumbers(a), kernelNumbers(b)
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	return len(na) < len(nb)
}

func kernelNumbers(v string) []int {
	var out []int
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' }) {
		n, _ := strconv.Atoi(part)
		out = append(out, n)
	}
	return out
}

func countCPUs(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "\nprocessor") + boolInt(strings.HasPrefix(string(raw), "processor"))
}

func readMemory(path string) sonde.Memoire {
	values := map[string]int64{}
	f, err := os.Open(path)
	if err != nil {
		return sonde.Memoire{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[strings.TrimSuffix(fields[0], ":")] = kb
	}
	return sonde.Memoire{
		TotalMo:      values["MemTotal"] / 1024,
		DisponibleMo: values["MemAvailable"] / 1024,
		SwapMo:       values["SwapTotal"] / 1024,
		SwapLibreMo:  values["SwapFree"] / 1024,
	}
}

// upgradable liste les paquets en attente, tels qu'apt les voit d'après sa
// dernière mise à jour d'index. apt list ne demande aucun droit particulier.
func upgradable(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "apt", "list", "--upgradable")
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseUpgradable(string(out)), nil
}

// parseUpgradable garde une ligne par paquet : « nom/suite version ».
func parseUpgradable(out string) []string {
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Listing") || strings.HasPrefix(line, "WARNING") || !strings.Contains(line, "/") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			pkgs = append(pkgs, fields[0]+" "+fields[1])
		} else {
			pkgs = append(pkgs, fields[0])
		}
	}
	return pkgs
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
