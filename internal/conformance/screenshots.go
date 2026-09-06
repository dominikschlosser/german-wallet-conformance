package conformance

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func Screenshots(ctx context.Context, suite, outDir, chrome string, refs []PlanRef) error {
	if len(refs) == 0 {
		return fmt.Errorf("no plans found in run log")
	}
	groups := map[string][]string{}
	type status struct {
		Status string `json:"status"`
		Result string `json:"result"`
	}
	statuses := map[string]status{}
	var reviews []reviewScreenshot
	api := NewAPI(suite, os.Getenv("CONFORMANCE_TOKEN"))
	var order []string
	for _, ref := range refs {
		var plan Plan
		if err := api.JSON(ctx, "GET", "api/plan/"+ref.ID, nil, &plan); err != nil {
			return err
		}
		for _, m := range plan.Modules {
			if len(m.Instances) == 0 {
				continue
			}
			id := m.Instances[len(m.Instances)-1]
			var info ModuleInfo
			if err := api.JSON(ctx, "GET", "api/info/"+id, nil, &info); err != nil {
				return err
			}
			if !info.Terminal() {
				return fmt.Errorf("%s is still running; capture after completion", ref.Slug)
			}
			statuses[id] = status{info.Status, info.Result}
			if info.Status == "FINISHED" && info.Result == "REVIEW" {
				reviews = append(reviews, reviewScreenshot{ID: id, Variant: ref.Slug, Module: m.Name})
			}
		}
		parts := strings.Split(ref.Slug, "-")
		if len(parts) < 2 {
			return fmt.Errorf("unrecognized variant slug %q", ref.Slug)
		}
		group := strings.Join(parts[:2], "-")
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], ref.ID)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome), chromedp.Flag("ignore-certificate-errors", true))
	alloc, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()
	browser, cancel := chromedp.NewContext(alloc)
	defer cancel()
	browser, cancel = context.WithTimeout(browser, 3*time.Minute)
	defer cancel()
	if err := chromedp.Run(browser, chromedp.EmulateViewport(1400, 1000), chromedp.Navigate(suite+"/plans.html"), chromedp.Poll(`document.querySelector('cts-plan-list')?._plans?.length > 0`, nil), chromedp.Evaluate(`window.capturePlans = document.querySelector('cts-plan-list')._plans`, nil)); err != nil {
		return err
	}
	for _, group := range order {
		ids := groups[group]
		var count int
		// Populate the suite's real overview with the same API values its lazy
		// loader uses. Await both Lit renders so screenshots cannot capture grey
		// loading squares or a failed background status fetch.
		js := fmt.Sprintf(`(async () => {
		 const el=document.querySelector('cts-plan-list'), statuses=%s;
		 const plans=window.capturePlans.filter(p => %s.includes(p._id));
		 for(const p of plans) for(const m of p.modules) {
		   const info=statuses[m.instances?.at(-1)];
		   if(info) Object.assign(m, info, {_statusResolved:true});
		 }
		 el._plans=plans;el._visibleCount=plans.length;el._sortKey='started-asc';
		 await el.updateComplete;
		 await Promise.all([...el.querySelectorAll('cts-plan-status')].map(c=>c.updateComplete));
		 return el._plans.length;
		})()`, jsonString(statuses), jsonString(ids))
		if err := chromedp.Run(browser, chromedp.Evaluate(js, &count, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
			return err
		}
		if count != len(ids) {
			return fmt.Errorf("missing %s variants in suite database", group)
		}
		var png []byte
		if err := chromedp.Run(browser, chromedp.Poll(`document.fonts.status === 'loaded'`, nil), chromedp.Screenshot("cts-plan-list", &png, chromedp.ByQuery)); err != nil {
			return err
		}
		path := filepath.Join(outDir, group+".png")
		if err := os.WriteFile(path, png, 0644); err != nil {
			return err
		}
		fmt.Printf("%s: %d variant runs\n", path, count)
	}
	return saveReviewScreenshots(ctx, api, outDir, reviews)
}

type reviewScreenshot struct{ ID, Variant, Module string }

func saveReviewScreenshots(ctx context.Context, api *API, outDir string, reviews []reviewScreenshot) error {
	if len(reviews) == 0 {
		return nil
	}
	dir := filepath.Join(outDir, "review")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, review := range reviews {
		var images []struct {
			Data string `json:"img"`
		}
		if err := api.JSON(ctx, "GET", "api/log/"+review.ID+"/images", nil, &images); err != nil {
			return err
		}
		if len(images) == 0 {
			return fmt.Errorf("manual review has no screenshot: %s %s", review.Variant, review.Module)
		}
		for i, img := range images {
			media, encoded, ok := strings.Cut(img.Data, ",")
			ext := ".png"
			switch media {
			case "data:image/png;base64":
			case "data:image/jpeg;base64":
				ext = ".jpg"
			default:
				return fmt.Errorf("unsupported screenshot type %q for %s", media, review.ID)
			}
			if !ok {
				return fmt.Errorf("missing image data for %s", review.ID)
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return fmt.Errorf("decode screenshot for %s: %w", review.ID, err)
			}
			name := review.Variant + "-" + ShortName(review.Module)
			if len(images) > 1 {
				name += fmt.Sprintf("-%d", i+1)
			}
			if name != filepath.Base(name) {
				return fmt.Errorf("invalid screenshot name %q", name)
			}
			if err := os.WriteFile(filepath.Join(dir, name+ext), data, 0644); err != nil {
				return err
			}
		}
	}
	fmt.Printf("%s: screenshots for %d manual reviews\n", dir, len(reviews))
	return nil
}
