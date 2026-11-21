// Package store é o banco da aplicação (SQLite): configurações de grupos,
// configurações globais, estatísticas e tabelas das funcionalidades.
// A sessão do WhatsApp fica em outro arquivo, gerenciado pelo whatsmeow.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	_ "modernc.org/sqlite" // driver "sqlite"
)

// Store é o acesso ao banco.
type Store struct {
	db *sql.DB

	mu sync.RWMutex
	// groups é o cache dos grupos; nil marca "não salvo" (evita ler o banco a
	// cada mensagem de grupos sem configuração). O banco é a fonte da verdade.
	groups map[string]*Group
}

// Open abre (ou cria) o banco em path e aplica as migrações do núcleo.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite só tem um escritor; uma conexão evita SQLITE_BUSY e é rápida o
	// bastante para o volume de um bot.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, groups: map[string]*Group{}}
	if err := s.Migrate(ctx, "core", coreMigrations); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close fecha o banco.
func (s *Store) Close() error { return s.db.Close() }

// DB expõe o *sql.DB para as funcionalidades com tabelas próprias.
// Crie as tabelas com Migrate e um nome único (ex.: o nome da funcionalidade).
func (s *Store) DB() *sql.DB { return s.db }

var coreMigrations = []string{
	`CREATE TABLE schema_versions (scope TEXT PRIMARY KEY, version INTEGER NOT NULL)`,
	`CREATE TABLE groups (
		jid        TEXT PRIMARY KEY,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE command_usage (
		id      INTEGER PRIMARY KEY,
		command TEXT NOT NULL,
		chat    TEXT NOT NULL,
		user    TEXT NOT NULL,
		ok      INTEGER NOT NULL,
		at      INTEGER NOT NULL
	)`,
	`CREATE INDEX command_usage_at ON command_usage(at)`,
	`CREATE INDEX command_usage_chat_at ON command_usage(chat, at)`,
}

// Migrate aplica, em ordem e uma única vez, as migrações de scope.
// Migrações já aplicadas nunca podem ser editadas, só acrescentadas.
func (s *Store) Migrate(ctx context.Context, scope string, migrations []string) error {
	// A tabela de versões é criada pela 1ª migração do núcleo; antes dela
	// não há o que consultar.
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_versions (scope TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_versions WHERE scope = ?`, scope).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for i := current; i < len(migrations); i++ {
		stmt := migrations[i]
		if scope == "core" && i == 0 {
			continue // schema_versions já foi criada acima
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback()
			return fmt.Errorf("migração %s #%d: %w", scope, i+1, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_versions(scope, version) VALUES(?, ?)
			 ON CONFLICT(scope) DO UPDATE SET version = excluded.version`, scope, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
