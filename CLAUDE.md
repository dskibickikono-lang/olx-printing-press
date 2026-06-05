# CLAUDE.md — olx-printing-press

> **Dla agentów AI:** Ten plik to Twój punkt startowy. Szczegółowa specyfikacja techniczna projektu jest w `src/CLAUDE.md` — przeczytaj oba.

---

## 📌 Co to jest

**olx-printing-press** to scraper OLX napisany w **Go**, który generuje leady B2B dla HR KONO / APT Work. Pobiera ogłoszenia pracy z OLX (produkcja, magazyn, logistyka), wykrywa firmy które często publikują oferty, wzbogaca je danymi rejestrowymi przez bizraport.pl (NIP, KRS, REGON).

**Stack:** Go, SQLite, CLI + MCP server  
**Deployment:** VPS (produkcja) + lokalnie (dev)  
**Repo:** https://github.com/dskibickikono-lang/olx-printing-press

---

## 🗂️ Struktura repo

```
olx-printing-press/
├── CLAUDE.md              ← ten plik (workflow + deployment)
├── README.md
├── data/                  ← runtime: SQLite, cache, eksporty CSV/JSON (NIE commituj)
├── docs/                  ← dokumentacja
└── src/                   ← cały kod źródłowy Go
    ├── CLAUDE.md            ← specyfikacja techniczna (czytaj to również)
    ├── cmd/
    │   ├── olx-pp-cli/      ← CLI entry point
    │   └── olx-pp-mcp/      ← MCP server entry point
    ├── internal/            ← logika biznesowa
    ├── go.mod
    ├── go.sum
    └── Makefile             ← komendy build
```

---

## ⚙️ Jak działa build — wyjaśnienie

**To jest Go — język kompilowany.** Kod źródłowy w `src/*.go` nie działa bezpośrednio. Musisz go najpierw skompilować do pliku binarnego.

```
Edytujesz plik .go w src/
        ↓
   go build  (~2 sekundy)
        ↓
  bin/olx-pp-cli    ← CLI — uruchamiasz ręcznie w terminalu
  bin/olx-pp-mcp    ← MCP server — Claude Code się z nim łączy
```

**Odpowiedź na pytanie "czy każda zmiana wymaga buildu?"**  
Tak — ale to tylko jedna komenda i trwa ~2 sekundy.

---

## 🔧 Komendy deweloperskie

```powershell
# Przejdź do kodu źródłowego
cd C:\Claude\lead-engine\scrapers\olx-printing-press\src

# === BUILD ===

# Zbuduj tylko CLI
make build
# Wynik: src/bin/olx-pp-cli.exe

# Zbuduj tylko MCP server
make build-mcp
# Wynik: src/bin/olx-pp-mcp.exe

# Zbuduj oba naraz (najczęściej używane)
make build-all

# === TESTY ===
make test

# === URUCHOMIENIE CLI ===
.\bin\olx-pp-cli doctor --offline     # Sprawdź czy wszystko ładuje się poprawnie
.\bin\olx-pp-cli sync                 # Pobierz nowe oferty z OLX → SQLite
.\bin\olx-pp-cli companies            # Analityka firm (lokalnie, bez sieci)
.\bin\olx-pp-cli jobs                 # Analityka ofert (lokalnie, bez sieci)
.\bin\olx-pp-cli enrich --limit 10    # Wzbogacanie danych przez bizraport.pl
.\bin\olx-pp-cli export               # Eksport do CSV/JSON

# === CZYSZCZENIE ===
make clean  # Usuń folder bin/
```

---

## 💻 CLI vs MCP — czym się różnią

| | `olx-pp-cli` | `olx-pp-mcp` |
|---|---|---|
| **Co to jest** | Program CLI | Serwer MCP |
| **Kto steruje** | Ty (ręcznie w terminalu) | Claude Code (automatycznie) |
| **Kiedy używać** | Testy, VPS cron, ręczne synki | Gdy chcesz żeby AI sam scrapeował |
| **Komendy** | `sync`, `jobs`, `companies`, `enrich`, `export` | Te same — jako MCP tools |

**Obie binarki mają identyczną logikę** — MCP server to tylko inny "interfejs" do tego samego kodu.

---

## 🚀 Deploy na VPS

```bash
# Lokalnie — zbuduj binarkę dla Linux (VPS)
cd src
$env:GOOS="linux"; $env:GOARCH="amd64"
go build -o bin/olx-pp-cli-linux ./cmd/olx-pp-cli
go build -o bin/olx-pp-mcp-linux ./cmd/olx-pp-mcp

# Wyślij na VPS przez SCP
scp bin/olx-pp-cli-linux user@vps-ip:/opt/olx-printing-press/bin/olx-pp-cli
scp bin/olx-pp-mcp-linux user@vps-ip:/opt/olx-printing-press/bin/olx-pp-mcp

# Lub — alternatywnie — klonuj repo na VPS i buduj tam
git pull origin main
cd src && make build-all
```

> ⚠️ **Uwaga:** folder `data/` (SQLite, cache) zostaje na VPS. Nigdy nie nadpisuj go podczas deploy.

---

## 🌃 Typowy workflow — krok po kroku

### Dodajesz nową funkcję:
```powershell
# 1. Utwórz branch
git checkout -b feature/nazwa-funkcji

# 2. Edytuj pliki .go w src/

# 3. Zbuduj i przetestuj lokalnie
cd src
make build-all
make test
.\bin\olx-pp-cli doctor --offline

# 4. Commit i push
git add .
git commit -m "feat: opis zmiany"
git push origin feature/nazwa-funkcji

# 5. Pull Request na GitHub → merge do main

# 6. Deploy na VPS (jeśli potrzebny)
```

### Nie chcesz używać MCP — tylko CLI:
- Zapomnij o `olx-pp-mcp` całkowicie
- Pracuj tylko z `olx-pp-cli`
- `make build` (bez `build-mcp`) w zupełności wystarczy

---

## 🚨 Zasady bezpieczeństwa

- ❌ Nie commituj `data/` (SQLite, logi, cache) — jest w `.gitignore`
- ❌ Nie loguj pełnych URLów bizraport.pl (credentials są w query string)
- ❌ `enrich` billuje per zwócony rząd — zawsze używaj `--limit` na testach
- ✅ Dane uwierzytelniające bizraport.pl tylko w `.env` lub zmiennych środowiskowych

---

## 🤖 Dla agentów AI

1. Kod źródłowy jest w `src/` — tam edytujesz pliki `.go`
2. Po każdej zmianie uruchom `make build-all` i `make test`
3. Szczegółowe zasady kodu, model danych i komendy są w `src/CLAUDE.md`
4. Nie tworzysz nowych folderów poza `src/`, `data/`, `docs/`, `bin/`
5. Nie pushuj na `main` bezpośrednio — branch + PR

---

*CLAUDE.md v1.0 — 2026-06-05*
