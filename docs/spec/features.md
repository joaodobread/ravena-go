# Funcionalidades do Ravena (Node) — mapa para o port

Referência: `RavenBot/src/functions/*.js`, carregados por `src/commands/FixedCommands.js`. Caminhos abaixo são relativos a `RavenBot/src/` salvo indicação.

## Como os módulos são carregados (importa para o port)

- `FixedCommands.loadCommands` faz `readdir` de `functions/` e junta `module.exports.commands` de cada arquivo (`commands/FixedCommands.js:30-65`). Objetos simples ganham reações padrão `before: LOADING_EMOJI ("⌛️")`, `after: "✅"` (`:53-58`).
- **Aliases não funcionam**: `getCommand` compara só `cmd.name` (`commands/FixedCommands.js:89-97`; `CommandHandler.js:816`). Por isso os módulos repetem o comando com outro `name` (ex.: `s`, `fig`, `figurinha`). Os campos `aliases: [...]` (anagram, a-rank, sbg, google/search, img/imagem, instasipt, psn/playstation, rifa/acao, popular/top) são letra morta. No Go, basta usar `Aliases`.
- **Nome duplicado → vence o primeiro arquivo em ordem alfabética** (`find`): `yt`/`sr`/`sc` → `SocialMediaDownloader` (cobalt), não `YoutubeDownloader`/`SoundCloud`; `wildrift`/`wr` → `RiotGamesCommands` (perfil), não `WildRiftBuild`; `clima` → só `WeatherMeteo` (o `Weather` não exporta nada); `imagine` → só `BonsaiCommands` exporta.
- **Módulos que não exportam comandos** (export comentado): `ComfyUICommands.js:703`, `DataShare.js:241`, `GeoguesserGame.js:1382`, `SoundCloud.js:137`, `StableDiffusionCommands.js:393`, `Weather.js:446`. `MenuOrder.js` é só um helper de ordem de categorias para o `Menu`.
- **Exportação condicional**: `CorreiosCommands.js:13-19` exporta `[]` sem `CORREIOS_API`; `Copa2026.js:1527` exporta `[]` sem `WDC2026_API_URL`.
- **Gatilho por reação**: `reactions.trigger` faz o comando rodar sobre a mensagem reagida (equivale a `bot.Command.Triggers` no Go). Lista: 🖼 sticker, 🤖 ai, 🔍 ocr (e buscar), 🦜 resumo, 🍽️ comida, ⬇️/⏬ download/download-audio, 👂 stt, 🗣️🦇👩💋👨🎙️ tts, 🎲 sorteio, 📢 atencao/galera, 🗑️ apagar, bandeiras → traduzir, números → listas, ✨ imagine.
- **Ganchos passivos** (fora de comandos, chamados pelo `EventHandler`): `RankingMessages.processMessage/processReaction` em toda msg/reação de grupo (`EventHandler.js:226,476`); `SummaryCommands.storeMessage` guarda msgs para resumo/interagir (`:345,500,582`); `SpeechCommands.processAutoSTT` (`:574`); `Stickers.processAutoSticker` no PV (`:615`); auto-download de links no PV se `bot.autoDownloadPV` (`:599-610`); `MuNews.detectNews`, `Horoscopo.detectHoroscopo`, `Copa2026.detectCopaGif` em mensagens de **canais (newsletter)** (`:278-302`); `Lembretes.inicializarLembretes` e `Correios.inicializarRastreio` no boot (`:186-191`).

## Ambiente no Railway

`.railway/railway.ts` só define `OPENROUTER_API_KEY` como chave de API (LLM multimodal `google/gemini-3.1-flash-lite`, único provider em `SERVICE_PROVIDERS` — **sem providers `whisper`, `f5tts`, `bonsai`, `sdwebui`**), sidecars `rembg` e `cobalt`, `DISABLE_IMAGINE_COMMAND=true`, `DISABLE_TTS_COMMAND=true`, `WDC2026_API_URL`. Do `.env.example` **faltam**: `GIPHY_API_KEY`, `OMDB_API_KEY`, `LASTFM_APIKEY/SECRET`, `UNSPLASH_API_KEY`, `RIOT_GAMES`, `NASA_API` (cai em `DEMO_KEY`), `GOOGLE_MAPS_API_KEY`, `OPENWEATHER_API_KEY`, `TWITCH_*`, `KICK_*`, `SOUNDCLOUD_*`, `CORREIOS_API`, `API_PLACAS_*`, `FIPE_API_TOKEN`, `SIPT_*`, `PSNCOMMAND_API_URL`, `STEAMCOMMAND_API_URL`, `ANYTHINGLLM_*`, `RAGNAROK_*`, `GRUPO_*` (logs/avisos/etc.), `DONATION_*`, links de Discord/Telegram. Dockerfile instala `ffmpeg`, `imagemagick`, `python3` (`Dockerfile:18-25`); yt-dlp vem do pacote npm `youtube-dl-exec`.

Legenda status: ✅ funciona · ⚠️ parcial/degradado · ❌ inutilizável · 🚫 não carregado em lugar nenhum.
Complexidade: S (< ~1 dia), M (alguns dias), L (semana+ / muito estado).

## Tabela de módulos

| Módulo | Comandos | Comportamento | Dependências externas | Persistência | Cx | Railway |
|---|---|---|---|---|---|---|
| Stickers | `sticker` `figurinha` `s` `fig` (🖼), `sq`/`stickerq`, `sqc`/`stickerqc`, `sqb`/`stickerqb`, `sqe`/`stickerqe`, `sqi`/`stickerqi` | Mídia → figurinha (contain/corte/esticar/IA); auto-figurinha no PV | ffmpeg, sharp; LLM visão para `sqi` (`Stickers.js:124`) | — | M | ✅ **já portado** em `ravena-go/internal/features/sticker` |
| ImageManipulation | `removebg`, `rbg`, `stickerbg`, `sbg`, `distort`, `morejpeg`, `sketch` `oil` `neon` `pixelate` (`:792`) | Remove fundo (PNG ou figurinha), efeitos ImageMagick | sidecar **rembg** `REMBG_API_URL` (`/api/remove`, `:91`), ImageMagick (`imagemagick` npm → `convert`), sharp | — | M | ✅ |
| EmojiKitchenCommands | `emojik`, `memoji` | Combina 2 emojis em figurinha | `https://emojik.vercel.app` | cache `data/media/emojikitchen` | S | ✅ |
| FileConversions | `extractaudio`, `extractvoice`, `volume` | Extrai áudio (mp3/voz ogg) de mídia; ajusta volume 0–1000 | ffmpeg (fluent-ffmpeg) | — | S | ✅ |
| SocialMediaDownloader | `download` (⬇️), `download-audio` (⏬), `download-musica`, `yt`, `yt-audio`, `yt-musica`, `sr`, `x`, `twitter`, `tiktok`, `tk`, `tiktok-audio`, `insta`, `instagram`, `insta-audio`, `fb`, `facebook`, `pin`, `pinterest`, `sc*`, `soundcloud*`, … (`:909-961`) | Baixa mídia de ~30 plataformas (`:184`) via cobalt; auto-download no PV | sidecar **cobalt** `COBALT_API_URL` (fallback `api.cobalt.tools`), `lrclib.net`, `youtube-search-api`; `DISABLE_DOWNLOADS` | `smd_cache` (url→arquivos), arquivos em `DL_FOLDER` | L | ✅ (YouTube via cobalt costuma falhar) |
| YoutubeDownloader | `yt`, `sr` (sombreados pelo SMD), `letra` | Baixa vídeo/música do YouTube com yt-dlp; letra de música | **yt-dlp** (`youtube-dl-exec`), ffmpeg, `lrclib.net`, cookies `data/www.youtube.com_cookies.txt` (`YT_USE_COOKIES`) | `VideoCacheManager` em `data/` | L | ⚠️ só `letra` é alcançável |
| SoundCloud | (`sc`) | Busca/baixa do SoundCloud | `soundcloud.ts`, `SOUNDCLOUD_CLIENT_ID/OAUTH_TOKEN` | — | S | 🚫 |
| GroupCommands | `atencao`/`atenção`/`atençao`, `galera` (📢, adminOnly, cd 300s), `ignorar`, `apagar` (🗑️) | Menciona todos (exceto quem pediu `ignorar`); apaga msg do bot respondida | — | `group.ignoredUsers` (via `saveGroup`, `:156-218`) | S | ✅ |
| GeneralCommands | `ping`, `status`, `apelido`, `grupao`/`comunidade`, `avisos`, `codigo`/`código`, `private`, `retrospectiva`, `comunitaria`, `convite`, `discord`, `telegram` | Infos estáticas do bot; `apelido` define nick no grupo; `status` consulta a própria API | `http://localhost:${API_PORT}`; textos `data/textos/*.txt`; `DISCORD_*`/`TELEGRAM_*` | `group.nicks` | S | ⚠️ links de comunidade vazios |
| Menu (+MenuOrder) | `cmd`/`comandos`/`menu`/`info`, `cmd-gerenciamento`/`cmd-g`, `cmd-grupo`, `cmd-categoria`, `cmd-<categoria>` | Lista comandos por categoria (fixos + personalizados do grupo), ordem de `MenuOrder` | `data/textos/cmd_header.txt` | lê custom commands | M | ✅ |
| DiceCommands | `d4` `d6` `d8` `d10` `d12` `d20` `d100`, `roll` (ex.: `2d6+3`) | Rola dados | — | — | S | ✅ |
| ZueiraCommands | `violencia`/`violência`, `morreu`, `boleto`, `clonarcartao`, `presente`, `pix`, `aniversario`/`aniversário`, `pecar`, `meus-pecados`, `genshin` | Zoeiras com membro marcado/aleatório: frase sorteada de uma lista em `custom-variables.json` (`handleComandoVariavelSimples`, `:269`; `pecar`/`meus-pecados` usam a lista `pecados`); `pix` gera imagem "Ravenabank" | canvas, `data/ravenabank.png`, `data/custom-variables.json` | — | S | ✅ |
| BiscoitoDaSorte | `biscoito` (`biscoito-da-sorte`, `biscoito-sorte`) | Frase aleatória | `data/custom-variables.json` | — | S | ✅ |
| CantadaCommand | `cantada` | Cantada aleatória para alguém | `data/custom-variables.json` | — | S | ✅ |
| PintoGame | `pinto`, `pinto-ranking`, `pinto-reset` (admin) | Tamanho aleatório com cooldown interno; ranking por grupo | — | `pinto_scores`, `pinto_history` | S | ✅ |
| RoletaRussaCommands | `roletarussa`, `roleta-ranking`/`roletaranking`, `roleta-reset` (admin), `roleta-tempo` (admin) | Chance de "morrer"; morto fica `timeout_time` s (padrão 300) sem poder jogar (`:191-204`) | — | `roleta_groups`, `roleta_players`, `roleta_history` | S | ✅ |
| SlotsGame | `slots`, `slots-premios`, `slots-ranking` | Caça-níquel com moedas e prêmios; `addCoins` usado pela pesca | — | `slots_users`, `slots_prizes`, `slots_group_stats` | M | ✅ |
| FishingGame | `pescar`/`pesca`, `meus-pescados`/`pesca-ficha`/`pesca-dados`, `pesca-ranking`/`pescados`, `pesca-info`, `pesca-lendas`, `pesca-peixes`, `pesca-iscas`, `pesca-reset` (admin), `psc-addBaits/addBuff/addDebuff/addFish`, `pesca-global` (admin, ocultos) | Jogo de pesca: iscas, peso, inventário, buffs/debuffs, lendários com imagem gerada (canvas/IA) | canvas + `data/fonts/FishingFont.ttf`, `data/rare-fish.jpg`, `data/media/*`; geração de imagem via `BonsaiCommands.generateImage` (`:1409`) — falha → sem imagem | `fishing_users`, `fishing_inventory`, `fishing_group_stats`, `fishing_legendary_history`, `fishing_buffs`, `*_history` | L | ✅ (lendário sem imagem nova) |
| AnagramGame | `anagrama` (cd 20), `ana`, `ana-dica`, `ana-pular`, `anagrama-ranking`, `anagrama-reset` (admin) | Anagrama por rodadas com imagem; dica via LLM (`:577`) | canvas; `data/anagrama/{letters,words}`; LLM | `anagram_groups`, `anagram_scores` | M | ✅ |
| LogicSequenceGame | `sequencia`, `seq`, `sequencia-ranking` | Adivinhar próximo termo de sequência; envia msgs com timer | — | `logic_scores` | M | ✅ |
| StopGame | `adedonha`, `stop` | Stop por rodada com tempo; LLM corrige respostas (`:366`); menciona jogadores | LLM | memória | M | ✅ |
| TarotGame | `tarot` | 3 cartas + interpretação por LLM; tenta imagem (cai para texto, `:329`) | LLM; Bonsai (opcional) | `tarot_users`, `tarot_history` | M | ✅ (sem imagem) |
| RelacionamentoCommands | `namorar`, `casar`, `separar`, `recusar`, `coisar`, `trair`, `relacionamento`, `relacionamentos` | Pedidos/aceites entre membros (marcado ou aleatório), histórico e estatísticas | — | `relacionamentos` | L | ✅ |
| SorteioCommands | `sorteio`, `sorteio-entrar`, `sorteio-sair`, `sortear`, `sorteios`, `reactionSorteioHelper` (🎲) | Sorteio no grupo com inscrição por comando ou 🎲; sorteia membro aleatório | participantes do grupo, menções | `sorteios`, `sorteio_participants` | M | ✅ |
| ListCommands | `listas`/`ll`, `lc`, `lct`, `ld`, `le`, `ls`, `lt`, `lr` (admin), `reactionListHelper` (emojis numéricos) | Listas de presença por grupo; entrar/sair também por reação numérica | participantes, admins | `lists` | M | ✅ |
| RankingMessages | `faladores`, `faladores-limpeza` (admin), `faladores-reset` (admin) | Conta msgs/reações por membro (gancho passivo) e mostra ranking | participantes | `ranking` | M | ✅ |
| AnonymousMessage | `anonimo`/`anônimo`, `adminanon` (oculto) | No PV, envia msg anônima a um grupo (LLM reescreve o texto, `:213`); cooldown interno | LLM; envio para outro chat | `anon_msgs` | M | ✅ |
| LembretesCommands | `lembrar`, `lembretes`, `l-cancelar` | Lembretes com data em linguagem natural, com mídia; reagendados no boot | `chrono-node` (pt) | `lembretes`; mídia em `data/lembretes-media` | M | ✅ |
| AICommands | `ai` `ia` `gpt` `gemini` (🤖) | Chat LLM com contexto do bot; analisa imagem/vídeo (frames, `:515`); classifica se a pergunta é um comando e o executa (`:120-140`) | LLM (`LLMService`, provider OpenRouter); ffmpeg p/ frames; `data/textos/llm_context*.txt`, `bot-greeting.txt` | — | M | ✅ |
| SummaryCommands | `resumo` (🦜), `interagir` | Resume conversa recente; gera interação "espontânea" (usada pela auto-interação do core, `CommandHandler.js:1901`); dossiê do grupo | LLM | `chat_conversations`, `conversations`, `group_dossiers`, `group_dossier_status` | M | ✅ |
| OcrCommands | `ocr` (🔍) | Extrai texto de imagem via LLM visão | LLM; `data/textos/llm_context_images.txt` | — | S | ✅ |
| FoodTracker | `comida` (🍽️), `comida-info`, `comida-lista` | Foto de comida → ingredientes/calorias por LLM (JSON schema) e histórico | LLM visão | `food_entries`, `food_ingredients` | M | ✅ |
| TranslationCommands | `traduzir`, `translationReactionHelper` (reação com bandeira) | Traduz texto (ou msg reagida pela bandeira do idioma) | npm `translate` engine google sem chave (`:299-309`) | — | S | ✅ |
| SpeechCommands | `stt`/`transcrever` (👂), `tts` (🗣️🦇), `tts-mulher` `tts-carioca` `tts-carioco` `tts-sensual` `tts-sensuel` `tts-homem` `tts-clint` `tts-morgan` `tts-narrador` `tts-rubao` | STT (Whisper) + otimização por LLM; TTS com vozes (F5-TTS, referência `data/ravena_sample.*`); auto-STT do core | providers `whisper` e `f5tts` de `service-providers.json` (fallback `http://localhost:5050`, `:285`); ffmpeg | `speech_transcription_stats`, `speech_generation_stats` | L | ❌ TTS desligado (`DISABLE_TTS_COMMAND`, `:223`); STT sem provider whisper |
| BonsaiCommands | `imagine` (✨) | Gera imagem: LLM refina prompt + checagem NSFW, marca d'água | servidor Bonsai/SD (provider ou `http://192.168.195.212:13434`, `:54`); sharp; LLM; `data/sd_watermark.png` | `bonsai_stats` | M | ❌ `DISABLE_IMAGINE_COMMAND` (`:443`) |
| ComfyUICommands | (`imagine`) | Igual, via ComfyUI (websocket, modelo z_image_turbo) | ComfyUI `http://127.0.0.1:8188` | `comfy_stats` | M | 🚫 |
| StableDiffusionCommands | (`imagine`) | Igual, via SD WebUI | `http://localhost:7860`, `SDWEBUI_TOKEN`, `SD_*` | — | M | 🚫 |
| AnythingLLMHelper | `ajuda` | Pergunta à base de conhecimento | AnythingLLM `ANYTHINGLLM_HOST/API_KEY/WORKSPACE` | — | S | ❌ |
| SearchCommands | `buscar`, `buscar-img`, `rab` | Busca web (DDG instant answer), imagens (Unsplash), registro aeronáutico ANAC (scraping) | `api.duckduckgo.com`, `api.unsplash.com` (`UNSPLASH_API_KEY`), `sistemas.anac.gov.br`; cheerio | — | S | ⚠️ `buscar-img` sem chave |
| WikipediaCommands | `wiki` | Resumo de artigo | `pt.wikipedia.org` | — | S | ✅ |
| WeatherMeteo | `clima` | Clima/previsão | `geocoding-api.open-meteo.com`, `api.open-meteo.com` (sem chave) | — | S | ✅ |
| Weather | (`clima`) | Clima via OpenWeather | `api.openweathermap.org`, `OPENWEATHER_API_KEY` | — | S | 🚫 |
| MetarStatistics | `metar` | METAR de aeroporto | `aviationweather.gov` | `data/metar/` | S | ✅ |
| NASA | `apod`, `epic`, `epic-enhanced`, `epic-aerosol`, `epic-cloud` | Foto do dia / imagens da Terra | `api.nasa.gov` (`NASA_API`, padrão `DEMO_KEY`, `:10`), `epic.gsfc.nasa.gov` | — | S | ⚠️ DEMO_KEY (rate limit) |
| GiphyCommands | `gif` | Busca GIF e envia como vídeo | `api.giphy.com` (`GIPHY_API_KEY`), ffmpeg | — | S | ❌ |
| ImdbCommands | `imdb` | Filme/série | `www.omdbapi.com` (`OMDB_API_KEY`) | — | S | ❌ |
| AnimeCommands | `anime` | Info do MyAnimeList | npm `mal-scraper` (scraping) | — | S | ✅ |
| LastFMCommands | `lastfm`, `lfm` | Perfil Last.fm | npm `lastfm` (`LASTFM_APIKEY/SECRET`) | — | S | ❌ |
| MyInstantsAudioSearch | `audio`/`áudio` | Busca e envia áudio do MyInstants | `www.myinstants.com` (cheerio) | — | S | ✅ |
| GamingFreebies | `0800`, `giveaways` | Jogos grátis atuais | `www.gamerpower.com/api` | — | S | ✅ |
| LolBuild | `lol-build` | Link de build de campeão | `op.gg` (só monta link) | — | S | ✅ |
| WildRiftBuild | `wr-build` (`wildrift`/`wr` sombreados) | Link de build Wild Rift | `www.wildriftfire.com` | — | S | ✅ |
| RiotGamesCommands | `lol`, `valorant`/`valo`, `wildrift`/`wr` | Perfil de jogador | `*.api.riotgames.com` (`RIOT_GAMES`), `ddragon.leagueoflegends.com`, `vaccie.pythonanywhere.com`, `www.reng.ar` | — | M | ❌ |
| RobloxCommands | `roblox`, `rbx` | Perfil + avatar Roblox | `users/games/presence/thumbnails.roblox.com` | `roblox_cache` | S | ✅ |
| PsnCommands | `psn-platinas` | Platinas PSN | API própria `PSNCOMMAND_API_URL` + `API_KEY_STEAMCOMMAND` | — | S | ❌ |
| SteamCommands | `steam-platinas` | Platinas Steam | API própria `STEAMCOMMAND_API_URL` | — | S | ❌ |
| Copa2026 | `copa`, `copa-times`, `copa-time`, `copa-grupos`, `copa-grupo`, `copa-jogos`, `copa-jogo`, `copa-estadios`, `copa-seguir` | Dados da Copa 2026; quem segue time recebe GIF do placar repassado de um canal (`detectCopaGif`, `:1283`) | `WDC2026_API_URL` (`/get/teams`, `/get/groups`…) | `copa_seguindo` | M | ✅ |
| HoroscopoCommands | `horoscopo`/`horóscopo`/`signo` | Mostra horóscopo salvo de canais do WhatsApp (gancho de newsletter) | `chrono-node`; depende do bot seguir os canais | `horoscopo` | S | ⚠️ só se o número seguir os canais |
| MuNewsCommands | `news` | MuNews do dia, capturada de canal (newsletter) | `chrono-node`; `GRUPO_LOGS` para confirmação | `munews` | S | ⚠️ idem |
| CorreiosCommands | `correios`, `correios-lista`, `correios-del` | Rastreio com polling (cron 15 min, `:93`) e aviso no chat | `CORREIOS_API`; `node-cron` | `tracks` | M | ❌ sem `CORREIOS_API` (exporta `[]`) |
| PlacasCommands | `placa` (oculto), `sipt`/`instasipt` | Consulta placa + FIPE | `wdapi2.com.br` (`API_PLACAS_COMUM/PREMIUM`), `fipe.parallelum.com.br` (`FIPE_API_TOKEN`), SIPT em LAN `192.168.3.200:1936` | `placas`, `fipe_cache` | M | ❌ |
| Raffles | `raffle` (cd 15) | Info de rifa/ação (scraping) | `www.narigapremios.com` (cheerio) | `raffle_cache` | S | ✅ (nicho) |
| QrCommands | `qr`, `qr-wifi`, `qr-pix` | Gera QR de texto, Wi-Fi e PIX (payload BR Code) | npm `qr-image`, canvas | — | S | ✅ |
| FileManager | `pastas`, `p-criar`, `p-enviar`, `p-excluir`, `p-baixar` | Pastas/arquivos por chat (100 MB/arquivo, 1 GB/grupo, `:33-36`); `{file-...}` em custom cmds | disco `media/<chatId>/files` (`:102`) | `files`, `chat_storage` | M | ✅ (volume) |
| DonationCommands | `doar`, `doadores`, `donate` | Link e lista de doadores | `DONATION_GOAL_*`; `data/textos/donate_*.txt` | `database.getDonations` (tabela de doações do core) | S | ⚠️ sem meta/link |
| StreamCommands | `streams`, `streamstatus`, `streamers`, `live`, `live-kick`, `topstreams` | Consulta/lista streams monitoradas (motor em `StreamSystem.js`/`services/StreamMonitor.js`) | Twitch Helix (`TWITCH_CLIENT_ID/SECRET`), Kick `api.kick.com/public/v1` (`KICK_*`), YouTube por scraping/RSS (`StreamMonitor.js:1504,1579`) | `group.twitch/kick/youtube` | M | ⚠️ só YouTube |
| RagnarokCommands | `ragnavena`, `rag-info`, `ragnavena-reset` | Cria/reseta conta no servidor de Ragnarok da ravena | MySQL `RAGNAROK_*` (`mysql2`) | MySQL externo | S | ❌ |
| GeoguesserGame | (`geoguesser`, `geoguess`, `geostatus`, `geohistory`, `geo-ranking`, `geotest`) | Adivinhar local por Street View | Google Maps (`GOOGLE_MAPS_API_KEY`), sharp | `data/geoguesser.json` | L | 🚫 |
| DataShare | (`share`) | Exporta dados do grupo para a API | `BOT_DOMAIN` | `data/data-share/<grupo>.json` | S | 🚫 |

## Lotes de port (paralelizáveis)

Ordenados por valor. Cada lote tem ~1–1,5k linhas de Node em lógica útil. "Capacidades do core" = o que precisa existir em `internal/bot` antes (ou junto) do lote. Capacidades transversais que todo lote assume: `Reply` com texto/mídia, reações before/after/error, `Triggers` por emoji, `adminOnly`/cooldown do core, categorias para o menu.

| # | Lote | Módulos | Capacidades do core necessárias |
|---|---|---|---|
| 1 | **Jogos de grupo leves + zoeira** | DiceCommands, PintoGame, RoletaRussa, Zueira, Biscoito, Cantada, GroupCommands, RankingMessages | menções (`@` em texto + JIDs), participantes e admins do grupo, membro aleatório, envio sem citar, listener passivo de mensagens e reações (ranking), **storage por feature** (SQLite, tabelas próprias), dados de `data/custom-variables.json`, imagem gerada (canvas → Go `image`/fonte TTF) para `pix` |
| 2 | **Menu e utilidades gerais** | Menu(+MenuOrder), GeneralCommands, QrCommands, WikipediaCommands, WeatherMeteo, Metar, SearchCommands (buscar/rab), GamingFreebies, LolBuild, WildRiftBuild, Translation | registro/listagem de comandos com categoria e `hidden` (+ custom commands do grupo), `group.nicks`, **cliente HTTP** com timeout/retry, parse HTML (goquery), arquivos `data/textos/*`, reação por bandeira → idioma |
| 3 | **Mídia** (continua o sticker) | ImageManipulation, EmojiKitchen, FileConversions, AnimeCommands, MyInstantsAudioSearch, NASA | ffmpeg/ImageMagick já existentes em `internal/media`, cliente rembg (sidecar), **envio de áudio** (mp3 e voz/ogg-opus PTT), envio de imagem/vídeo/documento, download de mídia citada |
| 4 | **IA (OpenRouter)** | AICommands, SummaryCommands (`resumo`, `interagir`, dossiê), OcrCommands, FoodTracker, TarotGame, AnonymousMessage | **LLM chat** com system prompt, imagens (visão), saída JSON por schema; buffer das mensagens recentes por chat (listener passivo); frames de vídeo (ffmpeg); envio para **outro chat** que não o de origem; storage por feature; textos `data/textos/llm_*` |
| 5 | **Jogos com estado e rodadas** | FishingGame, SlotsGame (moedas compartilhadas com a pesca) | storage por feature com transações, imagem gerada (fontes `data/fonts`, `data/rare-fish.jpg`, `data/media`), menções, admins, API entre features (`addCoins`) |
| 6 | **Jogos temporizados e de grupo** | AnagramGame, LogicSequenceGame, StopGame, Relacionamento, Sorteio, ListCommands | **jobs agendados/timers** por chat (fim de rodada), estado de jogo em memória por grupo, menções, participantes, gatilhos por reação (🎲, números), LLM (dica/correção), `data/anagrama/*`, storage por feature |
| 7 | **Downloads e agendamentos** | SocialMediaDownloader (cobalt), YoutubeDownloader (`letra` + yt-dlp como fallback), Lembretes, Copa2026, FileManager, Raffles, Roblox | cliente cobalt (sidecar), yt-dlp binário, envio de arquivo grande/documento, **scheduler persistente** (lembretes sobrevivem a restart), parse de data em PT (equivalente ao chrono), listener de **canais/newsletter** (Copa), armazenamento em disco com quota por grupo, auto-download no PV |
| 8 | **Chaves de API / self-hosted / nicho (por último)** | SpeechCommands (Whisper/F5-TTS), BonsaiCommands/ComfyUI/SD (`imagine`), StreamCommands (+StreamSystem), Correios, Placas, Riot, Giphy, Imdb, LastFM, PSN, Steam, Horoscopo, MuNews, Donation, AnythingLLM, Ragnarok, Geoguesser | sidecars GPU (whisper/TTS/imagem), envio de **voz PTT**, **jobs periódicos** (polling de streams a cada N min, rastreio Correios), mensagens proativas em grupos, listener de newsletter, várias env vars de chave; vários podem ser descartados |

Sugestões:
- Lotes 1–4 dão o grosso do uso em grupos; 1, 2 e 3 não dependem entre si. O lote 4 precisa do buffer de mensagens, que também alimenta a auto-interação do core (`interagir`).
- No lote 8, só Speech (STT via OpenRouter/Whisper hospedado) e Streams (Twitch/Kick com chave grátis) parecem valer o esforço; os demais estão quebrados no deploy atual.
- Não portar: ComfyUI/SD (duplicam `imagine`), Weather (OpenWeather), SoundCloud, DataShare, Geoguesser — nenhum é carregado hoje.
