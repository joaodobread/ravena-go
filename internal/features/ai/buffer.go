package ai

import (
	"container/list"
	"strings"
	"sync"
	"unicode/utf8"
)

// Limites do buffer de conversa. No pior caso: 200 chats × 100 msgs × ~350 B ≈ 7 MB,
// mas chats pequenos só alocam o que usam.
const (
	defaultPerChat  = 100
	defaultMaxChats = 200
	maxEntryRunes   = 300
	maxNameRunes    = 40
)

// Entry é uma mensagem guardada para resumo/interação.
type Entry struct {
	Author string
	Text   string
}

type chatRing struct {
	key     string
	entries []Entry // cresce até cap; depois sobrescreve em anel
	next    int     // posição da próxima escrita quando cheio
}

// Buffer guarda as últimas mensagens de texto por chat, com teto de mensagens
// por chat e de chats (o menos usado sai primeiro).
type Buffer struct {
	mu       sync.Mutex
	perChat  int
	maxChats int
	lru      *list.List // de *chatRing; frente = mais recente
	index    map[string]*list.Element
}

// NewBuffer cria um Buffer; zeros usam os padrões.
func NewBuffer(perChat, maxChats int) *Buffer {
	if perChat <= 0 {
		perChat = defaultPerChat
	}
	if maxChats <= 0 {
		maxChats = defaultMaxChats
	}
	return &Buffer{perChat: perChat, maxChats: maxChats, lru: list.New(), index: map[string]*list.Element{}}
}

// truncate corta s em n runas e copia, para não segurar a mensagem original na memória.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		i, count := 0, 0
		for i = range s {
			if count == n {
				break
			}
			count++
		}
		s = s[:i] + "…"
	}
	return strings.Clone(s)
}

// Add guarda uma mensagem de chat.
func (b *Buffer) Add(chat, author, text string) {
	text = truncate(text, maxEntryRunes)
	if text == "" {
		return
	}
	e := Entry{Author: truncate(author, maxNameRunes), Text: text}

	b.mu.Lock()
	defer b.mu.Unlock()
	el, ok := b.index[chat]
	if !ok {
		el = b.lru.PushFront(&chatRing{key: chat, entries: make([]Entry, 0, 8)})
		b.index[chat] = el
		for b.lru.Len() > b.maxChats {
			old := b.lru.Back()
			delete(b.index, old.Value.(*chatRing).key)
			b.lru.Remove(old)
		}
	} else {
		b.lru.MoveToFront(el)
	}
	r := el.Value.(*chatRing)
	if len(r.entries) < b.perChat {
		r.entries = append(r.entries, e)
		return
	}
	r.entries[r.next] = e
	r.next = (r.next + 1) % b.perChat
}

// Recent devolve uma cópia das mensagens de chat, da mais antiga à mais nova.
func (b *Buffer) Recent(chat string) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	el, ok := b.index[chat]
	if !ok {
		return nil
	}
	r := el.Value.(*chatRing)
	out := make([]Entry, 0, len(r.entries))
	out = append(out, r.entries[r.next:]...)
	return append(out, r.entries[:r.next]...)
}

// Clear esquece as mensagens de chat.
func (b *Buffer) Clear(chat string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if el, ok := b.index[chat]; ok {
		delete(b.index, chat)
		b.lru.Remove(el)
	}
}

// Len diz quantos chats estão guardados.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lru.Len()
}

// maxPromptChars limita o histórico mandado ao LLM (custo de tokens).
const maxPromptChars = 12000

// formatEntries monta "autor: texto" por linha, mantendo as mais recentes
// dentro de maxPromptChars.
func formatEntries(entries []Entry) string {
	total, start := 0, len(entries)
	for start > 0 {
		e := entries[start-1]
		n := len(e.Author) + len(e.Text) + 3
		if total+n > maxPromptChars {
			break
		}
		total += n
		start--
	}
	var sb strings.Builder
	sb.Grow(total)
	for _, e := range entries[start:] {
		sb.WriteString(e.Author)
		sb.WriteString(": ")
		sb.WriteString(e.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}
