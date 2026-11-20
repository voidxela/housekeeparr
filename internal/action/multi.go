package action

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/store"
)

// A multi-episode release names one season and two or more episodes in
// its title: S03E01E02 (a list) or S03E01-02 / S03E01-E02 (a range).
var multiEpisodeTitle = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,3})e(\d{1,4})((?:[ ._]?e\d{1,4})+|-e?\d{1,4})(?:[^a-z0-9]|$)`)

var episodeToken = regexp.MustCompile(`(?i)e?(\d{1,4})`)

// parseMultiEpisode returns the season and episode numbers a release title
// names, when it names a single season and more than one episode. A title
// with a season pack or a single episode is not a multi-episode release.
func parseMultiEpisode(title string) (season int64, episodes []int64, ok bool) {
	ms := multiEpisodeTitle.FindAllStringSubmatch(title, -1)
	if len(ms) != 1 {
		return 0, nil, false
	}
	m := ms[0]
	season, _ = strconv.ParseInt(m[1], 10, 64)
	first, _ := strconv.ParseInt(m[2], 10, 64)
	episodes = []int64{first}
	if strings.HasPrefix(m[3], "-") {
		last, _ := strconv.ParseInt(episodeToken.FindStringSubmatch(m[3])[1], 10, 64)
		if last <= first || last-first > 20 {
			return 0, nil, false
		}
		for e := first + 1; e <= last; e++ {
			episodes = append(episodes, e)
		}
	} else {
		for _, t := range episodeToken.FindAllStringSubmatch(m[3], -1) {
			e, _ := strconv.ParseInt(t[1], 10, 64)
			episodes = append(episodes, e)
		}
	}
	slices.Sort(episodes)
	if len(slices.Compact(slices.Clone(episodes))) != len(episodes) {
		return 0, nil, false
	}
	return season, episodes, true
}

// multiEpisode reports whether the present queue rows are one Sonarr
// release covering several episodes it names: every row is a matched row of
// the same manager and download, for a distinct episode of one series and
// season, and the release title names exactly those episodes. It returns
// the rows ordered by episode number and a label such as "S03E01–E02", or
// why the rows are not such a release.
func multiEpisode(rows []store.QueueRecord, mgr adapter.Kind) ([]store.QueueRecord, string, string) {
	if mgr != adapter.KindSonarr {
		return nil, "", "only Sonarr releases cover several episodes"
	}
	out := slices.Clone(rows)
	seen := map[int64]bool{}
	var nums []int64
	r0 := out[0].Latest
	season, sok := r0.SeasonNumber.Get()
	for _, q := range out {
		it := q.Latest
		s, ok := it.SeasonNumber.Get()
		n, nok := it.EpisodeNumber.Get()
		switch {
		case q.MatchStatus != store.MatchMatched:
			return nil, "", fmt.Sprintf("queue item %d is not exactly correlated with this transfer", q.QueueID)
		case it.ContentKind != "episode" || it.EpisodeID <= 0 || it.SeriesID <= 0 || !ok || !nok || !sok:
			return nil, "", fmt.Sprintf("queue item %d does not report its series, season and episode", q.QueueID)
		case it.SeriesID != r0.SeriesID || s != season:
			return nil, "", "the rows belong to different series or seasons"
		case seen[it.EpisodeID]:
			return nil, "", fmt.Sprintf("episode %d appears in more than one row", it.EpisodeID)
		}
		seen[it.EpisodeID] = true
		nums = append(nums, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := out[i].Latest.EpisodeNumber.Get()
		b, _ := out[j].Latest.EpisodeNumber.Get()
		return a < b
	})
	slices.Sort(nums)
	ts, teps, ok := parseMultiEpisode(r0.Title)
	if !ok {
		return nil, "", fmt.Sprintf("the release title %q does not name its episodes (a season pack or an unrecognised naming)", r0.Title)
	}
	if ts != season || !slices.Equal(teps, nums) {
		return nil, "", fmt.Sprintf("the release title %q names S%02d episodes %v, but the manager's rows are for S%02d episodes %v", r0.Title, ts, teps, season, nums)
	}
	label := fmt.Sprintf("S%02dE%02d–E%02d", season, nums[0], nums[len(nums)-1])
	if len(nums) > 2 {
		var parts []string
		for _, n := range nums {
			parts = append(parts, fmt.Sprintf("E%02d", n))
		}
		label = fmt.Sprintf("S%02d", season) + strings.Join(parts, "")
	}
	return out, label, ""
}

// multiEpisodeTested filters the registry to combinations whose
// multi-episode recovery passed released-instance tests.
func multiEpisodeTested(tested []TestedWorkflow) []TestedWorkflow {
	var out []TestedWorkflow
	for _, t := range tested {
		if t.MultiEpisode {
			out = append(out, t)
		}
	}
	return out
}
