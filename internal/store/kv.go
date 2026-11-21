package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// GetJSON lê a chave key em v. ok=false se a chave não existir.
func (s *Store) GetJSON(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetJSON grava v em key.
func (s *Store) SetJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, string(raw))
	return err
}

// Usage é um uso de comando.
type Usage struct {
	Command, Chat, User string
	OK                  bool
	At                  time.Time
}

// LogUsage registra um uso de comando.
func (s *Store) LogUsage(ctx context.Context, u Usage) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO command_usage(command, chat, user, ok, at) VALUES(?, ?, ?, ?, ?)`,
		u.Command, u.Chat, u.User, u.OK, u.At.Unix())
	return err
}

// CommandCount é o total de usos de um comando.
type CommandCount struct {
	Command string
	Count   int
	Errors  int
}

// TopCommands soma os usos desde since, do mais usado ao menos usado.
// chat vazio considera todos os chats.
func (s *Store) TopCommands(ctx context.Context, since time.Time, chat string, limit int) ([]CommandCount, error) {
	q := `SELECT command, COUNT(*), SUM(CASE WHEN ok THEN 0 ELSE 1 END)
	      FROM command_usage WHERE at >= ?`
	args := []any{since.Unix()}
	if chat != "" {
		q += ` AND chat = ?`
		args = append(args, chat)
	}
	q += ` GROUP BY command ORDER BY COUNT(*) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandCount
	for rows.Next() {
		var c CommandCount
		if err := rows.Scan(&c.Command, &c.Count, &c.Errors); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneUsage apaga usos anteriores a before (para o banco não crescer sem fim).
func (s *Store) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM command_usage WHERE at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
