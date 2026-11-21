# Guia para portar funcionalidades do Ravena (Node) para o ravena-go

O ravena-go é uma reescrita do zero. O bot Node (`/home/bread/Documents/JoaoDoBread/RavenBot/src`) é só **referência de comportamento**: não traduza linha a linha, escreva Go idiomático, simples e testado. Pode simplificar, cortar opções obscuras e corrigir bugs do original.

Leia antes: `README.md`, `docs/spec/core.md`, `docs/spec/features.md`, `internal/app/app.go`, `internal/bot/{message,router}.go`, `internal/bot/bottest/fake.go` e o exemplo completo `internal/features/sticker/` (código + testes).

## Prioridade nº 1: gastar pouca RAM e CPU

O bot roda no Railway, onde se paga por GB·min. Hoje o processo inteiro usa ~10–30 MB; mantenha assim.

- Nada de cache sem limite: todo mapa em memória tem teto de tamanho e/ou expiração (veja `internal/wa/recent.go`, `internal/policy`).
- Estado persistente vai no SQLite (`d.Store.DB()` + `d.Store.Migrate(ctx, "<feature>", migrations)`), não em memória.
- Dados estáticos (listas de frases etc.): embuta com `go:embed` **só o necessário** (extraia as listas usadas para um arquivo pequeno no seu pacote; `data/custom-variables.json` tem 1 MB e não deve ser embutido inteiro). Faça o parse preguiçoso (`sync.OnceValue`) e só do que for usado.
- Processos externos (ffmpeg, magick) só via `d.Media` / `internal/media` (já limitam concorrência). Nunca rode trabalho pesado num `Observer`.
- Goroutines de fundo (timers de rodada, agendamentos) usam o `ctx` de `Register` e terminam com ele; prefira `time.AfterFunc` por jogo ativo a um ticker global.
- HTTP externo: use `d.HTTP` (timeout 20s), limite o corpo lido (`io.LimitReader`), prefira APIs JSON a scraping.

## Regras para trabalhar em paralelo com outros agentes

Vários agentes escrevem no mesmo repositório ao mesmo tempo.

- Escreva **somente** dentro de `internal/features/<seu-pacote>/` (pode criar mais de um pacote, se o lote pedir).
- **Não edite** `cmd/`, `internal/bot`, `internal/app`, `internal/wa`, `internal/store`, `internal/policy`, `internal/web`, `internal/media`, `internal/llm`, `go.mod`, `go.sum`, `README.md`, nem pacotes de outros agentes. Se faltar algo no núcleo, contorne localmente (interface no seu pacote) e **descreva a necessidade no relatório final**.
- **Sem dependências novas**: só stdlib e o que já está no `go.mod` (whatsmeow, modernc sqlite, rsc.io/qr, golang.org/x/... já presentes indiretamente não contam: confira com `go list -m all` antes de importar).
- Teste e verifique **só seus pacotes**: `go vet ./internal/features/<pkg>/... && go test ./internal/features/<pkg>/...` (o resto da árvore pode estar no meio de edições de outro agente). Rode `gofmt -w` nos seus arquivos.
- Não faça commit nem push; o integrador faz.
- Não registre nada no `main`: exporte `type Module struct{...}` implementando `app.Feature` (`Name()` e `Register(ctx, *app.Deps)`) e informe o nome no relatório.

## Convenções

- Comandos: `bot.Command` com `Name`, `Aliases`, `Description`, `Usage`, `Category` (use: `jogos`, `zoeira`, `utilidades`, `midia`, `ia`, `grupo`, `geral`), `Reactions` (`Before: "⌛️"` só se demorar; `After`/`Error` como no original), `Cooldown`, `AdminOnly`, `GroupOnly`, `Triggers` (emoji de reação), `Hidden`.
- Erros para o usuário: `return bot.Userf("mensagem")`. Erros internos: `return fmt.Errorf(...)` (o roteador mostra mensagem genérica e loga).
- Menções: texto com `bot.MentionText(jid)` + `Outgoing.Mentions`. Quem foi marcado chega em `msg.Mentions`; a mensagem citada em `msg.Quoted`.
- Membros/admins: `c.Client.GroupInfo(ctx, chat)` (tem cache). Admin do autor: `c.IsAdmin(ctx)`.
- Aleatoriedade: `math/rand/v2`. Para testes determinísticos, injete a fonte (campo `rand func(n int) int` ou similar).
- Tempo: injete `now func() time.Time` onde houver cooldown/rodada, para testar sem `sleep`.
- LLM: `d.LLM` pode ser `nil` → responda `bot.Userf("IA indisponível no momento.")`. Use `d.LLM.Chat`/`Ask` com `MaxTokens` razoável.
- Textos em português, tom do Ravena (emoji ok, sem exagero). Mantenha os nomes de comando do original para não quebrar o hábito dos usuários.
- Testes: cada comando com pelo menos o caminho feliz e os erros de uso, usando `bottest.FakeClient`, um `bot.Router` real e `store.Open` num `t.TempDir()`. Sem rede nos testes (use `httptest`).
