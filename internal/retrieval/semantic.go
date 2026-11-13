package retrieval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type EmbeddingUsage struct {
	Tokens    int       `json:"tokens"`
	Requests  int       `json:"requests"`
	StartedAt time.Time `json:"-"`
}
type Embedder interface {
	Key() string
	Name() string
	Embed(context.Context, []string) ([][]float32, EmbeddingUsage, error)
}
type HTTPEmbedder struct {
	Endpoint, Model, APIKey string
	Client                  *http.Client
}

func (e *HTTPEmbedder) Key() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimRight(e.Endpoint, "/")+"\x00"+e.Model)))
}
func (e *HTTPEmbedder) Name() string { return e.Model }
func (e *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, EmbeddingUsage, error) {
	usage := EmbeddingUsage{StartedAt: time.Now(), Requests: 1}
	u, err := url.Parse(e.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || e.Model == "" {
		return nil, usage, fmt.Errorf("invalid embedding configuration")
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/embeddings") {
		u.Path = strings.TrimRight(u.Path, "/") + "/embeddings"
	}
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": texts, "encoding_format": "float"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, usage, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, usage, fmt.Errorf("embedding transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, usage, fmt.Errorf("embedding request failed: HTTP %d", res.StatusCode)
	}
	var result struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			Total  int `json:"total_tokens"`
			Prompt int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&result); err != nil {
		return nil, usage, fmt.Errorf("invalid embedding response")
	}
	usage.Tokens = result.Usage.Total
	if usage.Tokens == 0 {
		usage.Tokens = result.Usage.Prompt
	}
	if len(result.Data) != len(texts) {
		return nil, usage, fmt.Errorf("embedding response count mismatch")
	}
	out := make([][]float32, len(texts))
	seen := map[int]bool{}
	for _, v := range result.Data {
		if v.Index < 0 || v.Index >= len(out) || seen[v.Index] || len(v.Embedding) == 0 {
			return nil, usage, fmt.Errorf("invalid embedding response index")
		}
		seen[v.Index] = true
		out[v.Index] = v.Embedding
	}
	return out, usage, nil
}
func textHash(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }

// Full fact text is indexed. Input over a provider's limit fails explicitly;
// it is never silently represented by only the start of a long observation.
func factText(f model.Fact) string {
	return f.Title + "\n" + f.Summary + "\n" + strings.Join(f.Tags, " ") + "\n" + f.Doc
}

type IndexPage struct {
	PageInfo
	Indexed   int            `json:"indexed_this_page"`
	Remaining int            `json:"remaining_to_visit"`
	Usage     EmbeddingUsage `json:"usage"`
}

func IndexFacts(ctx context.Context, s *store.Store, e Embedder, token string) (IndexPage, error) {
	if e == nil {
		return IndexPage{}, fmt.Errorf("semantic_unconfigured")
	}
	key := queryKey([]string{"embedding-index", e.Key()})
	db, c, err := read(s, token, key)
	if err != nil {
		return IndexPage{}, err
	}
	if c.Offset > len(db.Facts) {
		return IndexPage{}, fmt.Errorf("invalid_cursor")
	}
	upper, err := s.VectorWatermark()
	if err != nil {
		return IndexPage{}, err
	}
	hashes, texts := []string{}, []string{}
	end := c.Offset
	seen := map[string]bool{}
	for end < len(db.Facts) && len(texts) < 32 {
		batchEnd := end + 512
		if batchEnd > len(db.Facts) {
			batchEnd = len(db.Facts)
		}
		batchHashes, batchTexts := []string{}, []string{}
		for _, f := range db.Facts[end:batchEnd] {
			text := factText(f)
			batchTexts = append(batchTexts, text)
			batchHashes = append(batchHashes, textHash(text))
		}
		cached, err := s.ReadVectors(e.Key(), batchHashes, upper)
		if err != nil {
			return IndexPage{}, err
		}
		for i, hash := range batchHashes {
			if len(texts) >= 32 {
				break
			}
			if _, ok := cached[hash]; !ok && !seen[hash] {
				hashes = append(hashes, hash)
				texts = append(texts, batchTexts[i])
				seen[hash] = true
			}
			end++
		}
	}
	out := IndexPage{PageInfo: PageInfo{Version: c.Version, Total: len(db.Facts)}, Indexed: len(texts), Remaining: len(db.Facts) - end}
	if len(texts) > 0 {
		vectors, usage, err := e.Embed(ctx, texts)
		out.Usage = usage
		if err != nil {
			return out, err
		}
		if len(vectors) != len(texts) {
			return out, fmt.Errorf("embedding count mismatch")
		}
		batch := map[string][]float32{}
		for i, v := range vectors {
			batch[hashes[i]] = v
		}
		if err = s.CacheVectors(e.Key(), batch); err != nil {
			return out, err
		}
	}
	out.HasMore = end < len(db.Facts)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}

type SearchStatus struct {
	Mode     string         `json:"mode"`
	Model    string         `json:"model,omitempty"`
	Indexed  int            `json:"indexed"`
	Total    int            `json:"total"`
	Complete bool           `json:"complete"`
	Usage    EmbeddingUsage `json:"usage"`
}
type SearchMatch struct {
	Semantic float64 `json:"semantic_similarity"`
	Lexical  int     `json:"lexical_score"`
}
type IndexIncomplete struct{ Indexed, Total int }

func (e *IndexIncomplete) Error() string {
	return "semantic_index_incomplete: use POST /api/retrieval/index and follow next_cursor"
}

func SemanticFacts(ctx context.Context, s *store.Store, q FactQuery, e Embedder) (FactPage, error) {
	if e == nil {
		return FactPage{}, fmt.Errorf("semantic_unconfigured")
	}
	if strings.TrimSpace(q.Q) == "" {
		return FactPage{}, fmt.Errorf("semantic query is required")
	}
	key := queryKey(struct {
		Type, Provider string
		Query          FactQuery
	}{"semantic", e.Key(), q})
	db, c, err := read(s, q.Cursor, key)
	if err != nil {
		return FactPage{}, err
	}
	upper, err := s.VectorWatermark()
	if err != nil {
		return FactPage{}, err
	}
	usage := EmbeddingUsage{}
	queryHash := textHash("query\x00" + q.Q)
	queryVectors, err := s.ReadVectors(e.Key(), []string{queryHash}, upper)
	if err != nil {
		return FactPage{}, err
	}
	queryVector, ok := queryVectors[queryHash]
	if !ok {
		vectors, u, err := e.Embed(ctx, []string{q.Q})
		usage = u
		if err != nil {
			return FactPage{}, err
		}
		if len(vectors) != 1 {
			return FactPage{}, fmt.Errorf("query embedding count mismatch")
		}
		queryVector = vectors[0]
		if err = s.CacheVectors(e.Key(), map[string][]float32{queryHash: queryVector}); err != nil {
			return FactPage{}, err
		}
		upper, err = s.VectorWatermark()
		if err != nil {
			return FactPage{}, err
		}
	}
	if c.Semantic == 0 {
		c.Semantic = upper
	}
	type ranked struct {
		fact     model.Fact
		semantic float64
		lexical  int
		score    float64
	}
	rows := []ranked{}
	eligible := []model.Fact{}
	for _, f := range db.Facts {
		if q.Kind != "" && f.Kind != q.Kind || q.Intent != "" && f.IntentID != q.Intent || q.Status != "" && f.Status != q.Status {
			continue
		}
		if q.Tag != "" {
			found := false
			for _, tag := range f.Tags {
				if strings.EqualFold(tag, q.Tag) {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		eligible = append(eligible, f)
	}
	indexed := 0
	for start := 0; start < len(eligible); start += 512 {
		end := start + 512
		if end > len(eligible) {
			end = len(eligible)
		}
		hashes := make([]string, end-start)
		for i, f := range eligible[start:end] {
			hashes[i] = textHash(factText(f))
		}
		vectors, err := s.ReadVectors(e.Key(), hashes, c.Semantic)
		if err != nil {
			return FactPage{}, err
		}
		for i, f := range eligible[start:end] {
			v, ok := vectors[hashes[i]]
			if !ok {
				continue
			}
			similarity, err := cosine(queryVector, v)
			if err != nil {
				return FactPage{}, err
			}
			lexical, _ := TextScore(q.Q, f.Title, f.Summary, f.Doc)
			rows = append(rows, ranked{fact: f, semantic: similarity, lexical: lexical})
			indexed++
		}
	}
	if indexed != len(eligible) {
		return FactPage{Search: &SearchStatus{Mode: q.Mode, Model: e.Name(), Indexed: indexed, Total: len(eligible), Usage: usage}}, &IndexIncomplete{indexed, len(eligible)}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].semantic != rows[j].semantic {
			return rows[i].semantic > rows[j].semantic
		}
		return rows[i].fact.ID < rows[j].fact.ID
	})
	for i := range rows {
		rows[i].score = 1 / float64(60+i+1)
	}
	if q.Mode == "hybrid" {
		order := make([]int, len(rows))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(i, j int) bool {
			a, b := rows[order[i]], rows[order[j]]
			if a.lexical != b.lexical {
				return a.lexical > b.lexical
			}
			return a.fact.ID < b.fact.ID
		})
		for rank, i := range order {
			if rows[i].lexical > 0 {
				rows[i].score += 1 / float64(60+rank+1)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].score != rows[j].score {
				return rows[i].score > rows[j].score
			}
			return rows[i].fact.ID < rows[j].fact.ID
		})
	}
	out := FactPage{Facts: []model.Fact{}, PageInfo: PageInfo{Version: c.Version, Total: len(rows)}, Search: &SearchStatus{Mode: q.Mode, Model: e.Name(), Indexed: indexed, Total: len(rows), Complete: true, Usage: usage}, Matches: map[string]SearchMatch{}}
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor")
	}
	end := c.Offset + pageLimit(q.Limit)
	if end > len(rows) {
		end = len(rows)
	}
	for _, v := range rows[c.Offset:end] {
		out.Facts = append(out.Facts, v.fact)
		out.Matches[v.fact.ID] = SearchMatch{v.semantic, v.lexical}
	}
	out.HasMore = end < len(rows)
	if out.HasMore {
		c.Offset = end
		out.NextCursor = encode(c)
	}
	return out, nil
}
func cosine(a, b []float32) (float64, error) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, fmt.Errorf("embedding dimensions changed; rebuild with a new model key")
	}
	dot, aa, bb := float64(0), float64(0), float64(0)
	for i, v := range a {
		f, g := float64(v), float64(b[i])
		dot += f * g
		aa += f * f
		bb += g * g
	}
	if aa == 0 || bb == 0 {
		return 0, fmt.Errorf("zero embedding")
	}
	return dot / math.Sqrt(aa*bb), nil
}
