#!/bin/bash


export GOPROXY=https://goproxy.cn,direct
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/server ./cmd/main.go

docker rm -f chatgpt-go-backend 2>/dev/null

docker build -t chatgpt-go-backend .

docker run -d --name chatgpt-go-backend --network host chatgpt-go-backend
