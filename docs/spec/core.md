# Especificação do núcleo (referência: RavenBot/Node)

Comportamento que a reescrita em Go precisa reproduzir. Referências `arquivo:linha` apontam para `RavenBot/src`. O Node é só referência: bugs conhecidos estão marcados com **(bug)** e não precisam ser copiados.

Comparado com o núcleo atual do Go (`internal/bot/router.go`), ainda faltam: config por grupo (prefixo, pausa, mute...), checagem de admin, cooldown/debounce, comandos personalizados, listeners de entrada/saída de membros e de mudança de configuração do grupo, envio sem citar, menções e o histórico de mensagens (usado pela IA).

---

## 1. Fluxo de uma mensagem

### 1.1 `EventHandler.processMessage` (EventHandler.js:236)

Ordem exata. Cada passo pode encerrar o processamento:

| # | Passo | Ref |
|---|---|---|
| 1 | Ignora `fromMe` e qualquer `broadcast` (status) | :239 |
| 2 | **Anti-spammer**: só nos grupos fixos `GRUPO_INTERACAO/PESCA/DOWNLOADS`. Durante uma janela de 5 min aberta por um join suspeito, apaga mensagens de autores `63*`/`62*` ou que estejam no set `activeSpammers` | :247, :2021 |
| 3 | **Bloqueio local** (`local_blocks`, por PN ou LID, sem domínio) → ignora em silêncio | :252-273 |
| 4 | **Newsletter/canal**: só detecta MuNews, horóscopo e GIF da Copa, depois sai | :278 |
| 5 | **PV**: se `!ignoreInvites`, o InviteSystem tenta tratar link de convite e depois a mensagem de acompanhamento (§9) | :312-324 |
| 6 | `textContent` = `content` (texto) ou `caption`, com trim. Se vier entre crases (`` `!ping` ``), as crases são removidas | :330 |
| 7 | **Grupo**: guarda a mensagem no histórico (`summaries.chat_conversations`, últimas 200 por chat; imagens e vídeos são descritos por LLM) | :345, SummaryCommands.js:545 |
| 8 | `getOrCreateGroup`: um grupo novo recebe `name` = JID sanitizado, com sufixo aleatório se o nome já existir, e é salvo | :93 |
| 9 | Recebeu mensagem do grupo, então remove o bot de `group.botNotInGroup` e da lista de grupos a pular (skip list) | :355 |
| 10 | **Apelido**: se houver `group.nicks[{numero,apelido}]` para o autor, sobrescreve o nome em todos os campos | :377 |
| 11 | **Frase mágica** `ravena, ajude a recuperar meu grupo!` (só admin): despausa, força o prefixo `!`, responde e loga em `GRUPO_LOGS` | :402 |
| 12 | **Pausado**: só passa texto que comece com `<prefix>g-pausar` | :460 |
| 13 | Ranking de mensagens (`msgranking`) | :476 |
| 14 | **ignoredNumbers**: o autor contém algum número da lista com ≥8 dígitos (substring) → ignora | :484 |
| 15 | **Filtros** (§5.2) → apaga a mensagem e sai | :495 |
| 16 | (PV) também guarda no histórico | :500 |
| 17 | Sem texto → `processNonCommandMessage` (§1.4) | :505 |
| 18 | **Menção ao bot** (§8) | :509 |
| 19 | `prefix = group.prefix ?? bot.prefix`. É comando quando `prefix === ""` ou o texto começa com o prefixo. Espaços depois do prefixo são removidos (`! ping` → `ping`) | :518-528 |
| 20 | Texto começa com `g-` → `handleCommand` sempre, mesmo em PV com `ignorePV` | :532 |
| 21 | Outros comandos: roda se `!ignorePV`, se for grupo, ou se o texto contém algum item de `CMD_WHITELIST` (substring, ex.: `sa-,anoni`) | :543-552 |
| 22 | Não é comando → `processNonCommandMessage` | :557 |

O despacho é *fire-and-forget*: nada espera o comando terminar. Em Go, isso já é feito pelo pool de workers.

### 1.2 `CommandHandler.handleCommand` (CommandHandler.js:463)

1. **Debounce/anti-spam por usuário, global entre grupos** (:466-541). Super admins e o "comuAdmin" (`bot.numeroResponsavel`) ficam isentos.
   - `CMD_DEBOUNCE` (padrão 3000 ms): um comando antes desse intervalo desde o último conta 1 *bypass*, reinicia o cronômetro e é descartado em silêncio.
   - Ao chegar em `DEBOUNCE_BAN` (padrão 10) bypasses, o usuário fica 1 h sem comandos (`bannedUntil = now+3600000`, :506). O bot reage 📵 e envia "⚠️ Prevenção de Spam: ... banido ... por 1 hora".
   - A cada `DEBOUNCE_DECAY` s (padrão 60), cada registro perde 1 bypass. Registros zerados e não banidos são removidos (:55-67). Tudo fica em memória.
   - Qualquer texto com prefixo conta, inclusive comando inexistente.
2. Separa `[command, ...args]` por espaços.
3. **`sa-`** (:552): só para super admin (`SUPER_ADMINS`) ou comuAdmin. O método é procurado em `SuperAdmin.commandMap` (com diferença entre maiúsculas e minúsculas). Se não achar, responde "Comando de super admin desconhecido: X". Se o usuário não for super admin, responde "⛔ Apenas super administradores...".
4. Senão, chama `processCommand`.

**Super admin** (AdminUtils.js `isSuperAdmin`): o ID normalizado (só dígitos antes de `@`/`:`) precisa ter mais de 10 caracteres, e algum item de `SUPER_ADMINS` precisa *começar com* ele.

### 1.3 `processCommand` (CommandHandler.js:622)

| Ordem | Regra |
|---|---|
| 1 | **`g-`** (§1.5). No PV: `g-manage <nome>` liga a gestão do grupo pelo PV (`privateManagement[author]=groupId`, só em memória) se o autor for admin do grupo. `g-manage` sem argumento desliga. `g-painel <nome>` faz o mesmo e gera o link. Os demais `g-` no PV usam o grupo gerenciado e respondem no PV (:636-790) |
| 2 | `g-` em grupo pausado: só `g-pausar` passa (:778) |
| 3 | Outro comando em grupo pausado → ignora (:808) |
| 4 | **Comando fixo** `fixedCommands.getCommand(name)`: compara **só o `name`**, com diferença entre maiúsculas e minúsculas por padrão (`caseSensitive` vale `true` no padrão, Command.js). Comandos com `caseSensitive:false` (ex.: figurinhas) aceitam qualquer caixa. Os `aliases` quase não são usados: cada alias costuma ser um `Command` separado (FixedCommands.js:90) |
| 5 | **Comando personalizado** (só em grupo, se `!skipCustom`): `findCustomCommand` (§3.2) |
| 6 | **Desconhecido**: se o grupo tem lista de comandos personalizados e prefixo não vazio, reage 🆖 (:836). Com `NOTIFY_UNKNOWN_COMMANDS=true`, ainda responde "Comando desconhecido: X" (:847). Sem isso, fica em silêncio |

**`executeFixedCommand`** (CommandHandler.js:1027), checagens em ordem:

1. `mutedCategories` contém `category` em minúsculas → ignora em silêncio (:1047)
2. `mutedCommands` contém `name` → reage ⛔️ (:1058)
3. `needsQuotedMsg` sem mensagem citada → silêncio
4. `adminOnly` sem ser admin → reage 📵 (:1077)
5. `needsMedia` sem mídia direta nem citada → silêncio (:1092)
6. `exclusive` (lista de `group.name`), grupo fora dela → silêncio (:1115)
7. `allowedTimes {start,end,daysOfWeek}` fora do horário → reage 🕒 e responde "só está disponível das X até Y nos dias: ..." (:1125)
8. **Cooldown** (§1.6)
9. Reage `reactions.before`, roda `method`, grava cooldown e loga uso (`cmd_usage`)
10. Resultado: uma `string` vira um ReturnMessage citado. A cada ReturnMessage aplica `reaction=after` e, **em grupo, adiciona o autor às menções** (:1213). Depois reage `after` (atraso de 1 s). Em erro: reage `error` e responde "Erro ao executar comando: X"

**Admin de grupo** (AdminUtils.js `isAdmin`), em ordem: super admin → `group.additionalAdmins` (números normalizados) → comuAdmin, se o bot estiver no grupo → admin/superadmin do WhatsApp nos participantes (compara id, phoneNumber e lid).

### 1.4 `processNonCommandMessage` (EventHandler.js:572)

1. **autoStt**: áudio/voz/ptt. No PV transcreve sempre (exceto com `ignorePV`). Em grupo, só com `group.autoStt`. O texto vira `Audio[...]` e vai para o histórico (SpeechCommands.js:702)
2. PV + `bot.autoDownloadPV` + link de YouTube/Instagram/Facebook/TikTok/Twitter → baixa (:599)
3. PV → figurinha automática de mídia (:615). No Go já existe (`sticker.AutoSticker`)
4. PV + `bot.pvAI`: junta as mensagens de texto por 8 s (`PV_AI_DEBOUNCE_MS`, :57) e manda o conjunto ao `aiCommand`
5. Grupo: **SillyInteraction** (§4). Se tratou, para
6. Grupo com `customIgnoresPrefix`: `processCustomIgnoresPrefix` testa o **texto inteiro** como gatilho (match exato ou "começa com") (CommandHandler.js:1577)
7. Grupo: `checkAutoTriggeredCommands`, que cuida da interação automática (§6) e dos gatilhos `ignorePrefix` (§3.4)

### 1.5 Comandos `g-` (CommandHandler.js:868)

- Reage ⌛ (`LOADING_EMOJI`). Sem grupo, responde "Comandos de gerenciamento só podem ser usados em grupos. Use !g-manage..."
- Quem não é admin recebe "⛔ Apenas administradores podem usar comandos de gerenciamento."
- O método vem de `Management.commandMap[nome sem 'g-']`, com **diferença entre maiúsculas e minúsculas** (Management.js:390). Se não existir: "Comando de gerenciamento desconhecido: X"
- Ao gerenciar pelo PV, a resposta com `chatId == group.id` é redirecionada ao PV. No fim reage ✅

### 1.6 Cooldown

- Tabela SQLite `cooldowns(context_key, command, timestamp)` com `context_key = "${botId}_${chatId}"` (CommandHandler.js:150). **O cooldown é por chat, não por usuário.**
- O tempo restante usa o formato `Xs` / `Xm Ys` / `Xh Ym`. Em cooldown, o bot reage 😴 e responde "O comando 'X' está em cooldown, aguarde T para usar novamente." no máximo uma vez a cada 30 s por chat e comando (:242-270)

### 1.7 Envio (`sendReturnMessages`, WhatsAppBotGo.js:1165)

- `ReturnMessage {chatId, content (texto|mídia), options{quotedMessageId, mentions[], caption, sendMediaAsSticker, sendVideoAsGif, sendMediaAsDocument, customMime...}, reaction, delay}`
- Com `group.autoTranslateTo`, **todo** texto e legenda passa por `llmTranslate` (LLM com cache em `translation-cache`) antes do envio (:1175)
- `delay` acontece antes do envio. `reaction` é aplicada à mensagem enviada
- `aggregateReturnMessages` (CommandHandler.js:403): ao rodar comandos embutidos, junta textos consecutivos com `\n-----------\n` e adiciona 500 ms de atraso por mensagem

---

## 2. Modelos

### 2.1 Group (models/Group.js), salvo em `core.db.groups` (colunas JSON via GroupMapper)

| Campo | Padrão | Significado |
|---|---|---|
| `id` | — | JID `...@g.us` |
| `name` | JID sanitizado (até 30 caracteres) | Nome de cadastro **único**, usado em `g-manage`, `exclusive`, pacote de figurinha e painel. `[a-z0-9_.-]`, minúsculas |
| `titulo` / `descricao` | null | Título e descrição reais do WhatsApp, preenchidos quando o bot entra |
| `addedBy` / `removedBy` | null / false | Quem adicionou / removeu o bot |
| `prefix` | `!` (`bot.prefix` em grupo novo) | `""` permitido: qualquer texto vira comando. No painel, no máximo 1 caractere |
| `customIgnoresPrefix` | false | Comandos personalizados disparam sem prefixo (§1.4.6) |
| `inviteCode` | null | — |
| `paused` | false | Só `g-pausar` funciona. Volta a `false` quando o bot reentra no grupo (EventHandler.js:1132) |
| `additionalAdmins` | [] | Números (`...@c.us`) tratados como admin do bot |
| `filters` | `{nsfw:false, links:false, words:[], people:[]}` | §5.2 |
| `twitch` / `kick` / `youtube` | [] | Configurações de canal monitorado (§10) |
| `botNotInGroup` | [] | IDs de bots que saíram (multi-bot) |
| `webhooks` | [] | Até 10 `{name, header{name,value}, headerValue:'match'|'include', template, bot}` |
| `greetings` / `farewells` | {} | `{text: "...", image|video|audio|sticker: {file, caption}}`, um item por tipo |
| `interact` | `{enabled:true, useCmds:true, lastInteraction:0, cooldown:30, chance:100, proporcao:50}` | §6. No Node, `enabled` vale `true` no construtor, mas `false` quando criado por `g-interagir` (Management.js:4504) |
| `autoTranslateTo` | false | Ex.: `"Spanish (ES)"`. O painel aceita 17 idiomas (BotAPI.js:1750) |
| `autoStt` | false | Transcreve áudios no grupo |
| `ignoredNumbers` | [] | Dígitos (≥8). O bot ignora tudo dessas pessoas (§1.1.14) |
| `ignoredUsers` | [] | Prefixos de ID **excluídos de menções em massa** (atenção/sorteio/stream) (GroupCommands.js:43, StreamSystem.js:617). Nenhum comando grava aqui, só dados antigos ou o `update-group` do painel |
| `mutedCommands` | [] | Nomes de comandos fixos. `g-mute` silencia todos os comandos com o mesmo `method` |
| `mutedCategories` | [] | Categorias em minúsculas |
| `nicks` | [] | `{numero, apelido}`, apelido com até 20 caracteres |
| `warnings` | [] | `{jid, numero, count}`. Só contador, sem punição automática |
| `customAIPrompt` | [] (na prática uma string) | Personalidade da IA, até 1500 caracteres. Gerada por LLM quando o bot entra num grupo novo (EventHandler.js:1314) |
| `notificaGrupoFechado` / `notificaGrupoAberto` | false | §5.5 |
| `createdAt` / `updatedAt` | now | — |

### 2.2 Command (models/Command.js), campos que importam

`name, aliases, description, usage, category, group (agrupa no menu), needsMedia, needsQuotedMsg, needsArgs, minArgs, adminOnly, caseSensitive(true), exclusive[], ignoreInteract, reactions{trigger (emoji ou lista), before(⌛), after(✅), error(❌)}, cooldown(s), allowedTimes, replyInPrivate, method, hidden, active`.

`reactions.trigger` corresponde a `bot.Command.Triggers` no Go. O ReactionsHandler (ReactionsHandler.js:98) busca a mensagem que recebeu a reação, respeita `mutedCommands` e `paused` e chama `executeFixedCommand` com o texto dela como args. Reações em grupos de log/anúncio são ignoradas (WhatsAppBotGo.js:1686).

---

## 3. Comandos personalizados

### 3.1 Formato (custom_commands.db.custom_commands, PK `(group_id, trigger)`)

Colunas: `trigger (=startsWith), responses JSON[], admin_only, active, deleted (soft delete), count, last_used, created_by, created_at, metadata`. Os demais campos ficam em `json_data` (CommandMapper.js):

```
startsWith, responses[], adminOnly, ignoreInteract, sendAllResponses, mentions[] (JIDs),
cooldown (s, 1–60000), react (emoji|null), reactions{before,after}, reply (cita? padrão true),
replyInPvivate (sic), allowedTimes{start "HH:MM", end, daysOfWeek["seg",...]}, ignorePrefix, caseSensitive,
count, lastUsed, metadata{createdBy, createdAt}, active, deleted
```

Criação (`g-addCmd`, Management.js:550):

- Respondendo a uma mensagem: o gatilho são todos os args (em minúsculas) e a resposta é o texto ou a mídia citada.
- Sem mensagem citada: o gatilho é `args[0]` e a resposta é o resto do corpo, com quebras de linha preservadas.
- O prefixo é removido do gatilho, que pode ter até 10 palavras. As menções da mensagem citada vão para `mentions`.
- Mídia é salva em `data/media/<ts>-<rnd>.<ext>` e a resposta vira `{image|video|audio|voice|sticker|gif|document-<arquivo>} legenda`. Para figurinha animada: `{stickerGif-<url>}`.
- Limites (painel): `LIMIT_COMMANDS=100` e `LIMIT_STORAGE_MB=1024` por grupo (BotAPI.js:1901).

### 3.2 Casamento (`findCustomCommand`, CommandHandler.js:1273)

Monta prefixos crescentes: `w0`, `w0 w1`, … até 10 args.

1. **Match exato**, do maior para o menor (sem diferença de caixa, a menos que `caseSensitive`)
2. **Match parcial**: a entrada *começa com* `startsWith`, do maior para o menor. Ex.: o gatilho `oi` casa com `!oizinho`

Os args que sobram vão para o comando.

### 3.3 Execução (`executeCustomCommand`, CommandHandler.js:1333)

Ordem:

1. Sem respostas → ignora
2. `adminOnly`: reage ⛔️ e responde "o comando *X* só pode ser usado por _administradores_."
3. `allowedTimes` fora do horário: 🕒 + mensagem
4. Cooldown (chave = gatilho)
5. Reage `reactions.before`
6. `count++`, `lastUsed`, persiste
7. Reage `react`
8. Grava cooldown
9. Escolhe a resposta: `sendAllResponses` envia **todas**, em ordem. Caso contrário, **uma aleatória uniforme** (:1510). Nunca é sequencial
10. Cada resposta passa por `processCustomCommandResponse`. Com `replyInPvivate`, vai para o PV. Menções = as da resposta ∪ `command.mentions` ∪ autor (só em grupo)
11. Reage `reactions.after`

**Mutes e categorias não se aplicam a comandos personalizados.**

Na resposta (`processCustomCommandResponse`, :1599), as variáveis são processadas (§3.5) e o resultado pode ser:

| Resultado | Envio |
|---|---|
| `embedded-commands` (texto só com `{cmd-...}`, até 10) | Roda cada um como comando fixo (`skipCustom`, silencioso), com os args do personalizado anexados. Os resultados são agregados |
| `embedded-command` | Um comando fixo |
| Array de mídias (`{file-pasta/}`) | Até 5 mídias, com 1 s entre cada |
| Objeto de mídia (`{file-x}`, `{reddit-}`, foto de perfil) | Mídia com legenda |
| `^{(audio|voice|image|video|gif|document|sticker|stickerGif)-arquivo} legenda` | Lê `data/media/arquivo`. `sticker` vai como figurinha, `gif` como vídeo-gif, `audio` com mime `audio/mpeg` (:1762) |
| Texto | Mensagem de texto. `reply` cita a mensagem original |

### 3.4 Gatilhos automáticos (`ignorePrefix`)

Em grupo não pausado, depois da interação automática, o primeiro comando com `ignorePrefix` cujo `startsWith` está **contido** no texto (sem diferença de caixa) é executado sem argumentos (CommandHandler.js:1925). **Nenhum comando ou endpoint atual grava `ignorePrefix`.** Só existe em dados antigos. Para o mesmo efeito hoje, use `customIgnoresPrefix` no grupo inteiro.

### 3.5 Variáveis (`utils/CustomVariableProcessor.js`)

Pipeline em `process()` (:27):

1. Foto de perfil
2. `{file-}` sozinho
3. `{reddit-}`
4. `{API#}`
5. Variáveis de sistema
6. Estáticas (JSON)
7. Contexto
8. Dinâmicas
9. `{cmd-}`
10. Menções implícitas

| Variável | Resolve para |
|---|---|
| `{day}` | Nome do dia (`Domingo` … `Sábado`) |
| `{date}` / `{time}` | `toLocaleDateString()` / hora local |
| `{data-hora}` `{data-minuto}` `{data-segundo}` `{data-dia}` `{data-mes}` `{data-ano}` | Componentes com 2 dígitos (ano com 4) |
| `{randomPequeno}` / `{randomMedio}` / `{randomGrande}` / `{randomMuitoGrande}` | Inteiro aleatório 1–10 / 1–100 / 1–1000 / 1–10000, sorteado por ocorrência |
| `{rndDado-X}` | 1–X |
| `{rndDadoRange-X-Y}` | X–Y |
| `{somaRandoms}` | Deveria somar os números do texto. **(bug)** a regex com escape duplo `\\s+`/`\\d+` faz retornar sempre 0 (:322) |
| `{pessoa}` / `{nomeAutor}` | pushName ou apelido do autor ("ravena" se for o próprio bot, "Pessoa" se não houver) |
| `{group}` / `{nomeGrupo}` / `{nomeCanal}` | `group.name` (nome de cadastro) |
| `{tituloGrupo}` | **Só em boas-vindas e despedidas**: título real do grupo |
| `{contador}` | `command.count` (já incrementado) |
| `{membroRandom}` | Nome de um participante aleatório (exceto o bot) |
| `{mention}` | `@numero` + menção. Fonte: 1) pessoas marcadas na mensagem (aleatório, sem repetir) 2) autor da mensagem citada 3) membro aleatório. Cada ocorrência é uma pessoa diferente |
| `{singleMention}` | Igual, mas a mesma pessoa em todas as ocorrências |
| `{mentionOuEu}` | Igual a `{singleMention}`, mas cai no próprio autor como fallback |
| `{mention-NUMERO[@dom]}` | `@NUMERO` + menção (8–25 dígitos; resolve LID/PN) |
| `@123456789…` no texto final | Vira menção automática (8–25 dígitos) (:117) |
| `{fotoPerfil}` / `{fotoPerfilMention}` | Envia a foto de perfil do autor ou do primeiro marcado, com o texto como legenda (≤1000 caracteres; acima disso, a foto vai separada) (:1112) |
| `{file-caminho}` (texto inteiro) | Arquivo do FileManager do chat (`files.db`). Se for pasta (`{file-pasta/}`), até 5 arquivos |
| `{reddit-sub1-sub2}` | Sorteia um subreddit e busca `https://www.reddit.com/r/<sub>/new.json?limit=100`. Escolhe mídia (imagem/gif/vídeo) não repetida por grupo e devolve a mídia |
| `{API#GET\|POST\|FORM#TEXT\|JSON#url[\ntemplate]}` | HTTP. Em `TEXT`, devolve o corpo. Em `JSON`, o template usa `[a.b.c]` para ler caminhos. POST/FORM tiram os campos da query string. **(bug)** `argN` na URL deveria virar o argumento N, mas lê `context.command.args`, que não existe, então nunca substitui (:790) |
| `{weather:local}` | **Placeholder falso** fixo: "Ensolarado, 25°C" (:985) |
| `{cmd-!comando args}` | Texto só com `{cmd-}` executa os comandos. Misturado com texto, vira `[Comando embutido: X]` |
| `{<chave>}` de `data/custom-variables.json` | String → valor fixo. Array → item aleatório. São 60 listas (nomeMasculino/Feminino, municipio, peixe(s), pokemonEN, emoji*, genshin*, carro2024, presente, cantadas-ruins, biscoito-frases, statusZap, pecados, violencia...). **(bug)** a regex com flag `g` troca todas as ocorrências pelo mesmo item (:742). Cache de 5 min |

Boas-vindas e despedidas também têm `{pessoa}` = `@num` de todos que entraram, `{nomePessoas}`/`{numeroPessoas}` (iguais) e plurais `{plural_s}` `{plural_S}` `{plural_m}` `{plural_M}` → `s`/`m` ou vazio e `{plural_esao}` → `são`/`é`, além do pipeline acima (EventHandler.js:1673).

---

## 4. Interações "bobas" (SillyInteractionHandler.js)

- Só em grupo, depois de passar os filtros, em mensagem que não é comando. Cooldown de 30 s por chat (em memória).
- Normaliza o texto (minúsculas, sem acento), remove intensificadores (`muito, super, tao, bem, bastante, extremamente, ..., demais, e, um, uma`) e testa com nomes `bot, boot, ravena` + `bot.nomeExibir`:
  - **Xingamento**: o texto *começa com o nome* e *termina com* `lixo|podre|inutil|idiota|burro(a)|merda|lento|lerdo(a)|chato(a)|ridiculo(a)|pessimo(a)|insuportavel|...`, ou *começa com* `odiei o/a|cala boca|vai se ferrar|que merda de|lixo de|...` e *termina com o nome*.
  - **Elogio**: mesma ideia com `lindo(a)|fofo(a)|maravilhoso(a)|bom/boa|perfeito(a)|inteligente|util|melhor|incrivel|diva|deusa|brabo(a)` e `amei o/a|adoro|te amo|obrigado(a)|parabens|...`. Xingamento é testado primeiro.
- Incrementa `sillies.db.stats(category,count)`.
- **25%**: o LLM responde citando a mensagem. Prompts:
  - Xingamento: "Usuário X te xingou falando '...'. Responda xingando ele de volta... witty".
  - Elogio: "...Responda de forma fofa...".
  - System: "Atrevida se for xingada, fofa e gentil se for elogiada. Não use muitas emojis."
- **75%**, ou quando o LLM falha: figurinha aleatória de `data/sillies/<xingamentos|elogios>/*.{webp,png,jpg,gif}`, com autor `nomeExibir` e pacote "Silly". No repositório só existem 2 webps em `xingamentos/` e `elogios/` não existe, então o elogio sem LLM não envia nada. `sa-addElogio` e `sa-addXingamento` adicionam figurinhas.

---

## 5. Recursos de grupo

### 5.1 Entrada e saída (EventHandler.js:913, :1474)

- Join em comunidade ou canal de anúncios → ignora.
- Join de alguém em `filters.people` (PN ou LID) → **remove na hora** (:964).
- Join de `63*`/`62*` nos grupos fixos → spammer, com janela de 5 min (:995). Ao entrar nesses grupos, remove todos os `63/62` e também da comunidade (:1946).
- **Bot entrou**: despausa, grava `group_membership_periods` e loga em `grupoLogs`. Procura em `pending_joins` quem convidou (membro do grupo ou presente no payload); essa pessoa vira `addedBy` e entra em `additionalAdmins`. Em grupo novo, envia `data/textos/groupJoin.txt` (`{prefix}`) + "Adicionado por" + nome de cadastro + dica `!g-painel`/`g-manage`, e chama o LLM com `json_schema` `{welcomeMessage, botPersonality}`. A mensagem de boas-vindas sai 5 s depois e a personalidade vai para `customAIPrompt`. Em grupo já conhecido, usa `groupJoinExistente.txt`. *Join silencioso* (`sa-joinGrupoSilencioso` / `sa-modoSilencioso`) e `bot.sendJoinInfo=false` suprimem as mensagens.
- **Membro entrou** + `greetings`: uma mensagem por tipo (texto + mídia com legenda), com menção a quem entrou (:1445, :1634).
- **Bot saiu**: adiciona o grupo à skip list, fecha o período e loga o histórico de estadias. **Membro saiu** + `farewells`: igual às boas-vindas (:1609).

### 5.2 Filtros (`applyFilters`, EventHandler.js:708)

Não há exceção para admin. Mensagens com `g-filtro` não são filtradas. A mensagem filtrada é apagada (`delete(true)`) e não segue o fluxo.

| Filtro | Regra |
|---|---|
| `words` | Substring em minúsculas de qualquer palavra/frase da lista |
| `links` | Regex `https?://\S+` |
| `people` | Compara os últimos 10 dígitos de author/authorAlt e das conversões PN↔LID com a lista |
| `nsfw` | Imagem/figurinha/vídeo via `NSFWPredict` (LLM com visão; `NSFW_THRESHOLD`). Ligar exige que o bot seja admin (Management.js:2343) |

### 5.3 Apelidos, advertências, admins extras, ignorados

- `g-setApelido @p nome` grava `nicks`. O apelido substitui o nome em todos os comandos (§1.1.10).
- `g-advertir @p` faz `count++`. Os emojis indicam o nível (`[🟡..]`, até `[⚫️⚫️⚫️]` com ≥12). Não remove ninguém.
- `g-customAdmin <num>` alterna `additionalAdmins` (`num@c.us`).
- `g-ignorar <num>` alterna `ignoredNumbers`.

### 5.4 autoStt / autoTranslateTo

Ver §1.4.1 e §1.7. A tradução atinge **todas** as saídas do grupo.

### 5.5 Grupo fechado/aberto (`processGroupSettingsUpdate`, EventHandler.js:2076)

Evento `announce`. Mudança feita pelo próprio bot → ignora. Com a flag ativa, envia "🔒 *Grupo fechado.* Apenas administradores..." ou "🔓 *Grupo aberto.* ...", sem dizer quem mudou.

---

## 6. Interação automática ("interagir")

Em `checkAutoTriggeredCommands` (CommandHandler.js:1850), para toda mensagem de grupo que não é comando e não foi tratada pelos sillies:

1. É preciso `interact.enabled` e `now - lastInteraction ≥ max(cooldown, 30) min` (:1869). O máximo configurável é 43200 min.
2. Sorteia `1..10000`. Dispara se o valor for `≤ min(chance, 500)` (:1878). O padrão `chance` 100 dá 1%. O setter força o intervalo 1–500 (no máximo 5%).
3. Grava `lastInteraction` (`updateGroupInteract`).
4. `autoCommands` = comandos personalizados ativos com `!ignoreInteract`, se `useCmds`. Usa o LLM se não houver nenhum ou se `random*100 < proporcao` (padrão 50; intervalo 0–100) (:1899).
   - **LLM**: roda o comando fixo `interagir` sem reações e sem mensagem de erro (`args=[false]`). Ele também respeita mute e o próprio cooldown de 150 s.
   - **Comando**: um personalizado aleatório, sem args e sem reações.

**Comando `!interagir`** (SummaryCommands.js:239, :768): categoria `ia`, cooldown 150 s, trigger por reação 🦜, reações ⌛/💬.

- Lê **todo** o histórico guardado do chat (até 200 mensagens, `autor: texto`; imagens viram `Imagem[tipo|sfw|descrição]`).
- Prompt: "Responda apenas em português do brasil. A seguir anexei uma conversa recente de um grupo de WhatsApp. Crie uma única mensagem curta para interagir com o grupo de forma natural, como se você entendesse o assunto e quisesse participar da conversa com algo relevante. Tente usar o mesmo tom e estilo informal que as pessoas estão usando. A mensagem deve ser curta e natural. ((Sua personalidade: '<customAIPrompt>'))" + mensagens.
- Os `@números` da resposta viram menções.
- Em sucesso, **apaga o histórico do chat**. Respostas com "Não foi poss" são descartadas em silêncio no modo automático.

O mesmo histórico alimenta `!resumo` (cooldown 300 s) e o "dossiê": com ≥10 mil caracteres pendentes, roda uma classificação de moderação por LLM, `{type, summary, problematic_score 0-10}` (SummaryCommands.js:356).

---

## 7. Mídia e menções em massa

`ignoredUsers` exclui pessoas das menções a todos (GroupCommands/Sorteio/Stream). O bot marca o autor em toda resposta de comando em grupo (§1.3). Para menções e envio sem citar, o Go precisa de `Outgoing.Mentions` e de um `Send` além do `Reply`.

## 8. Menção ao bot (MentionHandler.js)

Só em grupo. Exige que a mensagem tenha menções e que o texto *comece com* `@<lid do bot>` (ou `<@id>` no Discord), ou que o bot esteja entre as menções.

- Reage ⌛ e remove a menção do início.
- Prompt vazio → "Olá! Como posso te ajudar? ... envie !cmd" com reação 🤖.
- Caso contrário, chama `aiCommand` (o `!ia`, de AICommands.js; usa `customAIPrompt`) com o resto como args.

Isso acontece **antes** do teste de prefixo.

## 9. Sistema de convites (InviteSystem.js, só no PV, se `!bot.ignoreInvites`)

1. Detecta `chat.whatsapp.com/<code 10-50>`.
   - Usuário em `soft_blocks.block_invites` → 🛑 e aviso, no máximo 1 a cada 7 dias.
   - Código ou JID em `blocked_invites` → 🛑 e aviso.
   - Cooldown de 60 min por usuário (reage ⏰) e por código (⏱️ + aviso, no máximo 1/h).
2. Envia `data/textos/invites_pre.txt` + um código aleatório e espera o motivo. Sem motivo em 5 min: "Seu convite não foi processado...".
3. Junta as mensagens de motivo por 30 s.
   - Motivo com menos de 15 caracteres: uma segunda chance. Depois disso, `invite_ignorado.txt` e punição de 10× o cooldown.
   - Motivo igual ao código ou cópia do texto pré-convite → punição.
4. `getInviteInfo`:
   - Comunidade (`IsParent`) → aviso.
   - Dono = autor → ✅.
   - Outros bots (`bot.otherBots`) já no grupo → aviso.
   - 401/not-authorized → "bot já foi removido...".
   - Grupo com ≤2 membros → aviso de "grupo particular".
   - Caracteres estranhos (math, zalgo...) no nome → aviso.
5. Grava `pending_joins` e `invite_history`. Envia ao usuário "Seu convite foi recebido..." + `invites_pos.txt`.
6. Envia a `GRUPO_INVITES` um resumo (doador 💸, número de convites do autor e do grupo, outros que indicaram, histórico de estadias, motivo) e mais duas mensagens prontas: `!sa-joinGrupo <code> <autor> <nome>` e `!sa-blockInvites <num> <code>`. O dono só copia e cola.

## 10. Monitoramento de streams (StreamSystem.js + services/StreamMonitor.js)

- Polling com `setInterval` (StreamMonitor.js:555):
  - Twitch Helix `api.twitch.tv/helix/streams|users`, OAuth `id.twitch.tv` (`TWITCH_CLIENT_ID/SECRET`).
  - Kick `api.kick.com/public/v1/channels`, OAuth `id.kick.com` (`KICK_CLIENT_ID/SECRET`).
  - YouTube via RSS `youtube.com/feeds/videos.xml` (sem chave).
- Configuração por canal (Management.js:2930): `{channel, onConfig{media[{type:text|image|video|audio|sticker, content|file, caption}]}, offConfig{media[]}, changeTitleOnEvent, onlineTitle, offlineTitle, groupPhotoOnline/Offline, useThumbnail, useAI, mentionAllMembers}`.
- Texto padrão Twitch/Kick: `⚠️ ATENÇÃO!⚠️ 🌟 *canal* ✨ está *online* streamando *{jogo}*! _{titulo}_ link`. YouTube: `*{author}:* *{title}* {link}`.
- Por evento (online/offline/vídeo novo): opcionalmente muda o título e a foto do grupo (o bot precisa ser admin), envia mídias e texto (ou texto gerado por LLM com `customAIPrompt`) e marca todos (exceto `ignoredUsers`).
- Com vários bots no grupo, só um notifica (`botNotInGroup`, `g-streamRefresh`).
- Limite `LIMIT_STREAMS=20` por grupo. Comandos de consulta: `streams, streamstatus, streamers, live, live-kick, topstreams` (StreamCommands.js).

## 11. Webhooks por grupo (BotAPI.js:3960, só com `GROUP_WEBHOOKS`)

Servidor separado: `POST /:botId/:groupId`. Valida o header (`match` ou `include`), preenche o template com `{{caminho.json}}` e envia ao grupo no máximo 1 mensagem a cada 120 s, concatenando as que chegarem no intervalo. Configuração via `g-setWebhook`, respondendo a um JSON.

---

## 12. Dados persistentes

`data/sqlites/*.db`, um SQLite por módulo (`database.getSQLiteDb(name, schema)`). O núcleo usa better-sqlite3 (CoreRepository). Há backup local de 6 em 6 h e réplica MySQL opcional.

| Banco.tabela | Conteúdo | Reescrita |
|---|---|---|
| `core.groups` | Group (§2.1), colunas JSON + `json_data` legado | **Essencial**. Migrar |
| `custom_commands.custom_commands` | §3.1 | **Essencial**. Migrar com `data/media/` |
| `core.pending_joins`, `invite_history`, `soft_blocks`, `blocked_invites`, `local_blocks`, `group_membership_periods` | Convites e bloqueios | Necessário se houver convites |
| `core.donations` | Doadores `{name, valor, numero, historico}` | Útil (`!doadores`, destaque de convite) |
| `cooldowns.cooldowns` | §1.6 | Recriar (estado efêmero) |
| `summaries.chat_conversations / group_dossier_status / group_dossiers / conversations` | Histórico para IA e dossiê | Recriar. Necessário para `interagir`/`resumo` |
| `sillies.stats` | Contador | Opcional |
| `web_management.tokens` | Tokens do painel (`MANAGEMENT_TOKEN_DURATION`=30 min) | Recriar no painel novo |
| `cmd_usage`, `llm_stats`, `media_stats`, `load_reports` | Análises de uso | Opcional |
| `translation-cache`, `cache`, `video_cache`, `skip_groups`, `greeted_users` | Caches | Recriar |
| Jogos e utilidades (`fishing`, `pinto`, `roleta`, `anagrama`, `sorteios`, `relacionamentos`, `lists`, `lembretes`, `files`, `correios`, `food_tracker`, `msgranking`, `stream_monitor`, `ytchannels`, `copa_seguir`, `munews`, `horoscopo`, `placas`, `raffle_cache`, `anon_msgs`) | Por funcionalidade (ver `features.md`) | Migrar `fishing`, `lembretes`, `lists`, `files`, `relacionamentos` e rankings, se os usuários se importarem |
| Arquivos: `data/media/` (mídias de comandos, boas-vindas e streams), `data/textos/*.txt` (groupJoin, groupJoinExistente, invites_pre/pos, invite_ignorado, discord), `data/custom-variables.json`, `data/sillies/`, `data/data-share/<gid>.json` (`/getData`), `data/fonts`, `data/profile-pics` | — | Copiar |

---

## 13. Web e admin (BotAPI.js + public/)

**Painel do grupo**:

- `!g-painel` gera um token (`web_management`, 30 min, Management.js:6288) e o link `/manage/:token` (public/management.html).
- O token é validado em `GET /api/validate-token` e os dados são lidos em `GET /api/group`.
- `POST /api/update-group {token, groupId, changes}` aplica **qualquer chave** no Group (BotAPI.js:1704). Valida o nome (`[a-z0-9_.-]{1,30}`, único), o prefixo (≤1 caractere), o idioma de tradução e o limite de streams.
- `POST /api/upload-media` (limite de armazenamento).
- `GET|POST /api/custom-commands/:groupId`, `PUT|DELETE /api/custom-commands/:groupId/:trigger`.
- `GET /api/group-dossier-history`.

O que o painel configura:

| Seção | Campos |
|---|---|
| Geral | Nome de cadastro, prefixo, bot habilitado (`paused`), personalidade da IA (1500), comandos personalizados sem prefixo, lista de membros + apelidos |
| Dossiês | Histórico (somente leitura) |
| Administrativas | `ignoredNumbers`, filtros (links, NSFW, palavras, pessoas), `mutedCategories`, `mutedCommands`, `additionalAdmins` |
| Automatizações | autoStt, notificar grupo fechado/aberto, interação (ligada, chance, cooldown, proporção), boas-vindas/despedida (texto + mídias), autoTranslate (seletor de idioma) |
| Streams Twitch/Kick/YouTube | Tabela + modal: canal, marcar todos, IA, mudar título, título online/offline, mudar foto, foto online/offline, mídias on/off |
| Comandos personalizados | Tabela + modal: gatilho, ativo, interage (`!ignoreInteract`), responder citando, enviar todas, só admin, emoji de reação, cooldown, marcar pessoas, respostas (texto/mídia), seletor de variáveis e de emoji |

**Admin do dono** (Basic Auth `BOTAPI_USER/BOTAPI_PASSWORD`, ou `managementUser/PW` por bot):

- `dashboard.html` e `/api/bots` GET/POST (bots.json).
- QR e pareamento: `/qrcode/:botId`, `/qrcode-stream`, `/qrimg`, `/qrcode-status`, `/qrcode-initconnect`, passkey `/passkey/respond|confirm`.
- `/restart|logout|recreate/:botId`, `/api/logs`.
- `/llm-stats`, `/api/llm/queue|stats`, `service-providers.html` + `/api/service-providers` (provedores de LLM).
- `groups-dossier.html` + `/api/groups-dossier`.

**Públicos**:

- `/health`, `/api/services/status`, `/api/bot-stats`, `/analytics`, `/getLoad`, `/api/stream` (SSE).
- `/cmd` e `/api/public-commands` (lista de comandos), `/ajuda` + `/api/ajuda/chat` (chat de ajuda com LLM).
- Páginas web de funcionalidades: `/stt` (`/api/stt/transcrever`), `/tts`, `/imagine`, `/pesca` (`/api/fishing/*`).
- `/top-donates`, `/recent-top-donates`, `/media-direct/:file`, `/getData/:gid/:var`, `/discord`, `/telegram`, `/ciclo-ravena`, `/copa` (POST).
- Webhooks: doação `/donate_tipa` (`TIPA_TOKEN`) e no-break `/UPS/powerChange|powerCritical` (`UPS_API_SECRET`).

Para o painel novo (Go templates + htmx), basta o **painel do grupo** com autenticação por token de uso único gerado pelo `!g-painel`, mais um admin mínimo (QR/pareamento, status, lista de grupos).

---

## 14. Comandos `g-` (Management.js:21)

Todos exigem admin (§1.5). As chaves diferenciam maiúsculas e minúsculas.

| Comando | O que faz | Campo |
|---|---|---|
| `g-setNome <nome>` | Renomeia o cadastro (minúsculas, único) | `name` |
| `g-setPrefixo [p]` | Define o prefixo. Sem argumento, remove (`""`) | `prefix` |
| `g-setCustomSemPrefixo` | Alterna se comandos personalizados precisam de prefixo | `customIgnoresPrefix` |
| `g-setBoasvindas [texto]` / responder mídia | Texto ou mídia de boas-vindas. Sem argumento, lista os tipos ativos | `greetings.{text|image|video|audio|sticker}` |
| `g-delBoasvindas <tipo>` | Remove um tipo | `greetings` |
| `g-setDespedida` / `g-delDespedida` | Idem, para saída | `farewells` |
| `g-autoStt` | Alterna a transcrição automática | `autoStt` |
| `g-info` | Mostra a configuração completa do grupo | — |
| `g-manage [nome]` | Gestão pelo PV (§1.3) | memória |
| `g-setAutoTranslate [idioma]` | Define ou desliga a tradução | `autoTranslateTo` |
| `g-addCmd <gatilho> [resp]` | Cria comando personalizado (§3.1) | custom_commands |
| `g-addCmdReply <gatilho>` | Adiciona resposta (texto/mídia citada) | `responses[]` |
| `g-delCmd <gatilho>` | Soft delete | `deleted=true` |
| `g-cmd-enable` / `g-cmd-disable` | Liga/desliga | `active` |
| `g-cmd-setPV` | Alterna resposta no PV | `replyInPvivate` |
| `g-cmd-enviarTudo` | Alterna entre todas e aleatória | `sendAllResponses` |
| `g-cmd-responder` | Alterna citar a mensagem | `reply` |
| `g-cmd-react <cmd> <emoji>` | Reação depois | `reactions.after` |
| `g-cmd-startReact <cmd> <emoji>` | Reação antes | `reactions.before` |
| `g-cmd-setAdm` | Alterna só admin | `adminOnly` |
| `g-cmd-setInteragir` | Alterna participação na interação automática | `ignoreInteract` |
| `g-cmd-cd <cmd> <s>` | Cooldown 0 ou 1–60000 s | `cooldown` |
| `g-cmd-setHoras <cmd> HH:MM HH:MM` | Janela de horário | `allowedTimes.start/end` |
| `g-cmd-setDias <cmd> seg,ter…` | Dias permitidos | `allowedTimes.daysOfWeek` |
| `g-filtro-palavra [frase]` | Alterna a palavra. Sem argumento, lista | `filters.words` |
| `g-filtro-links` | Alterna | `filters.links` |
| `g-filtro-pessoa @p` | Alterna as pessoas marcadas | `filters.people` |
| `g-filtro-nsfw` | Alterna (exige bot admin) | `filters.nsfw` |
| `g-ignorar [num]` | Alterna. Sem argumento, lista | `ignoredNumbers` |
| `g-mute [cmd]` | Alterna o comando fixo e os que têm o mesmo `method`. Aceita emoji de reação | `mutedCommands` |
| `g-muteCategoria [cat]` | Alterna a categoria | `mutedCategories` |
| `g-customAdmin [num]` | Alterna o admin extra | `additionalAdmins` |
| `g-pausar` | Alterna a pausa | `paused` |
| `g-interagir` | Alterna a interação automática e mostra a configuração | `interact.enabled` |
| `g-interagir-cmd` | Alterna o uso de comandos personalizados | `interact.useCmds` |
| `g-interagir-cd <min>` | Cooldown 30–43200 | `interact.cooldown` |
| `g-interagir-chance <n>` | 1–500 (/10000) | `interact.chance` |
| `g-interagir-proporcao <0-100>` | % de IA | `interact.proporcao` |
| `g-ban @p` (oculto) | Remove do grupo | — |
| `g-block @p` (oculto) | Remove e impede a volta | `filters.people` (PN+LID) |
| `g-fechar` / `g-abrir` | Só admins enviam / todos enviam (WhatsApp) | — |
| `g-notificar-grupoFechado` / `g-notificar-grupoAberto` | Alterna o aviso | `notificaGrupoFechado/Aberto` |
| `g-setPersonalidade <texto>` | Até 1500 caracteres | `customAIPrompt` |
| `g-setApelido @p nome` | Até 20 caracteres | `nicks` |
| `g-{twitch,kick,youtube}-canal <canal>` | Adiciona/remove (valida se existe) | `twitch/kick/youtube[]` |
| `g-…-mudarTitulo` | Alterna a troca de título | `changeTitleOnEvent` |
| `g-…-titulo on|off <canal> <título>` | Título online/offline | `onlineTitle/offlineTitle` |
| `g-…-fotoGrupo on|off <canal>` (responder imagem) | Foto do grupo | `groupPhotoOnline/Offline` |
| `g-…-midia on|off <canal>` (responder) | Adiciona mídia à notificação | `onConfig/offConfig.media` |
| `g-…-midia-del on|off <canal> <i>` | Remove mídia | idem |
| `g-…-usarIA` | Alterna a mensagem por LLM | `useAI` |
| `g-…-usarThumbnail` | Alterna a thumbnail | `useThumbnail` |
| `g-…-marcar` | Alterna menção a todos | `mentionAllMembers` |
| `g-variaveis` | Lista as variáveis (§3.5) | — |
| `g-painel [nome]` | Link do painel web | `web_management.tokens` |
| `g-setWebhook` (responder JSON) / `g-delWebhook <nome>` | Até 10 | `webhooks` |
| `g-advertir @p` / `g-advertencias` / `g-limpar-advertencias @p` | Contador | `warnings` |
| `g-streamRefresh` | Recalcula qual bot notifica | `botNotInGroup`/streams |
| `g-dossie` | Histórico de dossiês do grupo | summaries |
| `g-copiarCmds <origem> <destino>` | Copia comandos personalizados entre grupos (admin nos dois) | custom_commands |

**(bug)** `youtube-mudarTitulo` e `youtube-usarIA` apontam para métodos inexistentes (`toggleYoutubeTitleChange`, `toggleYoutubeAI`). O comando cai em "desconhecido".

## 15. Comandos `sa-` (SuperAdmin.js:27)

| Comando | O que faz |
|---|---|
| `sa-joinGrupo <code> [autorId] [nome]` | Entra pelo convite (respeita `blocked_invites`; repetir força a entrada) e grava `pending_joins` com quem convidou |
| `sa-joinGrupoSilencioso <code> …` | Igual, sem boas-vindas (`bot.silentJoinGroups`) |
| `sa-modoSilencioso` | Join silencioso global por 30 min |
| `sa-leaveGrupo <grupo> [b]` | Sai do grupo, opcionalmente bloqueando os membros |
| `sa-block <num>` / `sa-unblock` | Bloqueia o contato no WhatsApp e em `local_blocks` |
| `sa-blockList` / `sa-unblockList <n1,n2>` | Em lote |
| `sa-blockTudoList <lista>` / `sa-blockTudoPessoa <num>` | Sai de todos os grupos em comum e bloqueia todos os membros |
| `sa-listaGruposPessoa <num>` | Grupos em comum |
| `sa-blockInvites <num> [code]` / `sa-unblockInvites` | `soft_blocks.block_invites` + `blocked_invites` |
| `sa-addDonate <nome> <valor>` / `sa-addDonateNumero` / `sa-addDonateValor` / `sa-mergeDonates` | Gerencia `donations` |
| `sa-sendMsg <chatId> <texto>` / `sa-testeMsg` | Envia mensagem / teste |
| `sa-retrospectiva` | Mensagem de fim de ano |
| `sa-privacidade` | Padrões de privacidade da conta |
| `sa-foto` (responder imagem) / `sa-status <texto>` | Foto de perfil / recado do bot |
| `sa-simular <plataforma> <canal> on|off` | Simula evento de stream |
| `sa-restart` | Reinicia o bot |
| `sa-stats` / `sa-iaStats` | Grupos e uso / uso de LLM, Comfy e fala |
| `sa-getGroupInfo <nome>` / `sa-getMembros <nome>` | Dump do grupo / membros separados em admins e comuns |
| `sa-reagir <emoji>` | Debug |
| `sa-wol <mac>` | Wake-on-LAN |
| `sa-globalStreamRefresh` | `streamRefresh` em todos os grupos |
| `sa-dossie <grupo>` | Força a análise de dossiê |
| `sa-addElogio` / `sa-addXingamento` (responder figurinha) | Salva em `data/sillies/…` |
| `sa-fixGroupNames` | Sugere correções de nome (dry run) |
| `sa-dumpdbs` | Checkpoint WAL + integridade dos bancos |
| `sa-removeUserGp <gid> <num>` / `sa-addUserGp <gid> <num>` | Remove / adiciona participante |

**(bug)** `sa-unblockList` aponta para `fixGroupNames` (SuperAdmin.js:~103).
