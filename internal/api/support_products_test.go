package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kksty/tianwu/internal/aop/tool"
	native "github.com/kksty/tianwu/internal/aop/traffic"
	"github.com/kksty/tianwu/internal/artifact"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/traffic"
)

// Native artifacts and HTTP captures are persisted per run, so a report or
// verification ("support") run leaves its own products in the project case
// directory. Reading them is reading the review/report output, which
// docs/01-architecture.md §8 keeps out of both the commander's and the executor's
// API view. The boundary is decided from the owning run's persisted purpose, so
// the tests pin the products to real run rows (a normal direction run, a verifier
// run, and one orphan whose run row does not exist -> fail closed).

type supportProducts struct {
	normalArtifact   artifact.Record
	verifierArtifact artifact.Record
	orphanArtifact   artifact.Record
	normalFlow       traffic.Capture
	verifierFlow     traffic.Capture
	orphanFlow       traffic.Capture
}

func supportProductsFixture(t *testing.T) (*Server, supportProducts) {
	t.Helper()
	s, _ := newBindingServer(t)
	in, err := s.Store.CreateIntent(model.Intent{Title: "产品面"})
	if err != nil {
		t.Fatal(err)
	}
	normalRun, err := s.Store.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	verifierRun, err := s.Store.AddRun(model.Run{IntentID: in.ID, Purpose: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	saveArtifact := func(runID, resultID, body string) artifact.Record {
		t.Helper()
		rec, err := artifact.Save(s.CaseRoot, runID, in.ID, "ev-"+resultID, &tool.Artifact{
			Tool: "nuclei", Kind: "service", Target: "https://example.test",
			MediaType: "application/json", ResultId: resultID, Data: []byte(body),
		})
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	saveFlow := func(runID, flowID, target, body string) traffic.Capture {
		t.Helper()
		cap, err := traffic.SaveCapture(s.CaseRoot, runID, in.ID, "sess-"+flowID, "ev-"+flowID, &native.Flow{
			Id: flowID, Complete: true,
			Request:  &native.HttpRequest{Method: "GET", Url: target},
			Response: &native.HttpResponse{StatusCode: 200, Body: []byte(body)},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return cap
	}
	const orphanRun = "r_00000000000000ff"
	return s, supportProducts{
		normalArtifact:   saveArtifact(normalRun.ID, "normal-artifact", `{"normal":true}`),
		verifierArtifact: saveArtifact(verifierRun.ID, "verifier-artifact", `{"verifier":true}`),
		orphanArtifact:   saveArtifact(orphanRun, "orphan-artifact", `{"orphan":true}`),
		normalFlow:       saveFlow(normalRun.ID, "flow-normal", "https://example.test/normal", "normal"),
		verifierFlow:     saveFlow(verifierRun.ID, "flow-verifier", "https://example.test/review", "verifier"),
		orphanFlow:       saveFlow(orphanRun, "flow-orphan", "https://example.test/orphan", "orphan"),
	}
}

func decodeArtifactRefs(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("artifacts = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Artifacts []artifact.Record `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	for _, row := range body.Artifacts {
		refs[row.Ref] = true
	}
	return refs
}

func decodeFlows(t *testing.T, rec *httptest.ResponseRecorder) (map[string]bool, int) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("flows = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Flows []traffic.Capture `json:"flows"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	for _, row := range body.Flows {
		refs[row.Ref] = true
	}
	return refs, body.Total
}

// A harness session (the commander on the project mux and an executor on the
// participant mux) must not see a support run's native products or HTTP captures,
// while the operator keeps the full audit view. An orphan product (the run row is
// gone) is hidden too: the purpose cannot be shown to be battlefield work.
func TestHarnessSessionCannotSeeSupportRunProducts(t *testing.T) {
	s, products := supportProductsFixture(t)

	surfaces := []struct {
		name string
		do   func(method, path string) *httptest.ResponseRecorder
	}{
		{"commander", func(method, path string) *httptest.ResponseRecorder {
			return serveMux(t, s, harnessRequest(method, path))
		}},
		{"participant", func(method, path string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			s.AgentHandler().ServeHTTP(rec, agentRequest(s, method, path))
			return rec
		}},
	}
	for _, surface := range surfaces {
		t.Run(surface.name, func(t *testing.T) {
			refs := decodeArtifactRefs(t, surface.do(http.MethodGet, "/api/artifacts"))
			if !refs[products.normalArtifact.Ref] {
				t.Fatalf("%s lost the direction's native result: %+v", surface.name, refs)
			}
			if refs[products.verifierArtifact.Ref] {
				t.Fatalf("%s leaked a verifier native result: %+v", surface.name, refs)
			}
			if refs[products.orphanArtifact.Ref] {
				t.Fatalf("%s leaked an unattributed native result: %+v", surface.name, refs)
			}

			if rec := surface.do(http.MethodGet, "/api/artifacts/data?ref="+url.QueryEscape(products.verifierArtifact.Ref)); rec.Code != http.StatusNotFound {
				t.Fatalf("%s verifier native data = %d, want 404: %s", surface.name, rec.Code, rec.Body.String())
			}
			if rec := surface.do(http.MethodGet, "/api/artifacts/data?ref="+url.QueryEscape(products.orphanArtifact.Ref)); rec.Code != http.StatusNotFound {
				t.Fatalf("%s orphan native data = %d, want 404: %s", surface.name, rec.Code, rec.Body.String())
			}
			normalData := surface.do(http.MethodGet, "/api/artifacts/data?ref="+url.QueryEscape(products.normalArtifact.Ref))
			if normalData.Code != http.StatusOK || normalData.Body.String() != `{"normal":true}` {
				t.Fatalf("%s normal native data = %d %q", surface.name, normalData.Code, normalData.Body.String())
			}

			flows, total := decodeFlows(t, surface.do(http.MethodGet, "/api/traffic/flows"))
			if !flows[products.normalFlow.Ref] {
				t.Fatalf("%s lost the direction's HTTP capture: %+v", surface.name, flows)
			}
			if flows[products.verifierFlow.Ref] || flows[products.orphanFlow.Ref] {
				t.Fatalf("%s leaked a support/orphan HTTP capture: %+v", surface.name, flows)
			}
			if total != 1 {
				t.Fatalf("%s total = %d, want only the visible capture counted", surface.name, total)
			}

			if rec := surface.do(http.MethodGet, "/api/traffic/flow?ref="+url.QueryEscape(products.verifierFlow.Ref)); rec.Code != http.StatusNotFound {
				t.Fatalf("%s verifier HTTP capture = %d, want 404: %s", surface.name, rec.Code, rec.Body.String())
			}
			if rec := surface.do(http.MethodGet, "/api/traffic/flow?ref="+url.QueryEscape(products.normalFlow.Ref)); rec.Code != http.StatusOK {
				t.Fatalf("%s normal HTTP capture = %d: %s", surface.name, rec.Code, rec.Body.String())
			}
		})
	}

	// The operator's console is the audit surface: report/verification products
	// stay readable, including products recorded before the run row was pruned.
	operator := func(method, path string) *httptest.ResponseRecorder {
		return serveMux(t, s, httptest.NewRequest(method, path, nil))
	}
	refs := decodeArtifactRefs(t, operator(http.MethodGet, "/api/artifacts"))
	for _, want := range []string{products.normalArtifact.Ref, products.verifierArtifact.Ref, products.orphanArtifact.Ref} {
		if !refs[want] {
			t.Fatalf("operator lost native result %s: %+v", want, refs)
		}
	}
	if rec := operator(http.MethodGet, "/api/artifacts/data?ref="+url.QueryEscape(products.verifierArtifact.Ref)); rec.Code != http.StatusOK || rec.Body.String() != `{"verifier":true}` {
		t.Fatalf("operator verifier native data = %d %q", rec.Code, rec.Body.String())
	}
	flows, total := decodeFlows(t, operator(http.MethodGet, "/api/traffic/flows"))
	for _, want := range []string{products.normalFlow.Ref, products.verifierFlow.Ref, products.orphanFlow.Ref} {
		if !flows[want] {
			t.Fatalf("operator lost HTTP capture %s: %+v", want, flows)
		}
	}
	if total != 3 {
		t.Fatalf("operator total = %d, want all 3 captures", total)
	}
	if rec := operator(http.MethodGet, "/api/traffic/flow?ref="+url.QueryEscape(products.verifierFlow.Ref)); rec.Code != http.StatusOK {
		t.Fatalf("operator verifier HTTP capture = %d: %s", rec.Code, rec.Body.String())
	}
}
