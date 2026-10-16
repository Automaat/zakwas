package docs_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/platform"
)

type platformRow struct{ name, macOS, linux string }

func platformTables(t *testing.T) (modules, options []platformRow) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "config.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), "\n## Platform support\n")
	if !ok {
		t.Fatal("docs/config.md has no Platform support section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	var current *[]platformRow
	for line := range strings.SplitSeq(section, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		switch {
		case !strings.HasPrefix(line, "|"):
		case cells[0] == "Module":
			current = &modules
		case cells[0] == "Option":
			current = &options
		case strings.HasPrefix(cells[0], "---"):
		case current == nil || len(cells) < 3:
			t.Fatalf("unexpected row %q", line)
		default:
			*current = append(*current, platformRow{strings.Trim(cells[0], "`"), cells[1], cells[2]})
		}
	}
	return modules, options
}

func TestPlatformTableMatchesTheSourceOfTruth(t *testing.T) {
	modules, options := platformTables(t)

	var documented []string
	for _, row := range modules {
		documented = append(documented, row.name)
		for goos, cell := range map[string]string{platform.Darwin: row.macOS, platform.Linux: row.linux} {
			want := "no"
			if platform.Supported(row.name, goos) {
				want = "yes"
			}
			if cell != want {
				t.Errorf("module %s on %s: docs say %q, platform says %q", row.name, goos, cell, want)
			}
		}
	}
	if want := platform.Names(); !slices.Equal(documented, want) {
		t.Errorf("documented modules %v, platform modules %v (same names, same apply order)", documented, want)
	}

	var documentedOptions []string
	for _, row := range options {
		documentedOptions = append(documentedOptions, row.name)
		var feature *platform.Feature
		for i := range platform.Features {
			if platform.Features[i].Name == row.name {
				feature = &platform.Features[i]
			}
		}
		if feature == nil {
			t.Errorf("option %s is documented but not in platform.Features", row.name)
			continue
		}
		for goos, cell := range map[string]string{platform.Darwin: row.macOS, platform.Linux: row.linux} {
			want := "no"
			switch {
			case platform.FeatureSupported(row.name, goos):
				want = "yes"
			case feature.Fallback != "":
				want = "fallback"
			}
			if cell != want {
				t.Errorf("option %s on %s: docs say %q, platform says %q", row.name, goos, cell, want)
			}
		}
	}
	var features []string
	for _, f := range platform.Features {
		features = append(features, f.Name)
	}
	if !slices.Equal(documentedOptions, features) {
		t.Errorf("documented options %v, platform options %v", documentedOptions, features)
	}
}

func TestREADMEListsTheMacOnlySections(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for l := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(l, "macOS only: "); ok {
			line, _, _ = strings.Cut(rest, ". ")
		}
	}
	if line == "" {
		t.Fatal("README.md has no 'macOS only: ...' line")
	}
	var listed []string
	for i, part := range strings.Split(line, "`") {
		if i%2 == 1 {
			listed = append(listed, part)
		}
	}
	want := platform.UnsupportedModules(platform.Linux)
	for _, f := range platform.Features {
		if !platform.FeatureSupported(f.Name, platform.Linux) && f.Fallback == "" {
			want = append(want, f.Name)
		}
	}
	slices.Sort(listed)
	slices.Sort(want)
	if !slices.Equal(listed, want) {
		t.Errorf("README lists %v as macOS only, platform says %v", listed, want)
	}
}
