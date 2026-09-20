package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// assetRef matches a link to a recorded image, a chapter of the film or its
// subtitle track, from the README (which is one directory above the assets) or
// from a page under docs.
var assetRef = regexp.MustCompile(`assets/([A-Za-z0-9._-]+\.(?:png|gif|mp4|srt))`)

// docsMarkdown returns every markdown file that may point at an asset, with
// its contents, keyed by the path a failure should name.
func docsMarkdown(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string]string{}
	add := func(rel string) {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = string(data)
	}
	add("README.md")
	pages, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		add(filepath.Join("docs", filepath.Base(page)))
	}
	return out
}

// TestDocsAssetsAreUsed keeps the recorded images and the pages in step: a
// page may not point at an image that was never recorded, and an image that no
// page shows is dead weight in the repository.
func TestDocsAssetsAreUsed(t *testing.T) {
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "docs", "assets"))
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			recorded[entry.Name()] = false
		}
	}
	if len(recorded) == 0 {
		t.Fatal("no recorded assets")
	}

	pages := docsMarkdown(t)
	t.Run("every reference names a recorded asset", func(t *testing.T) {
		for page, body := range pages {
			for _, m := range assetRef.FindAllStringSubmatch(body, -1) {
				name := m[1]
				if _, ok := recorded[name]; !ok {
					t.Errorf("%s points at docs/assets/%s, which was never recorded", page, name)
					continue
				}
				recorded[name] = true
			}
		}
	})
	t.Run("every recorded asset is shown", func(t *testing.T) {
		var unused []string
		for name, used := range recorded {
			if !used {
				unused = append(unused, name)
			}
		}
		if len(unused) > 0 {
			t.Errorf("no page shows: %s", strings.Join(unused, ", "))
		}
	})
}

// TestDocsScenesProduceTheAssets ties each shown asset back to the scene that
// records it, so an image cannot survive the scene it came from. A picture of
// a documentation page comes from docs/scenes, a chapter of the film from
// docs/video, and the film itself from all of them at once.
func TestDocsScenesProduceTheAssets(t *testing.T) {
	root := repoRoot(t)
	known := map[string]bool{}
	chapters := map[string]bool{}
	for _, dir := range []struct {
		name string
		into map[string]bool
	}{
		{name: "scenes", into: known},
		{name: "video", into: chapters},
	} {
		scenes, err := filepath.Glob(filepath.Join(root, "docs", dir.name, "*.scene"))
		if err != nil {
			t.Fatal(err)
		}
		if len(scenes) == 0 {
			t.Fatalf("no scene under docs/%s", dir.name)
		}
		for _, scene := range scenes {
			dir.into[strings.TrimSuffix(filepath.Base(scene), ".scene")] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs", "assets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// The film and its subtitle track are every chapter joined, so they
		// answer to no single scene.
		if name == "demo.mp4" || name == "demo.srt" {
			continue
		}
		if chapter, ok := strings.CutPrefix(strings.TrimSuffix(name, ".mp4"), "demo-"); ok {
			if !chapters[chapter] {
				t.Errorf("docs/assets/%s has no chapter under docs/video", name)
			}
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".png"), ".gif")
		if !known[stem] {
			t.Errorf("docs/assets/%s has no scene under docs/scenes", name)
		}
	}
}
