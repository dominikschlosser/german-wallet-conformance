package conformance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrConformanceFailures = errors.New("suite recorded failures or incomplete tests; see the report")

type Wallet interface {
	Prepare(context.Context) error
	Submit(context.Context, string, string, string) (string, error)
	Screenshot(context.Context) ([]byte, error)
	Log(context.Context) ([]byte, error)
}
type Runner struct {
	API                                     *API
	Wallet                                  Wallet
	Logger                                  *log.Logger
	PollInterval, ModuleTimeout, ErrorGrace time.Duration
}
type RunOptions struct {
	Config                           ConfigOptions
	ResultsDir, Only, Exclude, Rerun string
	ResumeLog, Group                 string
	RerunDeferred                    bool
	ShardIndex, ShardCount           int
}

func NewRunner(api *API, wallet Wallet, logger *log.Logger) *Runner {
	return &Runner{api, wallet, logger, time.Second, 180 * time.Second, 45 * time.Second}
}
func (r *Runner) Run(ctx context.Context, o RunOptions) error {
	if o.ShardCount == 0 {
		o.ShardCount = 1
	}
	if o.ShardCount < 1 || o.ShardIndex < 0 || o.ShardIndex >= o.ShardCount {
		return fmt.Errorf("shard index must be between 0 and shard count minus one")
	}
	// Assign the full matrix first so filters and resume retain the same issuer.
	assignments := scenarioShards(o.Config.SDJWTConfiguration, o.ShardCount)
	scenarios := Scenarios(o.Config.SDJWTConfiguration, o.Only, o.Exclude)
	if len(scenarios) == 0 {
		return fmt.Errorf("no matching variant combinations")
	}
	selectors, err := ParseSelectors(o.Rerun, len(scenarios))
	if err != nil {
		return err
	}
	previous := map[string]string{}
	if o.ResumeLog != "" {
		data, err := os.ReadFile(o.ResumeLog)
		if err != nil {
			return err
		}
		for _, ref := range PlansOf(string(data)) {
			if id := previous[ref.Slug]; id != "" && id != ref.ID {
				return fmt.Errorf("multiple plans for %s in resume log", ref.Slug)
			}
			previous[ref.Slug] = ref.ID
		}
	}
	if o.Group != "" && o.Group != "vci-sdjwt" && o.Group != "vci-mdoc" && o.Group != "vp-sdjwt" && o.Group != "vp-mdoc" {
		return fmt.Errorf("invalid test group %q", o.Group)
	}
	if err = os.MkdirAll(o.ResultsDir, 0700); err != nil {
		return err
	}
	failed := false
	for index, s := range scenarios {
		if o.Group != "" && s.Kind+"-"+s.Credential != o.Group {
			continue
		}
		if assignments[s.Slug] != o.ShardIndex {
			continue
		}
		filter, selected := selectors[index+1]
		if len(selectors) > 0 && !selected {
			continue
		}
		if o.RerunDeferred && (s.Kind != "vci" || (!s.HAIP && s.Variant["vci_credential_issuance_mode"] != "deferred")) {
			continue
		}
		var cfg Config
		path := filepath.Join(o.ResultsDir, s.Slug+"-config.json")
		r.Logger.Printf("Running plan '%s' with configuration file '%s'", s.Name, path)
		var plan Plan
		query := url.Values{"planName": {s.Name}, "variant": {jsonString(s.Variant)}}
		if id := previous[s.Slug]; id != "" {
			if err = r.API.JSON(ctx, "GET", "api/plan/"+id, nil, &plan); err != nil {
				return err
			}
			plan.ID = id
			if plan.Name != s.Name || !maps.Equal(plan.Variant, s.Variant) {
				return fmt.Errorf("resume plan %s does not match %s", id, s.Slug)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(data, &cfg); err != nil {
				return err
			}
		} else {
			var e error
			cfg, e = BuildConfig(s, o.Config)
			if e != nil {
				return e
			}
			body, e := json.Marshal(cfg)
			if e != nil {
				return e
			}
			if err = WriteConfig(path, cfg); err != nil {
				return err
			}
			if err = r.API.JSON(ctx, "POST", "api/plan?"+query.Encode(), body, &plan); err != nil {
				return err
			}
		}
		if plan.ID == "" {
			return fmt.Errorf("suite returned a plan without an id")
		}
		r.Logger.Printf("%s/plan-detail.html?plan=%s", r.API.BaseURL, plan.ID)
		var modules []Module
		for _, m := range plan.Modules {
			if ModuleApplies(s, m) {
				modules = append(modules, m)
			}
		}
		if len(modules) == 0 {
			return fmt.Errorf("plan %s has no applicable modules", plan.ID)
		}
		for i := range filter {
			if i < 1 || i > len(modules) {
				return fmt.Errorf("module %d is outside plan %d (1..%d)", i, index+1, len(modules))
			}
		}
		for i, m := range modules {
			if len(filter) > 0 && !filter[i+1] {
				continue
			}
			mode, specified := m.Variant["vci_credential_issuance_mode"]
			if !specified {
				mode = s.Variant["vci_credential_issuance_mode"]
			}
			if o.RerunDeferred && mode != "deferred" {
				continue
			}
			var info ModuleInfo
			var e error
			repeat := o.RerunDeferred || len(selectors) > 0
			if len(m.Instances) > 0 {
				info, e = r.ResumeModule(ctx, m)
				if e != nil {
					return e
				}
			}
			if len(m.Instances) == 0 || repeat {
				if s.Kind == "vci" && cfg.Alias != o.Config.VCIAlias {
					return fmt.Errorf("resuming %s requires the app issuer alias %s", s.Slug, cfg.Alias)
				}
				info, e = r.RunModule(ctx, plan.ID, m, filepath.Join(o.ResultsDir, "wallet-logs"))
			}
			if e != nil {
				return e
			}
			r.Logger.Printf("%s: %s (%s)", m.Name, info.Verdict(), info.Status)
			if info.Status == "INTERRUPTED" || (info.Verdict() != "PASSED" && info.Verdict() != "REVIEW" && info.Verdict() != "SKIPPED") {
				failed = true
			}
		}
		data, e := r.API.Request(ctx, "GET", "api/plan/export/"+plan.ID, "", nil)
		if e != nil {
			return e
		}
		archive, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if e != nil {
			return fmt.Errorf("invalid suite export: %w", e)
		}
		for _, entry := range archive.File {
			reader, e := entry.Open()
			if e != nil {
				return e
			}
			_, e = io.Copy(io.Discard, reader)
			reader.Close()
			if e != nil {
				return e
			}
		}
		exportPath := filepath.Join(o.ResultsDir, s.Slug+"-"+plan.ID+".zip")
		if err = os.WriteFile(exportPath, data, 0600); err != nil {
			return err
		}
		r.Logger.Printf("results saved to %q", exportPath)
	}
	if failed {
		return ErrConformanceFailures
	}
	return nil
}

func scenarioShards(sdjwtConfiguration string, count int) map[string]int {
	assignments := map[string]int{}
	next := map[string]int{}
	for _, scenario := range Scenarios(sdjwtConfiguration, "", "") {
		group := scenario.Kind + "-" + scenario.Credential
		assignments[scenario.Slug] = next[group] % count
		next[group]++
	}
	return assignments
}

// Resume observes the existing instance without opening its request a second time.
// An abandoned interaction is captured and stopped in the same instance.
func (r *Runner) ResumeModule(ctx context.Context, m Module) (ModuleInfo, error) {
	id := m.Instances[len(m.Instances)-1]
	started := time.Now()
	cancelled := false
	for {
		var info ModuleInfo
		if err := r.API.JSON(ctx, "GET", "api/info/"+id, nil, &info); err != nil {
			return info, err
		}
		if info.Terminal() {
			r.Logger.Printf("Preserved existing module %s: %s", id, info.Verdict())
			return info, nil
		}
		if !cancelled && time.Since(started) > r.ErrorGrace {
			if err := r.cancel(ctx, id); err != nil {
				return info, err
			}
			cancelled = true
		}
		if time.Since(started) > r.ErrorGrace+30*time.Second {
			return info, fmt.Errorf("existing module %s did not stop", id)
		}
		if err := pause(ctx, r.PollInterval); err != nil {
			return info, err
		}
	}
}
func jsonString(v any) string { data, _ := json.Marshal(v); return string(data) }
func ParseSelectors(raw string, plans int) (map[int]map[int]bool, error) {
	result := map[int]map[int]bool{}
	if raw == "" {
		return result, nil
	}
	for _, part := range strings.Split(raw, ",") {
		nums := strings.Split(part, ":")
		if len(nums) > 2 {
			return nil, fmt.Errorf("invalid rerun selector %q", part)
		}
		plan, err := strconv.Atoi(nums[0])
		if err != nil || plan < 1 || plan > plans {
			return nil, fmt.Errorf("plan selector %q is outside 1..%d", part, plans)
		}
		if len(nums) == 1 {
			result[plan] = nil
			continue
		}
		module, err := strconv.Atoi(nums[1])
		if err != nil || module < 1 {
			return nil, fmt.Errorf("invalid module selector %q", part)
		}
		if previous, ok := result[plan]; ok && previous == nil {
			continue
		}
		if result[plan] == nil {
			result[plan] = map[int]bool{}
		}
		result[plan][module] = true
	}
	return result, nil
}
func (r *Runner) RunModule(ctx context.Context, planID string, m Module, logDir string) (result ModuleInfo, runErr error) {
	if err := r.Wallet.Prepare(ctx); err != nil {
		return ModuleInfo{}, fmt.Errorf("prepare wallet: %w", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	query := url.Values{"test": {m.Name}, "plan": {planID}, "variant": {jsonString(m.Variant)}}
	if err := r.API.JSON(ctx, "POST", "api/runner?"+query.Encode(), nil, &created); err != nil {
		return ModuleInfo{}, err
	}
	if created.ID == "" {
		return ModuleInfo{}, fmt.Errorf("suite returned a module without an id")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		data, err := r.Wallet.Log(ctx)
		if err == nil {
			err = writeWalletLog(logDir, created.ID, data)
		}
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("capture wallet log for %s: %w", created.ID, err))
		}
	}()
	r.Logger.Printf("Created test module, new id: %s", created.ID)
	submitted, uploaded := map[string]bool{}, map[string]bool{}
	started := time.Now()
	var appErrorAt time.Time
	lastStatus := ""
	cancelled := false
	for {
		var info ModuleInfo
		if err := r.API.JSON(ctx, "GET", "api/info/"+created.ID, nil, &info); err != nil {
			return info, err
		}
		info.ID = created.ID
		if info.Status != lastStatus {
			r.Logger.Printf("module id %s status changed to %s", created.ID, info.Status)
			lastStatus = info.Status
		}
		if info.Terminal() {
			return info, nil
		}
		if !cancelled && (time.Since(started) > r.ModuleTimeout || (!appErrorAt.IsZero() && time.Since(appErrorAt) > r.ErrorGrace)) {
			if err := r.cancel(ctx, created.ID); err != nil {
				return info, err
			}
			cancelled = true
		}
		if cancelled {
			if time.Since(started) > r.ModuleTimeout+30*time.Second {
				return info, fmt.Errorf("module %s did not stop after cancellation", created.ID)
			}
			if err := pause(ctx, r.PollInterval); err != nil {
				return info, err
			}
			continue
		}
		var entries []LogEntry
		if err := r.API.JSON(ctx, "GET", "api/log/"+created.ID, nil, &entries); err != nil {
			return info, err
		}
		var requests []string
		for _, entry := range entries {
			if entry.Redirect != "" {
				requests = append(requests, entry.Redirect)
			}
			if entry.OfferURL != "" {
				requests = append(requests, entry.OfferURL)
			}
		}
		for _, request := range requests {
			if submitted[request] {
				continue
			}
			firstInteraction := len(submitted) == 0
			submitted[request] = true
			txCode, err := r.TransactionCode(ctx, request)
			if err != nil {
				return info, err
			}
			appError, err := r.Wallet.Submit(ctx, request, m.Name, txCode)
			if err != nil {
				return info, err
			}
			if firstInteraction {
				started = time.Now()
			}
			if appError != "" {
				r.Logger.Printf("[wallet] %s: %s", m.Name, appError)
				appErrorAt = time.Now()
				if strings.HasPrefix(appError, "[final]") {
					appErrorAt = appErrorAt.Add(-r.ErrorGrace - time.Second)
				}
			}
		}
		// Some negative tests request a screenshot before the wallet opens the
		// request. Capture after the interaction so the image shows its response.
		for _, entry := range entries {
			if entry.Upload != "" && !uploaded[entry.Upload] && len(submitted) > 0 {
				png, err := r.Wallet.Screenshot(ctx)
				if err != nil {
					return info, err
				}
				if _, err = r.API.Request(ctx, "POST", "api/log/"+created.ID+"/images/"+url.PathEscape(entry.Upload), "text/plain;charset=utf-8", imageDataURL(png)); err != nil {
					return info, err
				}
				uploaded[entry.Upload] = true
			}
		}
		if err := pause(ctx, r.PollInterval); err != nil {
			return info, err
		}
	}
}
func (r *Runner) cancel(ctx context.Context, id string) error {
	png, err := r.Wallet.Screenshot(ctx)
	if err != nil {
		return err
	}
	if _, err = r.API.Request(ctx, "POST", "api/log/"+id+"/images?description=Wallet+screen+before+cancellation", "text/plain;charset=utf-8", imageDataURL(png)); err != nil {
		return err
	}
	r.Logger.Printf("[monitor] cancelling incomplete module %s after capturing its screen", id)
	return r.API.JSON(ctx, "DELETE", "api/runner/"+id, nil, nil)
}

type Offer struct {
	Issuer           string           `json:"credential_issuer"`
	ConfigurationIDs []string         `json:"credential_configuration_ids"`
	Grants           map[string]Grant `json:"grants,omitempty"`
}
type Grant struct {
	TxCode *TransactionCode `json:"tx_code,omitempty"`
}
type TransactionCode struct {
	Description string `json:"description"`
	Length      int    `json:"length"`
}

var transactionCodePattern = regexp.MustCompile(`<(\d{4,12})>`)

func (r *Runner) TransactionCode(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	var offer Offer
	if value := q.Get("credential_offer"); value != "" {
		if err = json.Unmarshal([]byte(value), &offer); err != nil {
			return "", err
		}
	} else if ref := q.Get("credential_offer_uri"); ref != "" {
		if err = r.API.JSON(ctx, "GET", ref, nil, &offer); err != nil {
			return "", err
		}
	} else {
		return "", nil
	}
	tx := offer.Grants["urn:ietf:params:oauth:grant-type:pre-authorized_code"].TxCode
	if tx == nil {
		return "", nil
	}
	if match := transactionCodePattern.FindStringSubmatch(tx.Description); match != nil {
		return match[1], nil
	}
	if tx.Length > 0 && tx.Length <= 12 {
		return strings.Repeat("0", tx.Length), nil
	}
	return "", nil
}
