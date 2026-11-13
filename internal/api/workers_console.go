package api

// Console (operator-token) surface for spawn/workers/kill.
//
// These are the same operations as the participant routes in workers.go and
// deliberately share one implementation: an operator and an executor should see
// the same request/response shape, the same idempotency, capacity and kill
// semantics. Only the caller differs:
//
//   - an operator's spawn records the actor from X-Tw-Actor ("operator", or
//     "commander" when the commander drives the console API) in CreatedBy, which
//     distinguishes it from an executor-created "pt_in_x"; the auth gate stamps
//     that header from the agent token, so the commander's spawn is attributed
//     even without the model sending the header itself;
//   - the roster treats every worker as killable while it is still active;
//   - kill skips the ownership check but still refuses a finished worker (409).

import "net/http"

// consoleSpawn is POST /p/<project>/api/spawn.
func (s *Server) consoleSpawn(w http.ResponseWriter, r *http.Request) {
	s.spawnWorker(w, r, workerCaller{participant: actorOf(r), operator: true})
}

// consoleWorkers is GET /p/<project>/api/workers.
func (s *Server) consoleWorkers(w http.ResponseWriter, r *http.Request) {
	s.workersRoster(w, workerCaller{participant: actorOf(r), operator: true})
}

// consoleKillWorker is POST /p/<project>/api/workers/{id}/kill.
func (s *Server) consoleKillWorker(w http.ResponseWriter, r *http.Request) {
	s.killWorker(w, r, workerCaller{participant: actorOf(r), operator: true})
}
