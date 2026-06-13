# DEPLOY.md — wdrażanie olx-printing-press na VPS

> Przewodnik dla **nowych / dodatkowych** VPS-ów. Główny firmowy VPS jest już
> wdrożony — patrz sekcja [Główny VPS](#główny-vps-stan-i-utrzymanie) na końcu.

---

## ⚠️ Najważniejsza pułapka: stale binary + MCP

To jest Go — **kod źródłowy nie działa, dopóki nie zbudujesz binarki.** Dwa
miejsca, w których łatwo wdrożyć stary kod:

1. **CLI** (`olx-pp-cli`) — podnosi nowy kod natychmiast po przebudowaniu.
2. **MCP server** (`olx-pp-mcp`) — to **długo żyjący proces**. Nawet po
   przebudowaniu binarki **dalej serwuje stary kod w pamięci aż do restartu.**
   Sprawdź, czy proces nie trzyma usuniętej binarki:
   ```bash
   ls -la /proc/$(pgrep -f olx-pp-mcp)/exe   # "(deleted)" = trzeba zrestartować
   ```

**Po każdym deployu zawsze restartuj MCP server.** Inaczej np. `enrich`
przez MCP poleci po starym, droższym kodzie.

---

## Metoda A — build bezpośrednio na VPS (zalecana, gdy jest tam Go)

```bash
# na VPS
cd /opt/olx-printing-press            # ścieżka instalacji
git pull origin main
cd src && make build-all              # → src/bin/olx-pp-cli, src/bin/olx-pp-mcp

# Podmień binarki na ścieżce produkcyjnej (root bin/ pod symlinkami z $PATH)
cp src/bin/olx-pp-cli  bin/olx-pp-cli
cp src/bin/olx-pp-mcp  bin/olx-pp-mcp

# Restart MCP (patrz niżej) + sanity
olx-pp-cli doctor --offline
```

## Metoda B — cross-build na dev-hoście + scp (gdy na VPS nie ma Go)

```bash
# na maszynie deweloperskiej (dowolny OS z Go)
cd src && make release                # test + cross-build linux/amd64 + SHA256SUMS
# artefakty: src/bin/olx-pp-cli-linux, olx-pp-mcp-linux, SHA256SUMS.txt

scp src/bin/olx-pp-cli-linux  user@NOWY_VPS:/opt/olx-printing-press/bin/olx-pp-cli
scp src/bin/olx-pp-mcp-linux  user@NOWY_VPS:/opt/olx-printing-press/bin/olx-pp-mcp

# na VPS: zweryfikuj sumy, zrestartuj MCP, doctor --offline
```

Binarki są statyczne (`CGO_ENABLED=0`), więc działają niezależnie od wersji
glibc na docelowym VPS-ie.

---

## 🔒 Zasady deployu

- **NIGDY nie nadpisuj `data/`** (SQLite, cache, eksporty). To stan runtime —
  na produkcji żyje tam wzbogacona baza firm. Deploy dotyczy **tylko binarek**.
- **Sekrety bizraport** (`BIZRAPORT_EMAIL` / `BIZRAPORT_PASSWORD`) trzymaj w
  `.env` lub w jednostce systemd `Environment=` — nigdy w repo.
- **`enrich` billuje przez bizraport API.** Po deployu nie odpalaj pełnego
  `enrich` bez `--limit`. Pamiętaj o naprawionym koszcie: `/api/szukaj` jest
  teraz limitowany do `--max-candidates` (PR #3). Na przyszłych runach używaj
  `--min-jobs 2`, bo no-matche nie są stemplowane i re-bilżują się.
- Sprawdź koszt bez billowania: `olx-pp-cli doctor --live` (linia `bizraport use`).

---

## Restart MCP servera

**Jeśli MCP chodzi pod Claude Code (stdio):** zrestartuj sesję/klienta Claude —
to on uruchamia `olx-pp-mcp`.

**Jeśli MCP chodzi jako usługa systemd** (zalecane na produkcji):
```bash
sudo systemctl restart olx-pp-mcp
systemctl status olx-pp-mcp --no-pager
```

Przykładowa jednostka `/etc/systemd/system/olx-pp-mcp.service`:
```ini
[Unit]
Description=olx-printing-press MCP server
After=network.target

[Service]
WorkingDirectory=/opt/olx-printing-press
ExecStart=/opt/olx-printing-press/bin/olx-pp-mcp
EnvironmentFile=/opt/olx-printing-press/.env
Restart=on-failure
User=olx

[Install]
WantedBy=multi-user.target
```

---

## Główny VPS — stan i utrzymanie

Główny firmowy VPS jest **tą maszyną** (tu robiony był enrichment i build).
Stan na 2026-06-07:

- Binarki `bin/olx-pp-cli` i `bin/olx-pp-mcp` przebudowane z fixem (PR #3).
- ⚠️ **Checkout stoi na branchu `fix/szukaj-limit`, nie na `main`.** Po zmerge'owaniu PR:
  ```bash
  git checkout main && git pull origin main
  cd src && make build-all
  cp src/bin/olx-pp-cli bin/olx-pp-cli && cp src/bin/olx-pp-mcp bin/olx-pp-mcp
  ```
- ⚠️ **MCP server (długo żyjący proces) wymaga restartu** — do czasu restartu
  serwuje stary, drogi kod `enrich`.
