build:
	mkdir -p out
	GOOS=linux GOARCH=amd64 go build -o out/tiktok-api cmd/main.go

deploy: build
	ssh bignuc "sudo systemctl stop tiktok-api"
	scp out/tiktok-api bignuc:tiktok/
	ssh bignuc "sudo systemctl start tiktok-api"