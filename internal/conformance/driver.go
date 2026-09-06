package conformance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"image/png"
	"log"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Frame struct{ X, Y, Width, Height float64 }
type Element struct {
	Label   string `json:"AXLabel"`
	Value   string `json:"AXValue"`
	Title   string `json:"title"`
	Kind    string `json:"type"`
	Frame   Frame  `json:"frame"`
	Enabled bool   `json:"enabled"`
}
type UIFlow struct {
	Accept, Cancel, Home, PIN, Toggle, Recover, Error, Dismiss []string
	SettleSeconds                                              float64 `json:"settle_seconds"`
	TimeoutSeconds                                             float64 `json:"timeout_seconds"`
}

func DefaultFlow() UIFlow {
	return UIFlow{
		Accept:  []string{"Open", "Öffnen", "Teilen", "Freigeben", "Daten teilen", "Share", "Share data", "Hinzufügen", "Nachweis hinzufügen", "Add", "Add document", "Speichern", "Save", "Annehmen", "Akzeptieren", "Zustimmen", "Accept", "Bestätigen", "Confirm", "Weiter", "Fortfahren", "Continue", "Next", "Erlauben", "Allow", "Enter transaction code", "Transaktionscode eingeben", "Add credential", "Fertig", "Done", "Schließen", "Close", "OK", "Ok", "Verstanden", "Got it", "Zur Übersicht", "Zurück zur Übersicht", "Back to overview"},
		Cancel:  []string{"Abbrechen", "Cancel", "Ablehnen", "Decline", "Nicht erlauben", "Don't Allow"},
		Home:    []string{"burger-menu", "Meine Nachweise", "Nachweise", "Übersicht", "My documents", "Documents", "Dashboard", "Wallet"},
		Recover: []string{"Try again", "Erneut versuchen"},
		Error:   []string{"This credential cannot be added", "Dieser Nachweis kann nicht hinzugefügt werden", "Server is currently unavailable", "Oops, something went wrong", "Something went wrong", "Etwas ist schiefgelaufen", "Please restart the presentation", "cannot be shared", "kann nicht geteilt werden"},
		Dismiss: []string{"Close screen and return to overview", "Close", "Schließen", "Zur Übersicht", "Reject", "Ablehnen"},
		Toggle:  []string{"checkbox_unselected"}, PIN: []string{"PIN", "Wallet-PIN", "Gib deine PIN ein", "Enter your PIN", "PIN eingeben"}, SettleSeconds: 6, TimeoutSeconds: 120,
	}
}

type Driver struct {
	UDID, BundleID, PIN string
	Flow                UIFlow
	Logger              *log.Logger
	onboarded           bool
	needsRelaunch       bool
	lastLabels          string
}

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(e.Stderr)))
		}
		return nil, err
	}
	return out, nil
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func NewDriver(ctx context.Context, udid, bundle, pin, flowPath string, logger *log.Logger) (*Driver, error) {
	if udid == "booted" {
		data, err := command(ctx, "xcrun", "simctl", "list", "devices", "booted", "-j")
		if err != nil {
			return nil, err
		}
		var result struct {
			Devices map[string][]struct{ UDID, State string }
		}
		if err = json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		var ids []string
		for _, devices := range result.Devices {
			for _, device := range devices {
				if device.State == "Booted" {
					ids = append(ids, device.UDID)
				}
			}
		}
		if len(ids) != 1 {
			return nil, fmt.Errorf("found %d booted simulators; set DE_WALLET_IOS_UDID", len(ids))
		}
		udid = ids[0]
	}
	flow := DefaultFlow()
	if flowPath != "" {
		data, err := os.ReadFile(flowPath)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &flow); err != nil {
			return nil, err
		}
	}
	if flow.SettleSeconds <= 0 || flow.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("UI timing values must be positive")
	}
	return &Driver{UDID: udid, BundleID: bundle, PIN: pin, Flow: flow, Logger: logger}, nil
}
func (d *Driver) simctl(ctx context.Context, args ...string) ([]byte, error) {
	return command(ctx, "xcrun", append([]string{"simctl"}, args...)...)
}
func (d *Driver) idb(ctx context.Context, args ...string) ([]byte, error) {
	return command(ctx, "idb", append(args, "--udid", d.UDID)...)
}
func (d *Driver) log(format string, args ...any) { d.Logger.Printf("[de-wallet-ios] "+format, args...) }
func (d *Driver) Elements(ctx context.Context) ([]Element, error) {
	data, err := d.idb(ctx, "ui", "describe-all", "--json")
	if err != nil {
		return nil, err
	}
	var elements []Element
	if err = json.Unmarshal(data, &elements); err != nil {
		return nil, fmt.Errorf("decode idb accessibility tree: %w", err)
	}
	for i := range elements {
		e := &elements[i]
		e.Label = strings.TrimSpace(e.Label)
		if e.Label == "" {
			e.Label = e.Value
		}
		if e.Label == "" {
			e.Label = e.Title
		}
	}
	return elements, nil
}
func FindElement(elements []Element, labels []string, kinds ...string) *Element {
	for _, wanted := range labels {
		for i := range elements {
			e := &elements[i]
			if !e.Enabled || e.Frame.Width <= 0 || e.Frame.Height <= 0 || !strings.EqualFold(e.Label, wanted) {
				continue
			}
			if len(kinds) == 0 || e.Kind == "" {
				return e
			}
			for _, kind := range kinds {
				if strings.Contains(strings.ToLower(e.Kind), strings.ToLower(kind)) {
					return e
				}
			}
		}
	}
	return nil
}
func (d *Driver) tap(ctx context.Context, e Element) error {
	_, err := d.idb(ctx, "ui", "tap", strconv.Itoa(int(e.Frame.X+e.Frame.Width/2)), strconv.Itoa(int(e.Frame.Y+e.Frame.Height/2)))
	return err
}
func labelsOf(elements []Element) []string {
	var labels []string
	for _, e := range elements {
		if e.Label != "" {
			labels = append(labels, e.Label)
		}
	}
	return labels
}
func (d *Driver) logLabels(elements []Element) {
	labels := labelsOf(elements)
	if len(labels) > 40 {
		labels = labels[:40]
	}
	line := strings.Join(labels, " | ")
	if line != d.lastLabels {
		d.lastLabels = line
		d.log("on screen: %s", line)
	}
}
func (d *Driver) errorOnScreen(elements []Element) bool {
	for _, e := range elements {
		for _, marker := range d.Flow.Error {
			if strings.Contains(strings.ToLower(e.Label), strings.ToLower(marker)) {
				return true
			}
		}
	}
	return false
}
func transactionScreen(elements []Element) bool {
	for _, e := range elements {
		s := strings.ToLower(e.Label)
		if strings.Contains(s, "transaction code") || strings.Contains(s, "transaktionscode") {
			return true
		}
	}
	return false
}
func (d *Driver) enterCode(ctx context.Context, elements []Element, code string) (bool, error) {
	digits := map[rune]Element{}
	for _, e := range elements {
		if len(e.Label) == 1 && e.Label[0] >= '0' && e.Label[0] <= '9' && strings.Contains(strings.ToLower(e.Kind), "button") {
			digits[rune(e.Label[0])] = e
		}
	}
	numeric := true
	for _, r := range code {
		if r < '0' || r > '9' {
			numeric = false
		}
	}
	if len(digits) == 10 && numeric {
		for _, r := range code {
			if err := d.tap(ctx, digits[r]); err != nil {
				return false, err
			}
			if err := pause(ctx, 150*time.Millisecond); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	for _, e := range elements {
		if strings.Contains(strings.ToLower(e.Kind), "textfield") {
			if err := d.tap(ctx, e); err != nil {
				return false, err
			}
			if err := pause(ctx, 300*time.Millisecond); err != nil {
				return false, err
			}
			_, err := d.idb(ctx, "ui", "text", code)
			return err == nil, err
		}
	}
	return false, nil
}
func (d *Driver) relaunch(ctx context.Context) error {
	_, _ = d.simctl(ctx, "terminate", d.UDID, d.BundleID)
	if err := pause(ctx, time.Second); err != nil {
		return err
	}
	return d.launch(ctx)
}

func (d *Driver) launch(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute)
	waiting := false
	for {
		_, err := d.simctl(ctx, "launch", d.UDID, d.BundleID)
		if err == nil || !strings.Contains(err.Error(), "FBSOpenApplicationServiceErrorDomain") || time.Now().After(deadline) {
			return err
		}
		if !waiting {
			d.log("waiting for simulator app registration after boot")
			waiting = true
		}
		if err := pause(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}
func (d *Driver) Recover(ctx context.Context) error {
	if err := d.launch(ctx); err != nil {
		return err
	}
	if err := pause(ctx, time.Second); err != nil {
		return err
	}
	for range 3 {
		elements, err := d.Elements(ctx)
		if err != nil {
			return err
		}
		hasButtons := false
		for _, e := range elements {
			if strings.Contains(strings.ToLower(e.Kind), "button") {
				hasButtons = true
			}
		}
		if !hasButtons {
			d.log("relaunching to close a leftover sign-in sheet")
			if err = d.relaunch(ctx); err != nil {
				return err
			}
			if err = pause(ctx, 3*time.Second); err != nil {
				return err
			}
			continue
		}
		labels := d.Flow.Recover
		if d.errorOnScreen(elements) {
			labels = d.Flow.Dismiss
		}
		target := FindElement(elements, labels)
		if target == nil {
			return nil
		}
		if err = d.tap(ctx, *target); err != nil {
			return err
		}
		if err = pause(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return nil
}
func (d *Driver) Automate(ctx context.Context, purpose, txCode string) (string, error) {
	deadline := time.Now().Add(time.Duration(d.Flow.TimeoutSeconds * float64(time.Second)))
	lastAction := time.Now()
	acted := false
	lastTap := ""
	deadTaps := 0
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		elements, err := d.Elements(ctx)
		if err != nil {
			return "", err
		}
		d.logLabels(elements)
		labels := labelsOf(elements)
		for _, e := range elements {
			if e.Kind == "Application" && e.Label == "Safari" {
				d.log("%s: verifier redirect is open", purpose)
				return "", pause(ctx, time.Duration(d.Flow.SettleSeconds*float64(time.Second)))
			}
		}
		if d.errorOnScreen(elements) {
			return strings.Join(labels, " | "), nil
		}
		if acted && time.Since(lastAction) > 2*time.Second {
			for _, home := range d.Flow.Home {
				if slices.Contains(labels, home) {
					return "", nil
				}
			}
		}
		txScreen := transactionScreen(elements)
		if txScreen && txCode != "" {
			entered, e := d.enterCode(ctx, elements, txCode)
			if e != nil {
				return "", e
			}
			if entered {
				txCode = ""
				acted = true
				lastAction = time.Now()
				if e = pause(ctx, time.Second); e != nil {
					return "", e
				}
				continue
			}
		}
		onPIN := false
		for _, pinLabel := range d.Flow.PIN {
			if slices.Contains(labels, pinLabel) {
				onPIN = true
			}
		}
		if !txScreen && onPIN {
			entered, e := d.enterCode(ctx, elements, d.PIN)
			if e != nil {
				return "", e
			}
			if entered {
				d.log("entered wallet PIN")
				acted = true
				lastAction = time.Now()
				if e = pause(ctx, 1500*time.Millisecond); e != nil {
					return "", e
				}
				continue
			}
		}
		target := FindElement(elements, d.Flow.Toggle)
		if target == nil {
			target = FindElement(elements, d.Flow.Accept, "Button", "Cell", "Link")
			if target == nil {
				target = FindElement(elements, d.Flow.Accept)
			}
		}
		if target != nil {
			signature := target.Label + "\n" + strings.Join(labels, "\n")
			if signature == lastTap {
				deadTaps++
			} else {
				deadTaps = 0
				lastTap = signature
			}
			if deadTaps >= 3 {
				d.needsRelaunch = true
				return "[final] no screen change after repeated taps on " + target.Label + ": " + strings.Join(labels, " | "), nil
			}
			if err = d.tap(ctx, *target); err != nil {
				return "", err
			}
			d.log("tapped %s %q", target.Kind, target.Label)
			acted = true
			lastAction = time.Now()
			if err = pause(ctx, 1200*time.Millisecond); err != nil {
				return "", err
			}
			continue
		}
		// A blank accessibility tree can precede the consent screen while the
		// request loads. Keep waiting until the app displays content.
		hasContent := slices.ContainsFunc(elements, func(e Element) bool { return e.Kind != "Application" && e.Label != "" })
		if hasContent && time.Since(lastAction) > time.Duration(d.Flow.SettleSeconds*float64(time.Second)) {
			return "", nil
		}
		if err = pause(ctx, 800*time.Millisecond); err != nil {
			return "", err
		}
	}
	return "app flow timed out: " + purpose, nil
}
func AppURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return raw, nil
	}
	q := u.Query()
	if q.Has("credential_offer") || q.Has("credential_offer_uri") {
		return "openid-credential-offer://?" + u.RawQuery, nil
	}
	if q.Has("client_id") || q.Has("request_uri") || q.Has("request") {
		return "openid4vp://?" + u.RawQuery, nil
	}
	return raw, nil
}
func (d *Driver) Submit(ctx context.Context, raw, purpose, txCode string) (string, error) {
	if d.needsRelaunch {
		if err := d.relaunch(ctx); err != nil {
			return "", err
		}
		d.needsRelaunch = false
	}
	if !d.onboarded {
		if err := d.launch(ctx); err != nil {
			return "", err
		}
		if err := pause(ctx, 5*time.Second); err != nil {
			return "", err
		}
		message, err := d.Automate(ctx, "onboarding", "")
		if err != nil || message != "" {
			return message, err
		}
		d.onboarded = true
	}
	if err := d.Recover(ctx); err != nil {
		return "", err
	}
	appURL, err := AppURL(raw)
	if err != nil {
		return "", err
	}
	// Safari otherwise reuses a previous redirect differing only in its fragment.
	_, _ = d.simctl(ctx, "terminate", d.UDID, "com.apple.mobilesafari")
	if _, err = d.simctl(ctx, "openurl", d.UDID, appURL); err != nil {
		return "", err
	}
	d.log("opened request for %s", purpose)
	if err = pause(ctx, 1500*time.Millisecond); err != nil {
		return "", err
	}
	return d.Automate(ctx, purpose, txCode)
}

// Stop the app and browser before creating a test. This clears issuer metadata
// and cancels old browser callbacks. Launch when the suite issuer exists.
func (d *Driver) Prepare(ctx context.Context) error {
	for _, bundle := range []string{d.BundleID, "com.apple.mobilesafari"} {
		if _, err := d.simctl(ctx, "terminate", d.UDID, bundle); err != nil && !strings.Contains(err.Error(), "found nothing to terminate") {
			return err
		}
	}
	d.needsRelaunch = false
	d.onboarded = false
	return ctx.Err()
}

func (d *Driver) Shutdown() error {
	_, err := d.simctl(context.Background(), "shutdown", d.UDID)
	return err
}

func (d *Driver) Screenshot(ctx context.Context) ([]byte, error) {
	f, err := os.CreateTemp("", "wallet-screen-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if _, err = d.simctl(ctx, "io", d.UDID, "screenshot", path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return suiteImage(data)
}

// The suite accepts at most 500 KiB per image. Keep small PNGs lossless.
func suiteImage(data []byte) ([]byte, error) {
	if len(data) <= 500*1024 {
		return data, nil
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for _, quality := range []int{85, 65, 45, 25, 5} {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if out.Len() <= 500*1024 {
			return out.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("simulator screenshot exceeds the suite's 500 KiB image limit")
}
func imageDataURL(png []byte) []byte {
	if bytes.HasPrefix(png, []byte{0xff, 0xd8, 0xff}) {
		return []byte("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(png))
	}
	return []byte("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
}
