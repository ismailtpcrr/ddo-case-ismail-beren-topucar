# ParaKöprü — sık kullanılan komutlar.
# `make` ya da `make help` tüm hedefleri açıklamalarıyla listeler.

SHELL   := /bin/bash
COMPOSE := docker compose
BACKEND := backend

# Lint aracı sürümü sabit; yerel kurulum gerekmez (go run ile indirilip çalışır).
GOLANGCI_LINT_VERSION := v2.15.0

.DEFAULT_GOAL := help
.PHONY: help env up down logs ps reset-demo test test-race cover lint fmt

help: ## Hedefleri listele
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

## --- Çalıştırma -------------------------------------------------------------

env: ## .env yoksa .env.example'dan oluştur
	@test -f .env || { cp .env.example .env && echo ".env, .env.example'dan oluşturuldu"; }

up: env ## Sistemi derle ve başlat; tüm servisler sağlıklı olana kadar bekler
	$(COMPOSE) up --build --detach --wait

down: ## Sistemi durdur (veriler korunur)
	$(COMPOSE) down

logs: ## Servis loglarını izle
	$(COMPOSE) logs --follow --tail=100

ps: ## Servislerin durumunu göster
	$(COMPOSE) ps

reset-demo: env ## Tüm verileri sil ve sistemi ilk seed haliyle yeniden başlat
	$(COMPOSE) down --volumes --remove-orphans
	$(COMPOSE) up --build --detach --wait

## --- Geliştirme ------------------------------------------------------------

test: ## Go testleri
	cd $(BACKEND) && go test ./...

test-race: ## Go testleri, yarış dedektörüyle
	cd $(BACKEND) && go test -race ./...

cover: ## Test kapsamı raporu
	cd $(BACKEND) && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -n 1

lint: ## golangci-lint
	cd $(BACKEND) && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

fmt: ## Go kodunu biçimlendir
	cd $(BACKEND) && gofmt -w .
