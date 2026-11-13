package retrieval

import (
	"encoding/json"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type Context struct {
	DeliveryID  string     `json:"delivery_id"`
	Changes     ChangePage `json:"changes"`
	Survey      FactPage   `json:"survey"`
	Impacts     []Impact   `json:"impacts"`
	Acknowledge string     `json:"acknowledge"`
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// A delivery is replayed verbatim until explicitly acknowledged. A successful
// run or a delivered tool result is not evidence of model consumption.
func WorkingContext(s *store.Store, name string) (Context, error) {

	if pending, ok := s.PendingDelivery(name); ok {
		var out Context
		e := json.Unmarshal(pending.Payload, &out)
		return out, e
	}
	checkpoint := s.ReadCheckpoint(name)
	surveyHash := s.ContentHash()
	changes, e := Changes(s, checkpoint.Changes, 20)
	if e != nil {
		return Context{}, e
	}
	survey := FactPage{Facts: []model.Fact{}}
	if !checkpoint.ScanComplete || checkpoint.SurveyHash != surveyHash {
		survey, e = Facts(s, FactQuery{Ascending: true, Cursor: checkpoint.Scan, Limit: 10})
	}
	if e != nil {
		return Context{}, e
	}
	for i := range survey.Facts {
		f := &survey.Facts[i]
		f.Doc = ""
		f.Title = clip(f.Title, 160)
		f.Summary = clip(f.Summary, 240)
	}
	db, _ := s.ReadView()
	factIDs := []string{}
	seen := map[string]bool{}
	for _, c := range changes.Changes {
		if c.Kind == "fact" && !seen[c.ID] {
			factIDs = append(factIDs, c.ID)
			seen[c.ID] = true
		}
	}
	out := Context{DeliveryID: store.NewID("delivery"), Changes: changes, Survey: survey, Impacts: Impacts(db, factIDs), Acknowledge: "POST /api/context/ack with delivery_id after processing or explicitly retaining the references in state"}
	for i := range out.Impacts {
		v := &out.Impacts[i]
		v.IntentsTotal = len(v.Intents)
		v.GoalsTotal = len(v.Goals)
		v.Query = "/api/graph?root=fact:" + v.Fact + "&direction=descendants&depth=0"
		if len(v.Intents) > 5 {
			v.Intents = v.Intents[:5]
		}
		if len(v.Goals) > 5 {
			v.Goals = v.Goals[:5]
		}
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	saved, e := s.SaveDelivery(name, store.Delivery{ID: out.DeliveryID, Expected: checkpoint, Next: store.Checkpoint{Changes: changes.NextCursor, ChangesPending: changes.HasMore, Scan: survey.NextCursor, ScanComplete: !survey.HasMore, SurveyHash: surveyHash}, Payload: raw})
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(saved.Payload, &out)
	return out, e
}
