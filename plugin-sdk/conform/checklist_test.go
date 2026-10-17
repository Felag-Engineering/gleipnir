package conform

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

// itemRE matches a checklist item line: "- **TP-NAME-01** `auto` — ...".
var itemRE = regexp.MustCompile("^- \\*\\*([A-Z]+-[A-Z]+-[0-9]{2})\\*\\* `([a-z]+)`")

type checklistItem struct {
	id   string
	mode string
}

func readChecklist(t *testing.T, path string) []checklistItem {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening checklist: %v", err)
	}
	defer f.Close()
	var items []checklistItem
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := itemRE.FindStringSubmatch(sc.Text()); m != nil {
			items = append(items, checklistItem{id: m[1], mode: m[2]})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading checklist: %v", err)
	}
	return items
}

// TestToolProviderChecklistDrift pins the TP-* checks to
// checklists/tool-provider.md: every `auto` item has exactly one check, no
// other item does, and every TP-* check has an item. Changing either side
// alone fails here.
func TestToolProviderChecklistDrift(t *testing.T) {
	items := readChecklist(t, "checklists/tool-provider.md")
	if len(items) == 0 {
		t.Fatal("no items parsed from the checklist; the item line format changed")
	}

	modes := map[string]string{}
	for _, it := range items {
		if !strings.HasPrefix(it.id, "TP-") {
			t.Errorf("item %s in the tool-provider checklist is not a TP-* item", it.id)
		}
		if _, dup := modes[it.id]; dup {
			t.Errorf("item %s is listed twice", it.id)
		}
		switch it.mode {
		case "auto", "stimulus", "review":
		default:
			t.Errorf("item %s has mode %q, want auto, stimulus or review", it.id, it.mode)
		}
		modes[it.id] = it.mode
	}

	checks := map[string]bool{}
	for _, c := range Checks() {
		if !strings.HasPrefix(c.ID, "TP-") {
			continue
		}
		checks[c.ID] = true
		if c.Profile != profileToolProvider {
			t.Errorf("check %s has profile %q, want %q", c.ID, c.Profile, profileToolProvider)
		}
		mode, ok := modes[c.ID]
		switch {
		case !ok:
			t.Errorf("check %s has no item in the checklist", c.ID)
		case mode != "auto":
			t.Errorf("check %s implements a %q item; only auto items are checked by the runner", c.ID, mode)
		}
	}
	for id, mode := range modes {
		if mode == "auto" && !checks[id] {
			t.Errorf("auto item %s has no check", id)
		}
	}
}

func TestCheckIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Checks() {
		if seen[c.ID] {
			t.Errorf("check %s registered twice", c.ID)
		}
		seen[c.ID] = true
	}
}
