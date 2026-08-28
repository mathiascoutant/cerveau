.PHONY: run build vet fmt test gandi-check voices linux deploy

run:
	go run ./cmd/server

build:
	go build -o bin/cerveau ./cmd/server

vet:
	go vet ./...

fmt:
	gofmt -w .

test:
	go test ./...

# Teste les identifiants IMAP Gandi sans lancer toute la stack.
# Usage : make gandi-check EMAIL=moi@mondomaine.fr
gandi-check:
	go run ./cmd/gandicheck -email $(EMAIL)

# Liste les voix ElevenLabs du compte, les françaises d'abord.
# C'est la commande à lancer quand Raoul prend l'accent anglais : la voix par
# défaut est anglophone, et ça ne se corrige qu'en changeant de voix.
voices:
	go run ./cmd/voices

# Binaire pour le VPS OVH (Linux amd64).
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/cerveau-linux ./cmd/server

# Déploiement sans toolchain Go sur le serveur : on y envoie le binaire déjà
# compilé. Usage : make deploy VPS=ubuntu@cerveau.neurorun.fr
REMOTE_DIR ?= /home/ubuntu/projects/cerveau/backend

deploy: linux
	scp bin/cerveau-linux $(VPS):/tmp/cerveau.new
	ssh $(VPS) 'mv /tmp/cerveau.new $(REMOTE_DIR)/cerveau \
		&& chmod 755 $(REMOTE_DIR)/cerveau \
		&& sudo systemctl restart cerveau \
		&& sleep 2 && sudo systemctl status cerveau --no-pager | head -12'
