package api

// The durable journal owns the demand, Wake only schedules a cheap check. Local
// task callbacks do not buy a commander round or reset its debounce window.
func (s *Server) wakeCommanderIfNeeded(reason string) {
	if s.Decider == nil || s.Store == nil {
		return
	}
	a := s.Store.CommanderAttention()
	if a.More || reason == "watchdog" {
		s.Decider.Wake(reason)
		return
	}
	for _, r := range a.Reasons {
		if r.Code != "context_unacknowledged" {
			s.Decider.Wake(reason)
			return
		}
	}
}
