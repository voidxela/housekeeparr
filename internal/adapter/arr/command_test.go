package arr

import (
	"context"
	"os"
	"slices"
	"testing"
)

// Fixtures are sanitized captures of GET /api/v3/command from the
// disposable Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 instances of the
// integration suite (fictional content, no credentials or paths), right
// after a recovery: the manager-native replacement search has trigger
// "unspecified" and names the content in its body.
func TestCommandsFromReleasedManagers(t *testing.T) {
	for _, tc := range []struct {
		file, search string
		id           int64
		ids          func(Command) []int64
		content      int64
	}{
		{"../sonarr/testdata/v4.0.20-command.json", "EpisodeSearch", 45, func(c Command) []int64 { return c.EpisodeIDs }, 5},
		{"../radarr/testdata/v6.4.4-command.json", "MoviesSearch", 46, func(c Command) []int64 { return c.MovieIDs }, 5},
	} {
		body, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		cmds, err := serve(t, map[string]string{"/api/v3/command": string(body)}).Commands(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		var got *Command
		for i := range cmds {
			if cmds[i].ID == tc.id {
				got = &cmds[i]
			}
		}
		if got == nil || got.Name != tc.search || got.Trigger != "unspecified" || got.Status != "completed" || got.Result != "successful" ||
			got.Ended == nil || got.Started == nil || got.Queued.IsZero() || !slices.Contains(tc.ids(*got), tc.content) || got.Message == "" {
			t.Fatalf("%s: replacement search %+v", tc.file, got)
		}
	}
}

func TestCommandsRejectMalformedLists(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[{"name":"EpisodeSearch","status":"queued","queued":"2026-11-16T19:00:04Z"}]`,
		`[{"id":3,"name":"EpisodeSearch","queued":"2026-11-16T19:00:04Z"}]`, `[{"id":3,"name":"EpisodeSearch","status":"queued"}]`} {
		if _, err := serve(t, map[string]string{"/api/v3/command": body}).Commands(context.Background()); err == nil {
			t.Fatalf("%s accepted as a command list", body)
		}
	}
	cmds, err := serve(t, map[string]string{"/api/v3/command": `[]`}).Commands(context.Background())
	if err != nil || len(cmds) != 0 {
		t.Fatalf("explicit empty list: %v %+v", err, cmds)
	}
}
