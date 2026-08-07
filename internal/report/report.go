// Package report renders check results for the terminal.
package report

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/sagargoswami2001/sentinel/internal/checker"
)

const (
	green  = "\033[32m"
	red    = "\033[31m"
	yellow = "\033[33m"
	reset  = "\033[0m"
)

// Print writes an aligned, colored summary table to stdout and
// returns the number of targets that are down.
func Print(results []checker.Result) int {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tNAME\tCODE\tLATENCY\tCERT\tDETAIL")

	down := 0
	for _, r := range results {
		status := green + "● UP" + reset
		detail := ""

		switch {
		case r.Err != nil:
			status = red + "● DOWN" + reset
			detail = r.Err.Error()
			down++
		case !r.Up:
			status = red + "● DOWN" + reset
			detail = fmt.Sprintf("expected status %d", r.Target.ExpectStatus)
			down++
		}

		code := "-"
		if r.StatusCode != 0 {
			code = fmt.Sprintf("%d", r.StatusCode)
		}

		cert := "-"
		switch {
		case r.CertDaysLeft < 0:
			// not HTTPS, leave as "-"
		case r.CertDaysLeft < 14:
			cert = fmt.Sprintf("%s%dd left!%s", yellow, r.CertDaysLeft, reset)
		default:
			cert = fmt.Sprintf("%dd", r.CertDaysLeft)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\t%s\n",
			status, r.Target.Name, code, r.Latency.Round(1e6), cert, detail)
	}

	w.Flush()
	fmt.Printf("\n%d/%d targets up\n", len(results)-down, len(results))
	return down
}
