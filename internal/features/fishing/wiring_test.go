package fishing_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/joaodobread/ravena-go/internal/features/fishing"
	"github.com/joaodobread/ravena-go/internal/features/slots"
	"github.com/joaodobread/ravena-go/internal/store"
)

// Os dois módulos se encaixam sem um importar o outro.
var (
	_ slots.Tackle   = (*fishing.Module)(nil)
	_ fishing.Wallet = (*slots.Module)(nil)
	_ slots.Tackle   = (*fishing.Game)(nil)
)

func TestRealWalletAndTackle(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := slots.Migrate(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := fishing.Migrate(ctx, st); err != nil {
		t.Fatal(err)
	}
	wallet := slots.New(st.DB(), nil, nil, nil)
	fish := fishing.New(st.DB(), wallet, nil, nil, nil)
	for range 50 { // inclui "Ficha de Cassino" (crédito na wallet real, fora da transação)
		if _, err := fish.GiveRandomUpgrade(ctx, "5511"); err != nil {
			t.Fatal(err)
		}
	}
	if err := fish.AddBaits(ctx, "5511", 2); err != nil {
		t.Fatal(err)
	}
	if n, err := wallet.Coins(ctx, "5511"); err != nil || n != slots.MaxCoins {
		t.Fatalf("Coins = %d, %v", n, err)
	}
}
