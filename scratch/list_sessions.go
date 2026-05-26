//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type SessionMeta struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	IsBusy    bool   `json:"is_busy"`
	Alive     bool   `json:"alive"`
	UpdatedAt int64  `json:"updated_at"`
}

func main() {
	natsURL := "nats://47.110.255.240:4222"
	token := "ymm_rpc_2026"

	nc, err := nats.Connect(natsURL, nats.Token(token))
	if err != nil {
		log.Fatalf("NATS connect failed: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("Jetstream failed: %v", err)
	}

	kv, err := js.KeyValue(context.Background(), "CRUSH_SESSIONS")
	if err != nil {
		log.Fatalf("KV failed: %v", err)
	}

	keys, err := kv.Keys(context.Background())
	if err != nil {
		log.Fatalf("Keys failed: %v", err)
	}

	var sessions []SessionMeta
	for _, key := range keys {
		entry, err := kv.Get(context.Background(), key)
		if err != nil {
			continue
		}
		var meta SessionMeta
		if err := json.Unmarshal(entry.Value(), &meta); err == nil {
			sessions = append(sessions, meta)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})

	for _, s := range sessions {
		fmt.Printf("ID: %s, Title: %q, Path: %q, Alive: %v, Busy: %v, Updated: %s\n",
			s.SessionID, s.Title, s.Path, s.Alive, s.IsBusy, time.Unix(s.UpdatedAt, 0).Format(time.RFC3339))
	}
}
