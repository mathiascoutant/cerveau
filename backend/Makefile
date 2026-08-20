.PHONY: run build vet fmt test gandi-check linux deploy

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

# Binaire pour le VPS OVH (Linux amd64).
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/cerveau-linux ./cmd/server

# Déploiement : make deploy VPS=ubuntu@cerveau.neurorun.fr
# Le binaire est remplacé puis le service redémarré ; l'ancien reste en place
# jusqu'au mv, donc l'interruption se compte en millisecondes.
deploy: linux
	scp bin/cerveau-linux $(VPS):/tmp/cerveau.new
	ssh $(VPS) 'sudo mv /tmp/cerveau.new /opt/cerveau/cerveau \
		&& sudo chown cerveau:cerveau /opt/cerveau/cerveau \
		&& sudo chmod 755 /opt/cerveau/cerveau \
		&& sudo systemctl restart cerveau \
		&& sleep 2 && sudo systemctl status cerveau --no-pager | head -12'
