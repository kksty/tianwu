package settings

import "fmt"

// SetProfilePaused changes admission only. It preserves the binding revision,
// credentials and hosts, so an existing run is not interrupted by a toggle.
func (s *Store) SetProfilePaused(id string, paused bool) (Profile, error) {
	if err := validProfileID(id); err != nil {
		return Profile{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	root, doc, err := s.readDocument()
	if err != nil {
		return Profile{}, err
	}
	entry := findEntry(mapGet(mapGet(doc, "llm"), "providers"), id)
	if entry == nil {
		return Profile{}, fmt.Errorf("%w：%s", ErrProfileNotFound, id)
	}
	p, err := decodeProfile(entry)
	if err != nil || p.Paused == paused {
		return p, err
	}
	if paused {
		setScalar(entry, "paused", "true")
		mapGet(entry, "paused").Tag = "!!bool"
	} else {
		mapDelete(entry, "paused")
	}
	if err := s.writeDocument(root); err != nil {
		return Profile{}, err
	}
	p.Paused = paused
	return p, nil
}
