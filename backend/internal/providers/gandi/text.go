package gandi

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// Le contenu de <script> et <style> n'est pas du texte à lire. Deux
	// expressions plutôt qu'une : RE2 n'a pas de références arrière.
	htmlScriptRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>`)
	htmlStyleRE  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</\s*style\s*>`)
	// Balises qui séparent des blocs : elles deviennent des retours à la ligne.
	htmlBreakRE = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/tr|/h[1-6])\b[^>]*>`)
	htmlTagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
	entityRE    = regexp.MustCompile(`&#?[0-9a-zA-Z]+;`)
	// Lignes de citation d'une réponse : « > … » ou « Le 3 mars, X a écrit : ».
	quoteHeaderRE = regexp.MustCompile(`(?i)^(le .+ a écrit\s*:|on .+ wrote\s*:|-{2,}\s*message (transféré|d'origine|original).*)$`)
)

var namedEntities = map[string]string{
	"&nbsp;": " ", "&amp;": "&", "&lt;": "<", "&gt;": ">",
	"&quot;": `"`, "&apos;": "'", "&#39;": "'", "&eacute;": "é",
	"&egrave;": "è", "&ecirc;": "ê", "&agrave;": "à", "&ccedil;": "ç",
	"&ugrave;": "ù", "&ocirc;": "ô", "&icirc;": "î", "&euro;": "€",
	"&rsquo;": "'", "&lsquo;": "'", "&ldquo;": `"`, "&rdquo;": `"`,
	"&hellip;": "…", "&mdash;": "—", "&ndash;": "–",
}

// htmlToText réduit un mail HTML à quelque chose de prononçable.
//
// Ce n'est volontairement pas un vrai analyseur : un mail marketing est un
// empilement de tables dont aucune structure n'a de sens à l'oral. On garde le
// texte et les séparations de blocs, on jette le reste.
func htmlToText(html string) string {
	if strings.TrimSpace(html) == "" {
		return ""
	}
	s := htmlScriptRE.ReplaceAllString(html, " ")
	s = htmlStyleRE.ReplaceAllString(s, " ")
	s = htmlBreakRE.ReplaceAllString(s, "\n")
	s = htmlTagRE.ReplaceAllString(s, " ")
	s = entityRE.ReplaceAllStringFunc(s, func(e string) string {
		if v, ok := namedEntities[strings.ToLower(e)]; ok {
			return v
		}
		return " "
	})
	return s
}

// collapse normalise les blancs sans écraser les paragraphes : les retours à la
// ligne portent le rythme de lecture.
func collapse(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == '\t' || (unicode.IsSpace(r) && r != '\n') {
				return ' '
			}
			return r
		}, line))
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			// Une ligne vide sépare, deux n'apportent rien de plus.
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// stripQuotedReply coupe l'historique cité en fin de mail. Sans ça, Raoul lit
// six fois la même conversation dans un fil un peu long.
func stripQuotedReply(s string) string {
	body, _ := splitQuotedReply(s)
	return body
}

// QuotedMessage est un message antérieur recopié dans le corps du mail. Les
// clients mail empilent ainsi tout le fil : c'est là que se trouve la
// conversation, et souvent la seule trace de ce que l'utilisateur a lui-même
// écrit — ses propres envois ne sont pas dans sa boîte de réception.
type QuotedMessage struct {
	From    string
	Sent    string
	To      string
	Cc      string
	Subject string
	Body    string
}

var (
	inlineFromRE    = regexp.MustCompile(`(?i)^(?:from|de)\s*:\s*(.+)$`)
	inlineSentRE    = regexp.MustCompile(`(?i)^(?:sent|envoyé|envoye|date)\s*:\s*(.+)$`)
	inlineToRE      = regexp.MustCompile(`(?i)^(?:to|à)\s*:\s*(.+)$`)
	inlineCcRE      = regexp.MustCompile(`(?i)^(?:cc|copie)\s*:\s*(.+)$`)
	inlineSubjectRE = regexp.MustCompile(`(?i)^(?:subject|objet)\s*:\s*(.+)$`)
)

// Bornes du fil cité : au-delà on garde du texte que personne ne relira, et le
// contexte utile est toujours dans les messages les plus récents.
const (
	maxQuotedMessages = 6
	maxQuotedBody     = 900
)

// isForwardHeader dit si la ligne i ouvre un bloc d'en-têtes recopié.
//
// Un « From : » seul ne suffit pas : une phrase qui commence par « De : »
// couperait le mail en deux. On exige donc qu'un « Sent : » ou un « Subject : »
// suive de près, séparés au plus par d'autres en-têtes.
func isForwardHeader(lines []string, i int) bool {
	if !inlineFromRE.MatchString(strings.TrimSpace(lines[i])) {
		return false
	}
	for j := i + 1; j < len(lines) && j <= i+5; j++ {
		line := strings.TrimSpace(lines[j])
		if line == "" {
			continue
		}
		if inlineSentRE.MatchString(line) || inlineSubjectRE.MatchString(line) {
			return true
		}
		if !inlineToRE.MatchString(line) && !inlineCcRE.MatchString(line) {
			return false
		}
	}
	return false
}

// parseForwardHeaders lit le bloc d'en-têtes en tête d'un segment et rend le
// nombre de lignes consommées.
func parseForwardHeaders(lines []string) (QuotedMessage, int) {
	var m QuotedMessage
	i := 0
	for ; i < len(lines) && i < 10; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			if m.From != "" {
				break // ligne vide après les en-têtes : le corps commence
			}
			continue
		}
		field := func(re *regexp.Regexp) string {
			return strings.TrimSpace(re.FindStringSubmatch(line)[1])
		}
		switch {
		case m.From == "" && inlineFromRE.MatchString(line):
			m.From = field(inlineFromRE)
		case inlineSentRE.MatchString(line):
			m.Sent = field(inlineSentRE)
		case inlineToRE.MatchString(line):
			m.To = field(inlineToRE)
		case inlineCcRE.MatchString(line):
			m.Cc = field(inlineCcRE)
		case inlineSubjectRE.MatchString(line):
			m.Subject = field(inlineSubjectRE)
		default:
			return m, i
		}
	}
	return m, i
}

// parseQuotedThread sépare le mail lui-même des messages qu'il recopie.
//
// Trois conventions coexistent et il faut les couvrir toutes : le bloc
// d'en-têtes d'Outlook (« From: / Sent: / Subject: »), la ligne d'introduction
// de Gmail et Apple Mail (« Le 3 mars, X a écrit : »), et les chevrons. Ne
// reconnaître que les deux dernières laissait passer les fils Outlook en
// entier dans le corps — donc lus à voix haute, et sans qu'aucun message
// antérieur ne soit un objet distinct auquel se référer.
func parseQuotedThread(s string) (string, []QuotedMessage) {
	lines := strings.Split(s, "\n")

	starts := make([]int, 0, 4)
	forward := make(map[int]bool, 4)
	for i := range lines {
		if quoteHeaderRE.MatchString(strings.TrimSpace(lines[i])) {
			starts = append(starts, i)
			continue
		}
		if isForwardHeader(lines, i) {
			starts = append(starts, i)
			forward[i] = true
		}
	}
	if len(starts) == 0 {
		return strings.Join(lines, "\n"), nil
	}

	body := strings.TrimSpace(strings.Join(lines[:starts[0]], "\n"))

	msgs := make([]QuotedMessage, 0, len(starts))
	for k, start := range starts {
		end := len(lines)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		segment := lines[start:end]

		var m QuotedMessage
		var consumed int
		if forward[start] {
			m, consumed = parseForwardHeaders(segment)
		} else {
			// « Le 3 mars, X a écrit : » porte l'expéditeur et la date dans la
			// même phrase : on la garde telle quelle plutôt que de la découper
			// au risque de se tromper.
			m.From = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(segment[0]), ":"))
			consumed = 1
		}
		m.Body = truncateRunes(unquote(segment[consumed:]), maxQuotedBody)
		if m.From == "" && m.Body == "" {
			continue
		}
		msgs = append(msgs, m)
		if len(msgs) == maxQuotedMessages {
			break
		}
	}
	return body, msgs
}

// splitQuotedReply sépare le message de l'historique qu'il cite.
//
// Les deux moitiés ne servent pas à la même chose, et c'est pour ça qu'on les
// garde séparées plutôt que d'en jeter une : le message seul est ce qu'on lit à
// voix haute, l'historique est ce qui permet de RÉPONDRE. Sans lui, on ignore
// ce qui a déjà été dit, qui répond à qui, et comment les gens s'appellent
// entre eux — et on écrit un mail hors sujet, poliment.
func splitQuotedReply(s string) (body, quoted string) {
	body, msgs := parseQuotedThread(s)
	if len(msgs) > 0 {
		parts := make([]string, 0, len(msgs))
		for _, m := range msgs {
			parts = append(parts, strings.TrimSpace(m.From+"\n"+m.Body))
		}
		return strings.TrimSpace(body), strings.TrimSpace(strings.Join(parts, "\n\n"))
	}

	// Aucun en-tête reconnu : les lignes « > … » se trient une à une. Elles
	// peuvent être entrecoupées de blancs sans marquer la fin du message utile.
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	cited := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			cited = append(cited, line)
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), strings.TrimSpace(unquote(cited))
}

// unquote retire les chevrons de citation. Les garder ferait lire « supérieur,
// supérieur, bonjour » à la synthèse vocale, et gêne le modèle plus qu'ils ne
// l'aident : la structure du fil se lit aux en-têtes, pas aux chevrons.
func unquote(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for strings.HasPrefix(trimmed, ">") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		}
		out = append(out, trimmed)
	}
	return collapse(strings.Join(out, "\n"))
}

// replyPrefixRE reconnaît les préfixes qu'empilent les clients mail. Deux mails
// d'un même fil ne partagent que ce qu'il en reste.
var replyPrefixRE = regexp.MustCompile(`(?i)^\s*((re|ré|rép|rep|fw|fwd|tr|transf)\s*(\[[0-9]+\])?\s*:\s*)+`)

// baseSubject réduit « Re: Fwd: Devis » à « devis », pour rapprocher les
// messages d'une même conversation. IMAP ne donne pas de fil : le sujet nu est
// le seul lien qu'on puisse établir sans en-têtes de threading fiables.
func baseSubject(s string) string {
	return normalizeQuery(replyPrefixRE.ReplaceAllString(strings.TrimSpace(s), ""))
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// normalizeQuery aplatit une demande dictée : casse, accents et ponctuation ne
// sont pas fiables quand la phrase vient de la reconnaissance vocale.
func normalizeQuery(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if folded, ok := mailAccents[r]; ok {
			return folded
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

var mailAccents = map[rune]rune{
	'à': 'a', 'â': 'a', 'ä': 'a', 'á': 'a', 'ã': 'a', 'å': 'a',
	'ç': 'c',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
}
