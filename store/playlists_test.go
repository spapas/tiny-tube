package store

import (
	"reflect"
	"testing"
)

func testPlaylistStores(t *testing.T) map[string]Store {
	t.Helper()
	return map[string]Store{"fs": NewFS(t.TempDir()), "sqlite": newTestSQLite(t)}
}

func TestPlaylistCRUD(t *testing.T) {
	for name, s := range testPlaylistStores(t) {
		p := Playlist{ID: "series1", Name: "Course", Items: []string{"b", "a"}}
		if err := s.PutPlaylist(p); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := s.GetPlaylist("series1")
		if err != nil || got.Name != "Course" || !reflect.DeepEqual(got.Items, []string{"b", "a"}) || got.Created == "" {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
		// Rename preserves items and creation time.
		p.Name = "Renamed"
		p.Items = []string{"b", "a", "c"}
		if err := s.PutPlaylist(p); err != nil {
			t.Fatal(err)
		}
		got, err = s.GetPlaylist("series1")
		if err != nil || got.Name != "Renamed" || !reflect.DeepEqual(got.Items, []string{"b", "a", "c"}) {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
		lists, err := s.ListPlaylists()
		if err != nil || len(lists) != 1 || lists[0].ID != "series1" {
			t.Fatalf("%s: %+v %v", name, lists, err)
		}
		if err := s.DeletePlaylist("series1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetPlaylist("series1"); err == nil {
			t.Fatalf("%s: deleted playlist readable", name)
		}
		if err := s.DeletePlaylist("series1"); err == nil {
			t.Fatalf("%s: double delete silent", name)
		}
		lists, err = s.ListPlaylists()
		if err != nil || len(lists) != 0 {
			t.Fatalf("%s: %+v %v", name, lists, err)
		}
	}
}

func TestPlaylistValidation(t *testing.T) {
	for name, s := range testPlaylistStores(t) {
		for _, p := range []Playlist{
			{ID: "../escape", Name: "x"},
			{ID: "ok", Name: ""},
			{ID: "ok", Name: "x", Items: []string{"../escape"}},
		} {
			if err := s.PutPlaylist(p); err == nil {
				t.Fatalf("%s: invalid playlist accepted: %+v", name, p)
			}
		}
		big := Playlist{ID: "big", Name: "x"}
		for i := 0; i <= MaxPlaylistItems; i++ {
			big.Items = append(big.Items, "v")
		}
		if err := s.PutPlaylist(big); err == nil {
			t.Fatalf("%s: oversized playlist accepted", name)
		}
		if _, err := s.GetPlaylist("../escape"); err == nil {
			t.Fatalf("%s: invalid id accepted", name)
		}
	}
}
