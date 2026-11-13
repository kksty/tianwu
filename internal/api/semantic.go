package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"net/http"
	"os"
	"strings"
	"time"
)

func configuredEmbedder() retrieval.Embedder {
	endpoint, model := strings.TrimSpace(os.Getenv("TW_EMBEDDING_URL")), strings.TrimSpace(os.Getenv("TW_EMBEDDING_MODEL"))
	if endpoint == "" && model == "" {
		return nil
	}
	return &retrieval.HTTPEmbedder{Endpoint: endpoint, Model: model, APIKey: os.Getenv("TW_EMBEDDING_KEY")}
}

type trackedEmbedder struct {
	retrieval.Embedder
	s *Server
}

func (e trackedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, retrieval.EmbeddingUsage, error) {
	if e.s.paused.Load() {
		return nil, retrieval.EmbeddingUsage{}, fmt.Errorf("project_paused")
	}
	start := time.Now()
	vectors, usage, err := e.Embedder.Embed(ctx, texts)
	run, saveErr := e.s.Store.AddRun(model.Run{Purpose: "embedding", Node: "local", Model: e.Name()})
	if saveErr != nil {
		return nil, usage, saveErr
	}
	end := time.Now()
	_, saveErr = e.s.Store.UpdateRun(run.ID, func(r *model.Run) {
		r.StartedAt = start
		r.EndedAt = &end
		r.Status = model.RunDone
		if err != nil {
			r.Status = model.RunFailed
			r.ExitReason = e.s.redactSecrets(err.Error())
		}
		r.Tokens = usage.Tokens
		r.Usage = model.Usage{InputTokens: usage.Tokens, TotalTokens: usage.Tokens, Requests: 1, Known: usage.Tokens > 0}
		if usage.Tokens == 0 {
			r.Usage.Missing = 1
		}
	})
	if saveErr != nil {
		return nil, usage, saveErr
	}
	return vectors, usage, err
}
func (s *Server) embeddingReader() retrieval.Embedder {
	if s.Embedder == nil {
		return nil
	}
	return trackedEmbedder{s.Embedder, s}
}
func (s *Server) searchFacts(r *http.Request) (retrieval.FactPage, error) {
	q := factQuery(r)
	switch q.Mode {
	case "", "lexical":
		return retrieval.Facts(s.Store, q)
	case "semantic", "hybrid":
		return retrieval.SemanticFacts(r.Context(), s.Store, q, s.embeddingReader())
	default:
		return retrieval.FactPage{}, fmt.Errorf("unknown search mode")
	}
}
func (s *Server) indexFacts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cursor string `json:"cursor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if s.paused.Load() {
		writeErr(w, 409, "项目已暂停，不请求 embedding 服务")
		return
	}
	out, err := retrieval.IndexFacts(r.Context(), s.Store, s.embeddingReader(), req.Cursor)
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) retrievalStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"configured": s.Embedder != nil}
	if s.Embedder != nil {
		out["model"] = s.Embedder.Name()
	}
	writeJSON(w, 200, out)
}
