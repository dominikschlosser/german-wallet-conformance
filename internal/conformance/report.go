package conformance

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type PlanRef struct{ Slug, ID string }

func SelectPlans(refs []PlanRef, only string) []PlanRef {
	var selected []PlanRef
	for _, ref := range refs {
		if strings.HasSuffix(ref.Slug, "-wallet-initiated") {
			continue
		}
		if only == "" || containsAny(ref.Slug, only) {
			selected = append(selected, ref)
		}
	}
	return selected
}

var configLine = regexp.MustCompile(`Running plan '.*?' with configuration file '.*?/results/(.+?)-config\.json'`)
var planLink = regexp.MustCompile(`[?&]plan=([A-Za-z0-9]+)`)

func PlansOf(text string) []PlanRef {
	var refs []PlanRef
	slug := ""
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if m := configLine.FindStringSubmatch(line); m != nil {
			slug = m[1]
			continue
		}
		if m := planLink.FindStringSubmatch(line); m != nil && slug != "" && !seen[m[1]] {
			refs = append(refs, PlanRef{slug, m[1]})
			seen[m[1]] = true
		}
	}
	return refs
}
func ShortName(name string) string {
	for _, p := range []string{"oid4vp-1final-wallet-", "oid4vci-1_0-wallet-", "fapi2-security-profile-final-client-test-"} {
		name = strings.TrimPrefix(name, p)
	}
	return name
}
func cell(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", "|", "&#124;").Replace(strings.Join(strings.Fields(s), " "))
}

var VerdictColumns = []string{"PASSED", "FAILED", "WARNING", "REVIEW", "INTERRUPTED", "SKIPPED", "NOT RUN", "OTHER"}

type ReportModule struct{ Name, Verdict, Detail string }
type ReportPlan struct {
	Ref     PlanRef
	Name    string
	Modules []ReportModule
}

func ReadReport(ctx context.Context, api *API, refs []PlanRef, details bool) ([]ReportPlan, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("no plans found in run log")
	}
	var report []ReportPlan
	for _, ref := range refs {
		var plan Plan
		if err := api.JSON(ctx, "GET", "api/plan/"+ref.ID, nil, &plan); err != nil {
			return nil, err
		}
		r := ReportPlan{Ref: ref, Name: plan.Name}
		for _, module := range plan.Modules {
			m := ReportModule{Name: ShortName(module.Name), Verdict: "NOT RUN"}
			if module.Variant["vci_credential_issuance_mode"] == "deferred" {
				m.Name += " (deferred)"
			}
			if len(module.Instances) > 0 {
				id := module.Instances[len(module.Instances)-1]
				var info ModuleInfo
				if err := api.JSON(ctx, "GET", "api/info/"+id, nil, &info); err != nil {
					return nil, err
				}
				m.Verdict = info.Verdict()
				if details && (m.Verdict == "FAILED" || m.Verdict == "INTERRUPTED") {
					var entries []LogEntry
					if err := api.JSON(ctx, "GET", "api/log/"+id, nil, &entries); err != nil {
						return nil, err
					}
					for _, entry := range entries {
						if entry.Result == "FAILURE" {
							m.Detail = entry.Message
							break
						}
					}
				}
			}
			r.Modules = append(r.Modules, m)
		}
		report = append(report, r)
	}
	return report, nil
}
func WriteReport(w io.Writer, suite string, plans []ReportPlan, details bool) {
	names := map[string]bool{}
	total := 0
	for _, p := range plans {
		names[p.Name] = true
		total += len(p.Modules)
	}
	fmt.Fprintf(w, "%d test plans, %d variant runs, %d module slots. Counts use each module's latest instance.\n\n", len(names), len(plans), total)
	fmt.Fprintln(w, "| Variant run | Passed | Failed | Warning | Review | Interrupted | Skipped | Not run | Other |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, p := range plans {
		counts := map[string]int{}
		for _, m := range p.Modules {
			known := false
			for _, v := range VerdictColumns {
				if m.Verdict == v {
					known = true
					break
				}
			}
			v := m.Verdict
			if !known {
				v = "OTHER"
			}
			counts[v]++
		}
		fmt.Fprintf(w, "| [%s](%s/plan-detail.html?plan=%s) |", cell(p.Ref.Slug), suite, p.Ref.ID)
		for _, v := range VerdictColumns {
			fmt.Fprintf(w, " %d |", counts[v])
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "\nReview needs manual assessment. Interrupted and Not run are not passes. Other includes unfinished or unknown results.")
	if details {
		for _, p := range plans {
			fmt.Fprintf(w, "\n## %s\n\n| Module | Result | First failure |\n| --- | --- | --- |\n", cell(p.Ref.Slug))
			for _, m := range p.Modules {
				fmt.Fprintf(w, "| %s | %s | %s |\n", cell(m.Name), m.Verdict, cell(m.Detail))
			}
		}
	}
}
