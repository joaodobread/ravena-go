package relationship_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/joaodobread/ravena-go/internal/bot"
	"github.com/joaodobread/ravena-go/internal/bot/bottest"
	"github.com/joaodobread/ravena-go/internal/features/relationship"
	"github.com/joaodobread/ravena-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var (
	ana   = bottest.User
	bia   = bottest.Admin
	caio  = types.NewJID("5511777777777", types.DefaultUserServer)
	caioL = types.NewJID("123456789012345", types.HiddenUserServer)
)

type env struct {
	r   *bot.Router
	fc  *bottest.FakeClient
	now time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx, "relationship", relationship.Migrations()); err != nil {
		t.Fatal(err)
	}
	e := &env{now: time.Unix(1_700_000_000, 0)}
	e.fc = &bottest.FakeClient{Groups: map[types.JID]*bot.GroupInfo{bottest.Group: {
		JID: bottest.Group,
		Participants: []bot.Participant{
			{JID: ana}, {JID: bia}, {JID: caioL, LID: caioL, Phone: caio}, {JID: bottest.Bot},
		},
	}}}
	f := relationship.New(st.DB(), func(int) int { return 0 }, func() time.Time { return e.now })
	e.r = bot.NewRouter("!", e.fc, quiet)
	if err := e.r.Register(f.Commands()...); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) as(t *testing.T, from types.JID, text string, mentions ...types.JID) bot.Outgoing {
	t.Helper()
	m := bottest.GroupMsg(text)
	m.Sender, m.Mentions = from, mentions
	e.r.Handle(context.Background(), m)
	if len(e.fc.Replies) == 0 {
		t.Fatal("nenhuma resposta")
	}
	return e.fc.Last().Out
}

func expect(t *testing.T, out bot.Outgoing, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out.Text, s) {
			t.Fatalf("resposta sem %q: %q", s, out.Text)
		}
	}
}

func at(j types.JID) string { return "@" + j.User }

func TestDatingFlow(t *testing.T) {
	e := setup(t)
	expect(t, e.as(t, ana, "!namorar"), "precisa marcar alguém")
	expect(t, e.as(t, ana, "!namorar", ana), "consigo mesmo")

	out := e.as(t, ana, "!namorar "+at(bia), bia)
	expect(t, out, "foi pedido(a) em namoro", "!namorar "+at(ana), "!recusar "+at(ana))
	if len(out.Mentions) != 2 {
		t.Fatalf("menções: %v", out.Mentions)
	}
	expect(t, e.as(t, ana, "!namorar", bia), "já pediu")
	expect(t, e.as(t, bia, "!namorar", ana), "OFICIALIZADO", at(bia)+" aceitou o pedido de namoro de "+at(ana))
	expect(t, e.as(t, ana, "!namorar", bia), "já estão namorando")

	// Casamento.
	expect(t, e.as(t, ana, "!casar", caio), "Só pode casar depois de namorar")
	expect(t, e.as(t, ana, "!casar", bia), "foi pedido(a) em casamento")
	expect(t, e.as(t, ana, "!casar", bia), "já pediu")
	expect(t, e.as(t, bia, "!casar", ana), "OFICIALMENTE CASADOS")
	expect(t, e.as(t, ana, "!casar", bia), "já estão casados")
	expect(t, e.as(t, bia, "!namorar", ana), "casados(as)")

	// Coisar: marcado e aleatório.
	expect(t, e.as(t, ana, "!coisar", caio), "só pode coisar com quem")
	out = e.as(t, ana, "!coisar", bia)
	expect(t, out, "motel", at(ana), at(bia))
	expect(t, e.as(t, bia, "!coisar"), at(bia)+" e "+at(ana))

	// Trair com membro aleatório: só sobra o Caio (identificado pelo número).
	out = e.as(t, ana, "!trair")
	expect(t, out, "ESCÂNDALO", at(ana)+" foi flagrado(a) traindo "+at(bia)+" com "+at(caio))
	if len(out.Mentions) != 3 {
		t.Fatalf("menções: %v", out.Mentions)
	}
	expect(t, e.as(t, ana, "!trair", bia), "trair seu parceiro(a) com ele(a) mesmo")
	expect(t, e.as(t, ana, "!trair", ana), "consigo mesmo")

	e.now = e.now.Add(26 * time.Hour)
	expect(t, e.as(t, ana, "!relacionamento"), "HISTÓRICO", "💍 Casamento", "✅ Ativo", "1 dia(s) e 2 hora(s)", "*Coisaram:* 2 vez(es)", "*Traições:* 1 vez(es)")
	expect(t, e.as(t, caio, "!relacionamentos"), "Casamentos Ativos: 1", "Namoros Ativos: 0", "Total de Coisadas: 2", "Casal Safado:* "+at(ana)+" & "+at(bia), "Casal Mais Traído")

	// Separação.
	expect(t, e.as(t, ana, "!separar"), "precisa marcar")
	expect(t, e.as(t, ana, "!separar", caio), "não tem nenhum relacionamento ativo")
	expect(t, e.as(t, bia, "!separar", ana), "FIM DE RELACIONAMENTO", "casamento 💍")
	expect(t, e.as(t, ana, "!relacionamento"), "💔 Terminado", "1 dia(s) e 2 hora(s)")
	expect(t, e.as(t, ana, "!coisar"), "não está em nenhum relacionamento")
	expect(t, e.as(t, ana, "!trair"), "não pode trair ninguém")
	expect(t, e.as(t, ana, "!relacionamentos"), "Não há relacionamentos ativos", "Terminados: 1")
}

func TestRefuse(t *testing.T) {
	e := setup(t)
	expect(t, e.as(t, bia, "!recusar"), "precisa marcar")
	expect(t, e.as(t, bia, "!recusar", ana), "Não há nenhum pedido pendente")
	e.as(t, ana, "!namorar", bia)
	expect(t, e.as(t, bia, "!recusar", ana), "PEDIDO RECUSADO", "recusou o pedido de namoro")
	expect(t, e.as(t, bia, "!namorar", ana), "foi pedido(a) em namoro") // novo pedido, não aceite
	expect(t, e.as(t, ana, "!recusar", bia), "PEDIDO RECUSADO")
}

func TestLIDMentionMatchesPhone(t *testing.T) {
	e := setup(t)
	// Pedido marcando o LID; o aceite chega com o número do Caio.
	e.as(t, ana, "!namorar", caioL)
	expect(t, e.as(t, caio, "!namorar", ana), "OFICIALIZADO")
	// E o Caio mandando como LID (sem número) também é reconhecido.
	m := bottest.GroupMsg("!coisar")
	m.Sender = caioL
	e.r.Handle(context.Background(), m)
	expect(t, e.fc.Last().Out, at(caio)+" e "+at(ana))
}

func TestEmptyGroupStats(t *testing.T) {
	e := setup(t)
	expect(t, e.as(t, ana, "!relacionamentos"), "Nenhum relacionamento")
	expect(t, e.as(t, ana, "!relacionamento"), "não tem nenhum relacionamento")
}

func TestGroupOnly(t *testing.T) {
	e := setup(t)
	e.r.Handle(context.Background(), bottest.PrivateMsg("!namorar"))
	expect(t, e.fc.Last().Out, "só funciona em grupos")
}

func TestDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                                "0 segundo(s)",
		42 * time.Second:                 "42 segundo(s)",
		3*time.Minute + 5*time.Second:    "3 minuto(s) e 5 segundo(s)",
		2*time.Hour + 7*time.Minute:      "2 hora(s) e 7 minuto(s)",
		3*24*time.Hour + 4*time.Hour + 1: "3 dia(s) e 4 hora(s)",
	}
	for d, want := range cases {
		if got := relationship.Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}
