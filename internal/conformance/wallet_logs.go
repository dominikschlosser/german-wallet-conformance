package conformance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func walletLogPath(dir, id string) (string, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid test instance id %q", id)
	}
	return filepath.Join(dir, id+".txt"), nil
}

func writeWalletLog(dir, id string, data []byte) error {
	path, err := walletLogPath(dir, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ExportFailureLogs uses the latest instances selected by ReadReport.
func ExportFailureLogs(plans []ReportPlan, resultsDir, page string) error {
	plans = slices.Clone(plans)
	slices.SortFunc(plans, func(a, b ReportPlan) int {
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.Ref.Slug, b.Ref.Slug)
	})
	var doc bytes.Buffer
	fmt.Fprint(&doc, "# iOS test logs\n\nEach Failed test in the [iOS results](ios-results.md) has a page with its wallet log and suite log. Variant names match the runner's `--only` values. Filenames use suite test instance IDs.\n\n")
	files := map[string][]byte{}
	planName := ""
	for _, plan := range plans {
		var rows bytes.Buffer
		var suiteLogs map[string][]byte
		for _, module := range plan.Modules {
			if module.Verdict != "FAILED" {
				continue
			}
			path, err := walletLogPath(filepath.Join(resultsDir, "wallet-logs"), module.ID)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%s / %s: %w", plan.Ref.Slug, module.Name, err)
			}
			if len(bytes.TrimSpace(data)) == 0 {
				return fmt.Errorf("empty wallet log for %s / %s (%s)", plan.Ref.Slug, module.Name, module.ID)
			}
			if suiteLogs == nil {
				suiteLogs, err = readSuiteLogs(filepath.Join(resultsDir, plan.Ref.Slug+"-"+plan.Ref.ID+".zip"))
				if err != nil {
					return err
				}
			}
			suiteLog := suiteLogs[module.ID]
			if len(suiteLog) == 0 {
				return fmt.Errorf("missing suite log for %s / %s (%s)", plan.Ref.Slug, module.Name, module.ID)
			}
			settings := strings.TrimSpace(module.Variant["vci_credential_issuance_mode"] + " " + module.Variant["vci_credential_encryption"])
			name := module.Name
			if settings != "" {
				name = strings.TrimSuffix(name, " (deferred)") + " (" + settings + ")"
			}
			fmt.Fprintf(&rows, "| %s | %s | [Result and logs](test-results/%s.md) |\n", cell(name), cell(module.Verdict), module.ID)
			files[module.ID+"-wallet.txt"] = data
			files[module.ID+"-suite.json"] = suiteLog
			files[module.ID+".md"] = []byte(fmt.Sprintf("# %s\n\n| | |\n| --- | --- |\n| Plan | `%s` |\n| Variant | `%s` |\n| Result | Failed |\n| Test instance | `%s` |\n\n- [Wallet log](%s-wallet.txt): app output from launch until this test ends.\n- [Suite log](%s-suite.json): the suite's full JSON export for the same test instance, including protocol exchanges and checks.\n\n[All failed tests](../%s)\n", cell(name), cell(plan.Name), cell(plan.Ref.Slug), module.ID, module.ID, module.ID, filepath.Base(page)))
		}
		if rows.Len() == 0 {
			continue
		}
		if plan.Name != planName {
			fmt.Fprintf(&doc, "## %s\n\n", cell(plan.Name))
			planName = plan.Name
		}
		fmt.Fprintf(&doc, "### %s\n\n| Test | Result | Result and logs |\n| --- | --- | --- |\n%s\n", cell(plan.Ref.Slug), rows.String())
	}
	if len(files) == 0 {
		fmt.Fprintln(&doc, "No Failed tests in this selection.")
	}
	dir := filepath.Join(filepath.Dir(page), "test-results")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(page), 0755); err != nil {
		return err
	}
	return os.WriteFile(page, append(bytes.TrimRight(doc.Bytes(), "\n"), '\n'), 0644)
}

func readSuiteLogs(path string) (map[string][]byte, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	logs := map[string][]byte{}
	for _, entry := range archive.File {
		if !strings.HasSuffix(entry.Name, ".json") {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return nil, err
		}
		var exported struct {
			TestInfo struct {
				ID string `json:"testId"`
			}
			Results []json.RawMessage
		}
		if err := json.Unmarshal(data, &exported); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name, err)
		}
		if exported.TestInfo.ID != "" && len(exported.Results) > 0 {
			var formatted bytes.Buffer
			if err := json.Indent(&formatted, data, "", "  "); err != nil {
				return nil, fmt.Errorf("%s: %w", entry.Name, err)
			}
			logs[exported.TestInfo.ID] = append(bytes.TrimRight(formatted.Bytes(), "\n"), '\n')
		}
	}
	return logs, nil
}
