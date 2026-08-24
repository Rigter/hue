// Command gendemo regenerates the README's demo section from real output.
//
// For every image in example/images it runs hue.Extract with the same options
// the example CLI uses, writes a small solid-color PNG swatch per resulting
// color, and rewrites the block between the GENERATED DEMO markers in
// README.md with the resulting tables. The swatches and the numbers therefore
// always come from an actual extraction run rather than being hand-copied.
//
// Run from the repository root:
//
//	go generate ./...
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/rigter/hue"
)

// demoOpts mirrors example/main.go so the README reflects what that CLI prints.
var demoOpts = hue.Options{
	NumColors:       5,
	MaxSampleDim:    100,
	IgnoreNearWhite: true,
}

// demos lists the images to document, in README order. Titles and alt text are
// editorial, so they live here rather than being derived from the file name.
var demos = []struct {
	File  string
	Title string
	Alt   string
}{
	{"babylon-cinema-berlin.jpg", "Babylon cinema, Berlin", "Red bench at the Babylon cinema in Berlin"},
	{"batu-caves-stairs.jpg", "Rainbow stairs, Batu Caves", "Rainbow stairs at Batu Caves, Malaysia"},
	{"woman-on-sofa.jpg", "Woman on a sofa", "Woman in a white sweater reclining on a grey sofa"},
}

const (
	imagesDir   = "example/images"
	swatchesDir = "example/images/swatches"
	readmePath  = "README.md"

	beginMarker = "<!-- BEGIN GENERATED DEMO -->"
	endMarker   = "<!-- END GENERATED DEMO -->"

	swatchSize = 20
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gendemo:", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat(readmePath); err != nil {
		return fmt.Errorf("run from the repository root: %w", err)
	}
	if err := os.MkdirAll(swatchesDir, 0o755); err != nil {
		return err
	}

	var section strings.Builder
	written := map[string]bool{}

	for _, d := range demos {
		result, err := extract(filepath.Join(imagesDir, d.File))
		if err != nil {
			return fmt.Errorf("%s: %w", d.File, err)
		}

		fmt.Fprintf(&section, "### %s\n\n", d.Title)
		fmt.Fprintf(&section, "<img src=\"./%s/%s\" width=\"420\" alt=\"%s\">\n\n", imagesDir, d.File, d.Alt)
		section.WriteString("|   | Hex | RGB | Share | Luminance | Light? | Text on top |\n")
		section.WriteString("|---|-----|-----|-------|-----------|--------|-------------|\n")

		for _, c := range result.Colors {
			name := strings.TrimPrefix(c.Hex, "#")
			if !written[name] {
				if err := writeSwatch(name, c.R, c.G, c.B); err != nil {
					return err
				}
				written[name] = true
			}

			fmt.Fprintf(&section,
				"| <img src=\"./%s/%s.png\" width=\"16\" height=\"16\" alt=\"\"> | `%s` | %d, %d, %d | %.2f%% | %.2f | %s | `%s` |\n",
				swatchesDir, name, c.Hex, c.R, c.G, c.B,
				c.Percentage, c.Luminance, yesNo(c.IsLight), c.RecommendedTextColor,
			)
		}
		section.WriteString("\n")
	}
	if err := pruneStaleSwatches(written); err != nil {
		return err
	}

	if err := spliceReadme(strings.TrimRight(section.String(), "\n")); err != nil {
		return err
	}

	fmt.Printf("gendemo: %d images, %d unique swatches\n", len(demos), len(written))
	return nil
}

func extract(path string) (hue.Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return hue.Result{}, err
	}
	defer f.Close()
	return hue.Extract(f, demoOpts)
}

// writeSwatch emits a solid square of the given color with a translucent grey
// border, so pale swatches stay visible against GitHub's light theme and dark
// ones against its dark theme.
func writeSwatch(name string, r, g, b uint8) error {
	img := image.NewNRGBA(image.Rect(0, 0, swatchSize, swatchSize))
	fill := color.NRGBA{R: r, G: g, B: b, A: 255}
	border := color.NRGBA{R: 128, G: 128, B: 128, A: 140}

	for y := 0; y < swatchSize; y++ {
		for x := 0; x < swatchSize; x++ {
			if x == 0 || y == 0 || x == swatchSize-1 || y == swatchSize-1 {
				img.SetNRGBA(x, y, border)
				continue
			}
			img.SetNRGBA(x, y, fill)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(swatchesDir, name+".png"), buf.Bytes(), 0o644)
}

// pruneStaleSwatches removes obsolete generated swatches after an extraction
// change. The directory is generator-owned, and only six-digit hex PNG names
// are eligible, so unrelated files are left alone.
func pruneStaleSwatches(written map[string]bool) error {
	entries, err := os.ReadDir(swatchesDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".png")
		if entry.Name() == name || !isHexColorName(name) || written[name] {
			continue
		}
		if err := os.Remove(filepath.Join(swatchesDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isHexColorName(name string) bool {
	if len(name) != 6 {
		return false
	}
	for _, c := range name {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// spliceReadme replaces everything between the markers, leaving the
// hand-written prose around them untouched.
func spliceReadme(section string) error {
	data, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}

	begin := bytes.Index(data, []byte(beginMarker))
	end := bytes.Index(data, []byte(endMarker))
	if begin == -1 || end == -1 || end < begin {
		return fmt.Errorf("markers %q / %q not found in %s", beginMarker, endMarker, readmePath)
	}

	var out bytes.Buffer
	out.Write(data[:begin])
	out.WriteString(beginMarker + "\n\n")
	out.WriteString(section + "\n\n")
	out.Write(data[end:])

	return os.WriteFile(readmePath, out.Bytes(), 0o644)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
