#!/usr/bin/env python3
"""L'atelier : là où les agents de Cerveau font le vrai travail, avec Claude Code.

Cerveau tourne dans un conteneur, sans dépôts ni outils de développement. Quand
on confie une tâche à un agent, il l'envoie ici en SSH, avec une clé dédiée que
le serveur SSH attache à ce programme et à rien d'autre (command= dans
authorized_keys) : même volée, cette clé ne donne pas un shell, elle donne une
tâche à Claude Code.

L'atelier tourne sous l'utilisateur « atelier », sans sudo. Claude Code y est
connecté à l'abonnement Claude de Mathias (pas de clé API : la variable
ANTHROPIC_API_KEY est retirée de l'environnement), et y travaille sur des clones
des dépôts des projets, dans ~/depots.

Protocole : une ligne JSON sur l'entrée (la tâche), puis l'entrée reste ouverte
tant que Cerveau attend. Sur la sortie : une ligne {"type": "atelier", ...}
quand l'atelier est prêt, puis le flux stream-json de Claude Code tel quel.
Quand Cerveau ferme l'entrée (tâche annulée, délai dépassé, serveur arrêté),
Claude Code est arrêté.

Rien ici n'est installé par pip : la bibliothèque standard suffit.
"""

import fcntl
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

HOME = Path.home()
CONFIG = HOME / "atelier" / "config.json"
DEPOTS = HOME / "depots"
TRAVAIL = HOME / "travail"
CLAUDE = HOME / ".local" / "bin" / "claude"
SECRETS = HOME / "atelier" / "secrets.env"  # jetons des outils (600), jamais dans la consigne
MCP = HOME / "atelier" / "mcp.json"
GARDER_JOURS = 7


def emit(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def fail(message, code=2):
    emit({"type": "atelier", "event": "erreur", "message": message})
    sys.exit(code)


def config():
    try:
        return json.loads(CONFIG.read_text())
    except FileNotFoundError:
        fail(f"configuration absente : {CONFIG}")
    except json.JSONDecodeError as e:
        fail(f"configuration illisible : {e}")


def git(*args, cwd=None, timeout=120):
    return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, timeout=timeout)


def url(depot):
    """« propriétaire/dépôt » est sur GitHub ; sinon, l'adresse git complète
    (Gitea, par exemple : ssh://git@gitea.exemple:5151/equipe/projet.git)."""
    return depot if ":" in depot else f"git@github.com:{depot}.git"


def nom(depot):
    return depot.rstrip("/").split("/")[-1].removesuffix(".git")


def synchroniser(depots):
    """Clone ce qui manque, met à jour le reste. Les clones de ~/depots ne
    sont jamais modifiés à la main : on les remet sur la branche distante."""
    DEPOTS.mkdir(exist_ok=True)
    # Deux tâches simultanées ne mettent pas à jour les clones en même temps.
    verrou = open(HOME / "atelier" / ".synchro.lock", "w")
    fcntl.flock(verrou, fcntl.LOCK_EX)
    prets, erreurs = [], []
    for depot in depots:
        dest = DEPOTS / nom(depot)
        try:
            if not (dest / ".git").exists():
                r = git("clone", "--quiet", url(depot), str(dest), timeout=300)
            else:
                r = git("fetch", "--quiet", "--prune", "origin", cwd=dest)
                if r.returncode == 0:
                    head = git("symbolic-ref", "--short", "refs/remotes/origin/HEAD", cwd=dest).stdout.strip() or "origin/main"
                    git("checkout", "--quiet", "--detach", head, cwd=dest)
                    git("worktree", "prune", cwd=dest)
            if r.returncode == 0:
                prets.append(depot)
            else:
                erreurs.append(f"{depot} : {r.stderr.strip()[:200]}")
        except subprocess.TimeoutExpired:
            erreurs.append(f"{depot} : délai dépassé")
    verrou.close()
    return prets, erreurs


def nettoyer():
    """Les dossiers de travail de plus d'une semaine s'en vont."""
    TRAVAIL.mkdir(exist_ok=True)
    limite = time.time() - GARDER_JOURS * 86400
    for d in TRAVAIL.iterdir():
        try:
            if d.is_dir() and d.stat().st_mtime < limite:
                shutil.rmtree(d, ignore_errors=True)
        except OSError:
            pass


def consigne(tache, depots, erreurs, regles, cfg_outils=()):
    lignes = "\n".join(f"- ~/depots/{nom(d)} : {url(d)}" for d in depots)
    manquants = ("\nDépôts indisponibles pour l'instant :\n" + "\n".join(f"- {e}" for e in erreurs)) if erreurs else ""
    court = tache["run"][-6:]
    outils = "\n".join(f"- {o}" for o in cfg_outils)
    outils = f"\n\nOutils en plus de tes dépôts :\n{outils}" if outils else ""
    return f"""Tu es {tache.get('nom', 'un agent')}, {tache.get('role', '').lower()} dans l'équipe de Mathias. {tache.get('presentation', '')}
Mathias t'a confié une tâche depuis Cerveau HQ. Tu travailles sur son VPS, sous un utilisateur sans privilèges (pas de sudo, pas de Docker).

Dépôts disponibles (clones à jour, ne les modifie jamais directement) :
{lignes}{manquants}{outils}

Règles :
1. Si la tâche demande seulement une information ou un avis, lis et réponds, sans rien modifier.
2. Pour modifier du code, travaille dans un worktree créé dans le dossier courant, sur une branche neuve :
   git -C ~/depots/<dépôt> worktree add ./<dépôt> -b agent/{court}-<sujet-court> origin/<branche de base>
   La branche de base est celle où vit le code concerné (souvent main, parfois dev : regarde le dépôt).
3. Fais des commits clairs, en français. Lance les tests, le lint ou la compilation du projet quand ils existent.
4. {regles}
5. Pour attendre qu'un job ou un traitement se termine, fais une seule commande Bash au premier plan qui boucle (une lecture toutes les 30 secondes, avec un délai de commande assez long) et s'arrête d'elle-même quand c'est fini. Jamais de commande en arrière-plan ni de surveillance à relancer plus tard : personne ne te réveillera.
6. Ne lis, n'affiche et ne commite jamais de secret (.env, clés, jetons). Ne modifie aucune configuration de déploiement sans qu'on te le demande.
7. Ta réponse finale répond à la question de Mathias, et à rien d'autre. Une à trois phrases, en français simple.
   - Commence par la réponse elle-même (« Oui, … », « Non, … », le chiffre, le nom).
   - Pas de méthode, pas de chemins de fichiers, de numéros de ligne ou de commit, pas de titres ni de listes, sauf s'il les demande.
   - Ne dis pas ce que tu n'as pas fait (« sans rien modifier ») et n'ajoute pas de remarque qu'on ne t'a pas demandée.
   - Pour une modification de code : ce qui est corrigé, le lien de la branche, et ce qu'il doit vérifier, en trois lignes au plus."""


def regles_push(cfg):
    if cfg.get("pousser") == "aucun":
        return "Ne pousse rien : laisse les commits en local et dis-le dans ta réponse."
    return ("Pousse ta branche (git push -u origin <branche>), jamais sur main, master, dev ni une branche existante, "
            "jamais de --force. Donne ensuite le lien pour ouvrir la demande de fusion : celui qu'affiche git push "
            "s'il y en a un, sinon https://github.com/<propriétaire>/<dépôt>/compare/<base>...<branche>?expand=1 "
            "pour GitHub, ou https://<hôte>/<propriétaire>/<dépôt>/compare/<base>...<branche> pour Gitea.")


def juger(tache, cfg):
    """Un jugement court pour la veille : lire des messages, dire ce qui
    compte. Ni dépôts, ni outils, ni serveur MCP : un modèle plus léger
    répond directement, sur l'abonnement Claude."""
    dossier = TRAVAIL / "veille"
    dossier.mkdir(parents=True, exist_ok=True)
    emit({"type": "atelier", "event": "pret", "depots": [], "erreurs": [],
          "modele": cfg.get("modele_veille"), "effort": cfg.get("effort_veille")})
    cmd = [
        str(CLAUDE), "-p", tache["demande"],
        "--model", cfg.get("modele_veille", "claude-sonnet-5-5"),
        "--effort", cfg.get("effort_veille", "low"),
        "--output-format", "stream-json", "--verbose",
        "--no-session-persistence",
        "--disallowedTools", "Bash,Edit,MultiEdit,Write,Read,Glob,Grep,LS,WebFetch,WebSearch,Task,Agent,NotebookEdit,TodoWrite,Monitor",
        "--append-system-prompt", str(tache.get("consigne", "")) + "\nRéponds directement, sans utiliser d'outil.",
    ]
    env = {k: v for k, v in os.environ.items() if k not in ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")}
    env["PATH"] = f"{HOME / '.local' / 'bin'}:/usr/local/bin:/usr/bin:/bin"
    env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] = "1"
    proc = subprocess.Popen(cmd, cwd=dossier, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, bufsize=1, start_new_session=True)

    def surveiller_entree():
        sys.stdin.read()
        if proc.poll() is None:
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass

    threading.Thread(target=surveiller_entree, daemon=True).start()
    erreurs_claude = []
    threading.Thread(target=lambda: erreurs_claude.extend(proc.stderr.readlines()[-20:]), daemon=True).start()
    for out in proc.stdout:
        sys.stdout.write(out)
        sys.stdout.flush()
    code = proc.wait()
    if code != 0:
        emit({"type": "atelier", "event": "fin", "code": code, "stderr": "".join(erreurs_claude)[-2000:]})
    os._exit(code)


def main():
    ligne = sys.stdin.readline()
    if not ligne:
        fail("aucune tâche reçue")
    try:
        tache = json.loads(ligne)
    except json.JSONDecodeError as e:
        fail(f"tâche illisible : {e}")
    demande = str(tache.get("demande", "")).strip()
    run = re.sub(r"[^a-zA-Z0-9]", "", str(tache.get("run", "")))
    if not demande or not run:
        fail("tâche incomplète")
    tache["run"] = run

    cfg = config()
    if not CLAUDE.exists():
        fail("Claude Code n'est pas installé pour l'utilisateur atelier")
    if tache.get("mode") == "jugement":
        juger(tache, cfg)
        return
    nettoyer()
    depots, erreurs = synchroniser(cfg.get("depots", []))
    dossier = TRAVAIL / run
    dossier.mkdir(parents=True, exist_ok=True)
    emit({"type": "atelier", "event": "pret", "depots": depots, "erreurs": erreurs,
          "modele": cfg.get("modele"), "effort": cfg.get("effort")})

    cmd = [
        str(CLAUDE), "-p", demande,
        "--model", cfg.get("modele", "claude-opus-5-5"),
        "--effort", cfg.get("effort", "high"),
        "--output-format", "stream-json", "--verbose",
        "--permission-mode", "bypassPermissions",
        "--no-session-persistence",
        "--disallowedTools", "Monitor",
        "--add-dir", str(DEPOTS),
        *(["--mcp-config", str(MCP)] if MCP.exists() else []),
        "--append-system-prompt", consigne(tache, depots, erreurs, regles_push(cfg), cfg.get("outils", [])),
    ]
    env = {k: v for k, v in os.environ.items() if k not in ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")}
    env["PATH"] = f"{HOME / '.local' / 'bin'}:/usr/local/bin:/usr/bin:/bin"
    # Les jetons des outils (le serveur MCP de Cerveau) : Claude Code les lit
    # dans son environnement, la consigne n'en contient aucun.
    try:
        for ligne in SECRETS.read_text().splitlines():
            if "=" in ligne and not ligne.lstrip().startswith("#"):
                k, v = ligne.split("=", 1)
                env[k.strip()] = v.strip()
    except FileNotFoundError:
        pass
    # Sans personne pour le relancer, un agent qui attend « en arrière-plan »
    # ne voit jamais la fin de ce qu'il attend : il reste bloqué jusqu'au
    # délai. Ici, toute attente se fait au premier plan, et une commande peut
    # durer jusqu'à 50 minutes (le suivi d'un import, par exemple).
    env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] = "1"
    env["BASH_DEFAULT_TIMEOUT_MS"] = str(10 * 60 * 1000)
    env["BASH_MAX_TIMEOUT_MS"] = str(50 * 60 * 1000)
    proc = subprocess.Popen(cmd, cwd=dossier, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, bufsize=1, start_new_session=True)

    def arreter():
        try:
            os.killpg(proc.pid, signal.SIGTERM)
            time.sleep(5)
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    # Cerveau ferme l'entrée : on arrête Claude Code.
    def surveiller_entree():
        sys.stdin.read()
        if proc.poll() is None:
            arreter()

    threading.Thread(target=surveiller_entree, daemon=True).start()
    limite = int(cfg.get("duree_max_min", 60)) * 60
    threading.Timer(limite, arreter).start()

    erreurs_claude = []
    threading.Thread(target=lambda: erreurs_claude.extend(proc.stderr.readlines()[-20:]), daemon=True).start()
    for out in proc.stdout:
        sys.stdout.write(out)
        sys.stdout.flush()
    code = proc.wait()
    if code != 0:
        emit({"type": "atelier", "event": "fin", "code": code, "stderr": "".join(erreurs_claude)[-2000:]})
    os._exit(code)


if __name__ == "__main__":
    main()
