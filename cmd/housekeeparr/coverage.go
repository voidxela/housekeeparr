package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/voidxela/housekeeparr/internal/app"
)

func cmdCoverage(args []string, out, errOut io.Writer) int {
	o := newFlags("coverage", errOut)
	manager := o.fs.String("manager", "", "only this manager")
	gaps := o.fs.Int("gaps", 20, "list this many gaps per manager (0 for none)")
	offset := o.fs.Int("offset", 0, "skip this many gaps (most recently aired first)")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	cv, err := svc.Coverage(context.Background(), *manager, *offset, *gaps)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, cv)
	}
	if len(cv.Managers) == 0 {
		fmt.Fprintln(out, "No coverage read yet: the first sweep starts with the next observation cycle of a readable Sonarr or Radarr.")
		return exitOK
	}
	for _, n := range cv.Notes {
		fmt.Fprintln(out, n)
	}
	for _, m := range cv.Managers {
		fmt.Fprintf(out, "\n%s: %d of %d have a file (%.1f%%), %d gaps.", m.Name, m.Have, m.Wanted, m.Percent, m.GapsTotal)
		if m.DenominatorChange != nil {
			fmt.Fprintf(out, " Denominator %+d since the previous day.", *m.DenominatorChange)
		}
		fmt.Fprintln(out)
		switch {
		case m.Complete:
			fmt.Fprintf(out, "  Complete sweep; groups read between %s and %s.\n", ts(m.OldestRead), ts(m.NewestRead))
		default:
			fmt.Fprintf(out, "  INCOMPLETE: %d of %d groups read in this sweep; unread groups keep their previous counts or are not counted yet.\n", m.GroupsRead, m.Groups)
		}
		var parts []string
		for _, s := range app.GapStates {
			if n := m.States[s]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, app.GapLabel(s)))
			}
		}
		if len(parts) > 0 {
			fmt.Fprintf(out, "  Gaps: %s.\n", strings.Join(parts, ", "))
		}
		for _, d := range m.Trend {
			fmt.Fprintf(out, "  %s  %d / %d\n", d.Day, d.Have, d.Wanted)
		}
		if len(m.Gaps) > 0 {
			w := tw(out)
			fmt.Fprintln(w, "  ITEM\tSTATE\tSINCE\tWHY")
			for _, g := range m.Gaps {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", g.Label, app.GapLabel(g.State), ts(g.EvidenceAt), strings.Join(g.Reasons, "; "))
			}
			w.Flush()
		}
	}
	return exitOK
}
