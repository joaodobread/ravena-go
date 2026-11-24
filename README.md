# ravena-go

Reescrita em Go, do zero, do [ravena-ai](https://github.com/moothz/ravena-ai) de [moothz](https://github.com/moothz), que serve de referência de comportamento.

Um único processo: WhatsApp via [whatsmeow](https://github.com/tulir/whatsmeow), SQLite para tudo (sessão e dados), painel web embutido e ffmpeg/ImageMagick/yt-dlp chamados só sob demanda, com concorrência limitada. Sem Postgres, sem whatsgoapi, sem Node, sem sidecars. Com 160 comandos carregados, usa **~13 MiB de RAM** no contêiner.

## Rodando

```sh
go run ./cmd/ravena          # painel em http://localhost:8080 (pareie por lá)
go test ./...                # testes de integração usam ffmpeg e magick, se instalados
docker build -t ravena-go . && docker run -v ravena-data:/data -p 8080:8080 -e ADMIN_PASSWORD=... ravena-go
```

Pareamento: abra o painel e escaneie o QR, ou defina `PAIR_PHONE` para receber um código (aparece no painel e no log).

## Variáveis de ambiente

| Variável | Padrão | |
|---|---|---|
| `DATA_DIR` | `./data` (`/data` no Docker) | SQLite (sessão + dados) e mídias — **precisa de volume** |
| `PORT` | `8080` | painel e `GET /healthz` |
| `ADMIN_PASSWORD` | gerada | senha do painel; sem ela, uma senha é gerada, salva e mostrada no log |
| `SUPER_ADMINS` | — | números com acesso total, separados por vírgula (com ou sem o 9º dígito) |
| `PAIR_PHONE` | — | pareia por código em vez de QR |
| `PREFIX` | `!` | prefixo padrão (cada grupo pode ter o seu) |
| `OPENROUTER_API_KEY` | — | sem ela, as funções de IA respondem "indisponível" |
| `LLM_MODEL` | `google/gemini-3.1-flash-lite` | modelo (precisa ter visão) |
| `NASA_API` | `DEMO_KEY` | `!apod` |
| `WORKERS` | `8` | mensagens tratadas em paralelo |
| `FFMPEG_JOBS` | `2` | conversões simultâneas (segura o pico de RAM) |
| `MAX_MESSAGE_AGE` | `2m` | ignora mensagens antigas ao reconectar |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Painel

Login por senha; tudo que o bot tem de configurável:

- **Início**: status da conexão e pareamento (QR/código), memória, comandos mais usados.
- **Grupos**: prefixo, pausa, interações automáticas (chance, intervalo, proporção IA × comandos), respostas a elogios/xingamentos, boas-vindas/despedida, comandos e categorias silenciados, admins adicionais, usuários ignorados.
- **Comandos**: ligar/desligar qualquer comando em todos os grupos, com uso dos últimos 7 dias.
- **Personalizados**: comandos criados pelos grupos (`!g-addCmd`), com edição de respostas.
- **Configurações**: nome do bot, super admins, ignorar conversas privadas.

## Funcionalidades

| Área | Comandos |
|---|---|
| Figurinhas | `s`/`sticker`/`fig`, `sq` `sqc` `sqb` `sqe` `sqi` (corte por IA), `ssom` (figurinha com som), reação 🖼, figurinha automática no privado, extrair mídia de figurinha |
| Mídia | `distort` `morejpeg` `sketch` `oil` `neon` `pixelate`, `emojik`, `extractaudio` `extractvoice` `volume`, `audio` (MyInstants), `anime`, `apod` |
| Downloads | `download`/`dl`/`yt`/`tiktok`/`insta`… (⬇️), `download-audio` (⏬), `sr`, `letra` |
| IA | `ai` (🤖, com imagem), menção ao bot, `resumo` (🦜), `interagir` + interação automática, respostas a elogios/xingamentos, `ocr` (🔍), `tarot` |
| Grupo | `g-*` de gestão (pausar, prefixo, interagir, mutar, ignorar, admins, ban…), boas-vindas/despedida, comandos personalizados `g-addCmd`…, `atencao` (📢), `apagar` (🗑️), `ignorar`, `faladores` |
| Jogos | `pescar` + `slots` (economia compartilhada), `anagrama`, `sequencia`, `adedonha`, `roletarussa`, `pinto`, dados `d20`/`roll`, `namorar`/`casar`…, `sorteio` (🎲), listas `ll`… (1️⃣–🔟) |
| Zoeira | `biscoito`, `cantada`, `violencia`, `morreu`, `boleto`, `pix`, `pecar`… |
| Utilidades | `menu`, `lembrar`/`lembretes`, `traduzir` (+ reação com bandeira), `wiki`, `clima`, `buscar`, `qr`/`qr-wifi`/`qr-pix`, `0800`, `lol-build`/`wr-build`, `ping` |

**Deixados de fora de propósito** (custo de infraestrutura ou já quebrados no deploy antigo): remoção de fundo (rembg), TTS/STT, geração de imagem, Riot/Giphy/IMDb/Last.fm/Steam/PSN/Correios/Placas, Copa 2026, monitoramento de streams, filtros de mensagens, apelidos, avisos. Detalhes do original em [`docs/spec`](docs/spec).

## Estrutura

```
cmd/ravena/            main (conexão, eventos, painel) e features.go (lista de funcionalidades)
internal/bot/          núcleo independente do WhatsApp: Message, Client, Router, Policy, eventos de grupo
internal/bot/bottest/  Client falso para testes
internal/wa/           adaptador whatsmeow ⇄ bot, sessão/pareamento, cache de mensagens recentes
internal/policy/       regras: prefixo por grupo, pausa, silenciados, admins, anti-spam, cooldown, estatísticas
internal/store/        SQLite: grupos, kv, estatísticas, migrações por funcionalidade
internal/web/          painel (templates + htmx embutidos)
internal/media/        ffmpeg/ImageMagick com concorrência limitada, EXIF de figurinha
internal/llm/          cliente OpenRouter
internal/app/          contrato app.Feature / app.Deps
internal/features/     uma pasta por funcionalidade, cada uma com testes
```

Uma funcionalidade implementa `app.Feature` (`Name` e `Register(ctx, *app.Deps)`) e entra em `cmd/ravena/features.go`. Regras para escrever uma: [`docs/PORTING.md`](docs/PORTING.md).
