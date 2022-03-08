LAMBDA_NAME ?= tiktok-api-scraper

build:
	mkdir -p out
	GOOS=linux GOARCH=amd64 go build -o out/tiktok-api cmd/main.go
