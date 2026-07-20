package pokemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIngestDecodesTheTrainerObject(t *testing.T) {
	body := `{"game":"HG","generation":4,"count":0,"max_species":493,
		"trainer":{"ot":"Dylan","tid":11031,"sid":55664,"gender":0,"language":2,
			"played_minutes":3867,"money":4116,"money_max":999999,
			"progress":{"kind":"badges","count":16,"max":16},
			"hall_of_fame":{"clears":3,"teams":[{"members":[{"species":160,"level":55,"nickname":"Feraligatr"}]}]},
			"trusted":true,"notes":{"hall_of_fame_clears":"This game keeps the last 50 Hall of Fame visits, not a lifetime count."}},
		"dex":{"seen":[1],"caught":[1]},"pokemon":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, summary, err := c.Ingest(context.Background(), []byte("save"), "test.sav")
	if err != nil {
		t.Fatal(err)
	}
	tr := summary.Trainer
	if tr.OT != "Dylan" || tr.TID != 11031 {
		t.Fatalf("identity: %#v", tr)
	}
	if tr.PlayedMinutes != 3867 || tr.Money != 4116 {
		t.Fatalf("money/playtime: %#v", tr)
	}
	if tr.Progress == nil || tr.Progress.Kind != "badges" || tr.Progress.Count != 16 {
		t.Fatalf("progress: %#v", tr.Progress)
	}
	if tr.HallOfFame == nil || tr.HallOfFame.Clears == nil || *tr.HallOfFame.Clears != 3 {
		t.Fatalf("hall of fame clears: %#v", tr.HallOfFame)
	}
	if len(tr.Notes) != 1 {
		t.Fatalf("the sidecar's notes must survive decoding: %#v", tr.Notes)
	}
}

// A null clears count means "this game does not count", which is not zero. If it
// decoded to 0 the UI could not tell Gen 7 from a player who never won.
func TestIngestKeepsANullClearCountDistinctFromZero(t *testing.T) {
	body := `{"game":"SN","generation":7,"count":0,"max_species":802,
		"trainer":{"ot":"Ash","tid":1,"sid":2,"trusted":true,
			"hall_of_fame":{"clears":null,"teams":[]},
			"notes":{"hall_of_fame_clears":"This game records the Hall of Fame team but not how many times you have won."}},
		"dex":{"seen":[],"caught":[]},"pokemon":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()

	_, summary, err := NewClient(srv.URL).Ingest(context.Background(), []byte("s"), "t.sav")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Trainer.HallOfFame == nil {
		t.Fatal("a gen 7 save records teams; the object must survive")
	}
	if summary.Trainer.HallOfFame.Clears != nil {
		t.Fatalf("a null clear count must stay nil, not become 0: %#v", summary.Trainer.HallOfFame.Clears)
	}
}
