package settings

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilePausePersistsWithoutChangingCredentialsOrBinding(t *testing.T) {
	s, path := loadProfiles(t, twoProfiles)
	before, err := s.SavedProfile("fast")
	if err != nil {
		t.Fatal(err)
	}
	paused, err := s.SetProfilePaused("fast", true)
	if err != nil {
		t.Fatal(err)
	}
	if !paused.Paused || paused.Runnable() || paused.Revision() != before.Revision() || paused.APIKey != before.APIKey {
		t.Fatal("pause changed the binding or did not refuse new runs")
	}
	if _, err := s.ActivateProfile("fast"); err == nil {
		t.Fatal("paused profile could be activated")
	}
	if err := s.WriteProfileConfig("fast", filepath.Join(t.TempDir(), "host.yaml")); err == nil {
		t.Fatal("paused host config was materialized")
	}
	// Ordinary edits do not author admission state or silently resume a profile.
	paused.Name, paused.APIKey = "Renamed", ""
	if _, err := s.SaveProfile(paused); err != nil {
		t.Fatal(err)
	}
	reopened, err := LoadAt(filepath.Join(t.TempDir(), "settings.json"), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.SavedProfile("fast")
	if err != nil || !got.Paused || got.APIKey != before.APIKey {
		t.Fatalf("reopened: %+v, %v", viewOfProfile(got, "fast"), err)
	}
	if _, err := reopened.SetProfilePaused("fast", false); err != nil {
		t.Fatal(err)
	}
	got, _ = reopened.SavedProfile("fast")
	if got.Paused || !got.Runnable() || got.Revision() != before.Revision() {
		t.Fatal("resume failed to restore the same binding")
	}
}

func TestProfilePauseMustBeBoolean(t *testing.T) {
	s, _ := loadProfiles(t, strings.Replace(twoProfiles, "id: fast", "id: fast\n          paused: 'true'", 1))
	if _, err := s.Profiles(); err == nil {
		t.Fatal("string-valued admission state was accepted")
	}
}

func TestProfilePauseMetadataNeverReachesHarness(t *testing.T) {
	s, _ := loadProfiles(t, strings.Replace(twoProfiles, "id: fast", "id: fast\n          paused: false", 1))
	dest := filepath.Join(t.TempDir(), "host.yaml")
	if err := s.WriteProfileConfig("fast", dest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, dest), "paused:") {
		t.Fatal("admission metadata reached strict harness configuration")
	}
}
