"""Les voix gratuites de Cerveau HQ.

Les voix neuronales de Microsoft Edge (lecture à voix haute du navigateur),
par la bibliothèque edge-tts : humaines, fluides, sans clé ni quota. Le
service n'est joint que par Cerveau, sur le réseau Docker du compose ; il
n'est pas publié.

    POST /parler  {"texte": "...", "voix": "fr-FR-RemyMultilingualNeural"}
    → audio/mpeg, envoyé au fil de la synthèse

Le service de Microsoft n'est pas une API officielle : quand il change,
mettre à jour edge-tts (Dockerfile) et reconstruire.
"""

import logging
import re

import edge_tts
from aiohttp import web

VOIX_PAR_DEFAUT = "fr-FR-RemyMultilingualNeural"
# Un nom de voix Microsoft, rien d'autre : il part dans le SSML.
VOIX_VALIDE = re.compile(r"^[a-z]{2,3}-[A-Z]{2}-[A-Za-z]+Neural$")
# Comme maxSpeechChars côté Cerveau.
MAX_TEXTE = 4000

log = logging.getLogger("voix")


async def parler(request: web.Request) -> web.StreamResponse:
    try:
        corps = await request.json()
    except ValueError:
        return web.json_response({"error": "corps JSON attendu"}, status=400)
    texte = str(corps.get("texte") or "").strip()[:MAX_TEXTE]
    voix = str(corps.get("voix") or VOIX_PAR_DEFAUT)
    if not texte:
        return web.json_response({"error": "rien à dire"}, status=400)
    if not VOIX_VALIDE.match(voix):
        return web.json_response({"error": "voix inconnue"}, status=400)

    reponse = web.StreamResponse(headers={"Content-Type": "audio/mpeg"})
    commence = False
    try:
        async for morceau in edge_tts.Communicate(texte, voix).stream():
            if morceau["type"] != "audio":
                continue
            if not commence:
                await reponse.prepare(request)
                commence = True
            await reponse.write(morceau["data"])
    except Exception as err:  # le service de Microsoft a refusé ou coupé
        log.warning("synthèse %s : %s", voix, err)
        if not commence:
            return web.json_response({"error": f"synthèse impossible : {err}"}, status=502)
        raise
    if not commence:
        return web.json_response({"error": "aucun son reçu"}, status=502)
    await reponse.write_eof()
    return reponse


async def sante(_: web.Request) -> web.Response:
    return web.Response(text="ok")


def app() -> web.Application:
    a = web.Application(client_max_size=64 * 1024)
    a.router.add_post("/parler", parler)
    a.router.add_get("/health", sante)
    return a


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    web.run_app(app(), host="0.0.0.0", port=8080, access_log=None)
