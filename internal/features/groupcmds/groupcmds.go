// Package groupcmds implementa os comandos de grupo: chamar todo mundo
// (!atencao/!galera), sair dessas chamadas (!ignorar) e apagar mensagens do
// bot (!apagar).
package groupcmds

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/app"
	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Mensagens mostradas ao usuário.
const (
	MsgHeader      = "🚨 Atenção pessoal! 🚨"
	MsgNoMembers   = "Nenhum membro para mencionar."
	MsgIgnored     = "Você agora será *ignorado* nas menções de grupo."
	MsgIncluded    = "Você agora será *incluído* nas menções de grupo."
	MsgNoTarget    = "Responda à mensagem que deseja apagar."
	MsgNoPerm      = "🗑 Você não tem permissão para apagar esta mensagem. Você precisa ser admin, ou a mensagem deve ser sua, ou deve ser uma mensagem do bot que menciona você."
	MsgDeleteAdmin = "Não foi possível apagar a mensagem. Verifique se o bot é admin do grupo (necessário para apagar mensagens de terceiros)."
	MsgNotAdmin    = "Só admins do grupo podem usar esse comando."
)

// Module adapta a funcionalidade ao contrato app.Feature.
type Module struct{}

// Name implementa app.Feature.
func (Module) Name() string { return "groupcmds" }

// Register implementa app.Feature.
func (Module) Register(ctx context.Context, d *app.Deps) error {
	f, err := New(ctx, d.Store)
	if err != nil {
		return err
	}
	return d.Router.Register(f.Commands()...)
}

// Feature guarda o acesso ao banco.
type Feature struct{ db *sql.DB }

var migrations = []string{
	`CREATE TABLE groupcmds_ignored (
		chat TEXT NOT NULL,
		user TEXT NOT NULL,
		PRIMARY KEY (chat, user)
	)`,
}

// New aplica as migrações e cria a funcionalidade.
func New(ctx context.Context, st *store.Store) (*Feature, error) {
	if err := st.Migrate(ctx, "groupcmds", migrations); err != nil {
		return nil, err
	}
	return &Feature{db: st.DB()}, nil
}

const attentionCooldown = 5 * time.Minute

// Commands devolve os comandos para registrar no Router.
func (f *Feature) Commands() []*bot.Command {
	return []*bot.Command{
		{
			Name:        "atencao",
			Aliases:     []string{"atenção", "atençao", "galera"},
			Description: "Menciona todos os membros do grupo",
			Usage:       "!atencao [mensagem] (ou responda a uma mensagem)",
			Category:    "grupo",
			AdminOnly:   true,
			GroupOnly:   true,
			Cooldown:    attentionCooldown,
			Reactions:   bot.Reactions{Before: "📢", After: "✅"},
			Run:         f.attention,
		},
		{
			// O roteador checa AdminOnly no autor da mensagem reagida, não em
			// quem reagiu; por isso o gatilho 📢 é um comando à parte que
			// confere o admin de quem reagiu.
			Name:        "atencao-reacao",
			Description: "Menciona todos ao reagir 📢 a uma mensagem",
			Category:    "grupo",
			GroupOnly:   true,
			Hidden:      true,
			Cooldown:    attentionCooldown,
			Triggers:    []string{"📢"},
			Reactions:   bot.Reactions{After: "✅"},
			Run: func(ctx context.Context, c *bot.Context) error {
				if c.Reaction == nil {
					if !c.IsAdmin(ctx) {
						return bot.Userf(MsgNotAdmin)
					}
				} else if admin, err := reactorIsAdmin(ctx, c); err != nil {
					return err
				} else if !admin {
					return bot.ErrSilent
				}
				return f.attention(ctx, c)
			},
		},
		{
			Name:        "ignorar",
			Description: "Alterna ser ignorado pelas menções de grupo",
			Category:    "grupo",
			GroupOnly:   true,
			Reactions:   bot.Reactions{Before: "🔇", After: "✅"},
			Run:         f.toggleIgnore,
		},
		{
			Name:        "apagar",
			Description: "Apaga a mensagem do bot quando usado em resposta a ela",
			Category:    "grupo",
			Triggers:    []string{"🗑️", "🗑"},
			Reactions:   bot.Reactions{Error: "❌"},
			Run:         f.apagar,
		},
	}
}

// identities devolve as identidades não vazias.
func identities(js ...types.JID) []types.JID {
	var out []types.JID
	for _, j := range js {
		if j.User != "" {
			out = append(out, j)
		}
	}
	return out
}

func reactorIsAdmin(ctx context.Context, c *bot.Context) (bool, error) {
	info, err := c.Client.GroupInfo(ctx, c.Message.Chat)
	if err != nil {
		return false, fmt.Errorf("consultando grupo: %w", err)
	}
	for _, j := range identities(c.Reaction.By, c.Reaction.ByAlt) {
		if info.IsAdmin(j) {
			return true, nil
		}
	}
	return false, nil
}

func (f *Feature) ignored(ctx context.Context, chat types.JID) (map[string]bool, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT user FROM groupcmds_ignored WHERE chat = ?`, chat.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[u] = true
	}
	return out, rows.Err()
}

func (f *Feature) attention(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat
	info, err := c.Client.GroupInfo(ctx, chat)
	if err != nil {
		return fmt.Errorf("consultando grupo: %w", err)
	}
	ignored, err := f.ignored(ctx, chat)
	if err != nil {
		return err
	}
	me := c.Client.Me()
	var mentions []types.JID
	for _, p := range info.Participants {
		if p.Is(me) || ignored[p.JID.User] || ignored[p.LID.User] || ignored[p.Phone.User] {
			continue
		}
		mentions = append(mentions, p.JID)
	}
	if len(mentions) == 0 {
		return bot.Userf(MsgNoMembers)
	}

	// Conteúdo: a mensagem reagida, a citada ou o texto após o comando.
	var body string
	var media *bot.Media
	switch {
	case c.Reaction != nil:
		body, media = c.Message.Text, c.Message.Media
	case c.Message.Quoted != nil:
		body, media = c.Message.Quoted.Text, c.Message.Quoted.Media
	default:
		body = c.RawArgs()
	}
	text := MsgHeader
	if body != "" {
		text += "\n\n" + body
	}

	out := bot.Text(text)
	if media != nil {
		var kind bot.OutKind
		switch media.Kind {
		case bot.MediaImage:
			kind = bot.OutImage
		case bot.MediaVideo, bot.MediaGIF:
			kind = bot.OutVideo
		}
		if kind != bot.OutText {
			data, err := media.Download(ctx)
			if err != nil {
				return fmt.Errorf("baixando mídia: %w", err)
			}
			out = bot.Outgoing{Kind: kind, Data: data, Mime: media.Mime, Caption: text}
		}
	}
	// Menções "escondidas": todos são notificados sem poluir o texto.
	out.Mentions = mentions
	return c.Client.Send(ctx, chat, out)
}

func (f *Feature) toggleIgnore(ctx context.Context, c *bot.Context) error {
	chat := c.Message.Chat.String()
	users := identities(c.Message.Sender, c.Message.SenderAlt)
	if len(users) == 0 {
		return fmt.Errorf("mensagem sem autor")
	}
	// Junta todas as identidades conhecidas (número e LID) do membro, para
	// que desfazer funcione mesmo vindo pela outra identidade.
	if info, err := c.Client.GroupInfo(ctx, c.Message.Chat); err == nil {
		if p, ok := info.Member(users[0]); ok {
			for _, j := range identities(p.JID, p.LID, p.Phone) {
				if !slices.ContainsFunc(users, func(u types.JID) bool { return u.User == j.User }) {
					users = append(users, j)
				}
			}
		}
	}
	ignored, err := f.ignored(ctx, c.Message.Chat)
	if err != nil {
		return err
	}
	already := false
	for _, u := range users {
		already = already || ignored[u.User]
	}
	for _, u := range users {
		if already {
			_, err = f.db.ExecContext(ctx, `DELETE FROM groupcmds_ignored WHERE chat = ? AND user = ?`, chat, u.User)
		} else {
			_, err = f.db.ExecContext(ctx, `INSERT OR IGNORE INTO groupcmds_ignored(chat, user) VALUES(?, ?)`, chat, u.User)
		}
		if err != nil {
			return err
		}
	}
	if already {
		return c.ReplyText(ctx, MsgIncluded)
	}
	return c.ReplyText(ctx, MsgIgnored)
}

// sameUser diz se alguma identidade de a é a mesma pessoa que alguma de b,
// usando os participantes do grupo para casar número com LID.
func sameUser(info *bot.GroupInfo, a, b []types.JID) bool {
	for _, x := range a {
		for _, y := range b {
			if x.User == y.User {
				return true
			}
			if info != nil {
				if p, ok := info.Member(x); ok && p.Is(y) {
					return true
				}
			}
		}
	}
	return false
}

func (f *Feature) apagar(ctx context.Context, c *bot.Context) error {
	msg := c.Message
	target, requester := msg.Quoted, identities(msg.Sender, msg.SenderAlt)
	if c.Reaction != nil {
		target, requester = msg, identities(c.Reaction.By, c.Reaction.ByAlt)
	}
	if target == nil {
		return bot.Userf(MsgNoTarget)
	}

	var info *bot.GroupInfo
	if msg.IsGroup {
		var err error
		if info, err = c.Client.GroupInfo(ctx, msg.Chat); err != nil {
			return fmt.Errorf("consultando grupo: %w", err)
		}
	}
	owner := identities(target.Sender, target.SenderAlt)
	isBotMsg := target.FromMe || sameUser(info, []types.JID{c.Client.Me()}, owner)

	allowed := false
	switch {
	case msg.IsGroup && c.Reaction == nil:
		allowed = c.IsAdmin(ctx)
	case msg.IsGroup:
		for _, j := range requester {
			allowed = allowed || info.IsAdmin(j)
		}
	default:
		allowed = isBotMsg
	}
	allowed = allowed || sameUser(info, requester, owner) ||
		(isBotMsg && sameUser(info, requester, target.Mentions))
	if !allowed {
		return bot.Userf(MsgNoPerm)
	}

	// Mensagens citadas não trazem o chat; o alvo é sempre deste chat.
	del := *target
	del.Chat = msg.Chat
	if err := c.Client.Delete(ctx, &del); err != nil {
		if !isBotMsg && msg.IsGroup {
			return bot.Userf(MsgDeleteAdmin)
		}
		return fmt.Errorf("apagando mensagem: %w", err)
	}
	if c.Reaction == nil {
		_ = c.Client.React(ctx, msg, "✅") // melhor esforço
	}
	return nil
}
