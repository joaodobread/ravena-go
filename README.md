# ravena-go

Reescrita em Go, do zero, do [ravena-ai](https://github.com/moothz/ravena-ai) de [moothz](https://github.com/moothz), que serve de referência de comportamento.

Um único processo: WhatsApp via [whatsmeow](https://github.com/tulir/whatsmeow), sessão em SQLite e mídia via ffmpeg/ImageMagick como processos externos. Sem Postgres, sem whatsgoapi, sem Node. Parado, usa ~10 MiB.

## Rodando

```sh
go run ./cmd/ravena          # mostra o QR no terminal na primeira vez
go test ./...                # testes de integração precisam de ffmpeg (e magick)
docker build -t ravena-go . && docker run -v ravena-data:/data -e PORT=8080 ravena-go
```

Para parear sem QR (melhor no Railway, onde o QR fica nos logs), defina `PAIR_PHONE` com o número (ex.: `5562999999999`): o código aparece nos logs.

## Variáveis de ambiente

| Variável | Padrão | |
|---|---|---|
| `DATA_DIR` | `./data` (`/data` no Docker) | SQLite com a sessão — **precisa de volume** |
| `PREFIX` | `!` | prefixo dos comandos |
| `PAIR_PHONE` | — | pareia por código em vez de QR |
| `PORT` | — | liga `GET /healthz` |
| `OPENROUTER_API_KEY` | — | sem ela, `!sqi` usa corte central |
| `LLM_MODEL` | `google/gemini-3.1-flash-lite` | modelo do `!sqi` |
| `STICKER_AUTHOR` | `ravena` | autor do pacote de figurinhas |
| `WORKERS` | `8` | mensagens tratadas em paralelo |
| `FFMPEG_JOBS` | `2` | conversões simultâneas (é o que segura o pico de RAM) |
| `MAX_MESSAGE_AGE` | `2m` | ignora mensagens antigas ao reconectar |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Estrutura

```
cmd/ravena/            main: conexão, pareamento, pool de workers, /healthz
internal/bot/          núcleo independente do WhatsApp: Message, Client, Router
internal/bot/bottest/  Client falso para testar funcionalidades
internal/wa/           adaptador whatsmeow ⇄ bot (conversão + envio)
internal/media/        ffmpeg/ImageMagick, filtros de corte, EXIF de figurinha
internal/llm/          cliente OpenRouter (corte inteligente)
internal/features/     uma pasta por funcionalidade (sticker/ ...)
internal/config/       variáveis de ambiente
```

Uma funcionalidade nova expõe `Commands() []*bot.Command` e/ou um `bot.Listener` e é registrada no `main`. Testes usam `bottest.FakeClient`.

## Funcionalidades

- [x] Figurinhas: `!s` `!sticker` `!fig` `!figurinha` (encaixa com transparência), `!sq`/`!sqc`/`!sqb`/`!sqe` (centro/topo/base/esticado), `!sqi` (corte por IA). Argumentos viram o nome do pacote; sem eles, o nome do grupo.
- [x] Responder uma figurinha com `!s` devolve a mídia (animada → MP4, transparente → PNG como documento).
- [x] No privado, toda imagem/vídeo vira figurinha automaticamente.
- [ ] Painel web (Go templates + htmx) para habilitar comandos/funções por grupo
- [ ] Respostas aleatórias nos grupos
- [ ] Demais comandos do Ravena, por ordem de uso
