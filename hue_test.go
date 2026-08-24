package hue

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// makeSolidBlocksImage builds a synthetic image split into vertical color
// blocks with known pixel proportions, so tests can assert exact
// percentages instead of eyeballing "roughly right" output.
func makeSolidBlocksImage(w, h int, blocks []struct {
	color.RGBA
	widthRatio float64
}) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	x := 0
	for i, b := range blocks {
		blockW := int(float64(w) * b.widthRatio)
		endX := x + blockW
		if i == len(blocks)-1 {
			endX = w // avoid rounding gaps on the last block
		}
		for px := x; px < endX; px++ {
			for py := 0; py < h; py++ {
				img.Set(px, py, b.RGBA)
			}
		}
		x = endX
	}
	return img
}

func TestExtractFromImage_KnownProportions(t *testing.T) {
	red := color.RGBA{220, 40, 40, 255}
	blue := color.RGBA{30, 60, 200, 255}
	green := color.RGBA{40, 180, 90, 255}

	img := makeSolidBlocksImage(300, 100, []struct {
		color.RGBA
		widthRatio float64
	}{
		{red, 0.5},
		{blue, 0.25},
		{green, 0.25},
	})

	result, err := ExtractFromImage(img, Options{NumColors: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fewer distinct colors than requested NumColors must not produce
	// duplicate/split buckets (regression test for the median-cut bug
	// where a uniform bucket kept getting split).
	if len(result.Colors) != 3 {
		t.Fatalf("expected 3 distinct colors, got %d: %+v", len(result.Colors), result.Colors)
	}

	// Sorted descending by percentage: red (50%) must come first.
	if result.Colors[0].Hex != "#dc2828" {
		t.Errorf("expected #dc2828 first, got %s", result.Colors[0].Hex)
	}
	if result.Colors[0].Percentage < 49 || result.Colors[0].Percentage > 51 {
		t.Errorf("expected ~50%% for dominant color, got %.2f", result.Colors[0].Percentage)
	}
}

func TestExtractFromImage_LightDarkClassification(t *testing.T) {
	yellow := color.RGBA{250, 230, 90, 255} // clearly light
	navy := color.RGBA{15, 20, 60, 255}     // clearly dark

	img := makeSolidBlocksImage(200, 100, []struct {
		color.RGBA
		widthRatio float64
	}{
		{yellow, 0.5},
		{navy, 0.5},
	})

	result, err := ExtractFromImage(img, Options{NumColors: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Colors) != 2 {
		t.Fatalf("expected 2 colors, got %d", len(result.Colors))
	}

	for _, c := range result.Colors {
		switch c.Hex {
		case "#fae65a": // yellow
			if !c.IsLight {
				t.Errorf("yellow should be classified as light, luminance=%.2f", c.Luminance)
			}
			if c.RecommendedTextColor != "#000000" {
				t.Errorf("yellow should recommend black text, got %s", c.RecommendedTextColor)
			}
		case "#0f143c": // navy
			if c.IsLight {
				t.Errorf("navy should be classified as dark, luminance=%.2f", c.Luminance)
			}
			if c.RecommendedTextColor != "#ffffff" {
				t.Errorf("navy should recommend white text, got %s", c.RecommendedTextColor)
			}
		default:
			t.Errorf("unexpected color in result: %s", c.Hex)
		}
	}
}

func TestExtractFromImage_RecommendedTextUsesContrastRatio(t *testing.T) {
	// This mid-tone brown has luminance below 0.5, so IsLight is false, but
	// black has substantially more WCAG contrast than white on it.
	brown := color.RGBA{164, 141, 110, 255}
	img := makeSolidBlocksImage(10, 10, []struct {
		color.RGBA
		widthRatio float64
	}{{brown, 1}})

	result, err := ExtractFromImage(img, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := result.Colors[0]
	if got.IsLight {
		t.Fatalf("expected the mid-tone color to remain classified as dark")
	}
	if got.RecommendedTextColor != "#000000" {
		t.Errorf("expected black text for maximum contrast, got %s", got.RecommendedTextColor)
	}
}

func TestExtractFromImage_CompositesSemiTransparentPixelsOverWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 0, B: 0, A: 128})

	result, err := ExtractFromImage(img, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.Colors[0].Hex; got != "#ff7f7f" {
		t.Errorf("expected 50%% transparent red over white as #ff7f7f, got %s", got)
	}
}

func TestSplitBucketOrdersEqualPrimaryChannelsDeterministically(t *testing.T) {
	left, right := splitBucket(bucket{pixels: []pixel{
		{r: 0, g: 2, b: 2},
		{r: 0, g: 1, b: 1},
		{r: 0, g: 0, b: 2},
		{r: 5, g: 0, b: 0},
	}})

	got := append(left.pixels, right.pixels...)
	want := []pixel{
		{r: 0, g: 0, b: 2},
		{r: 0, g: 1, b: 1},
		{r: 0, g: 2, b: 2},
		{r: 5, g: 0, b: 0},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pixel %d: expected %+v, got %+v", i, want[i], got[i])
		}
	}
}

func TestExtractFromImage_IgnoreNearWhite(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	red := color.RGBA{220, 40, 40, 255}

	img := makeSolidBlocksImage(200, 100, []struct {
		color.RGBA
		widthRatio float64
	}{
		{white, 0.8}, // dominant by pixel count, but should be filtered out
		{red, 0.2},
	})

	result, err := ExtractFromImage(img, Options{
		NumColors:       5,
		IgnoreNearWhite: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Colors) != 1 {
		t.Fatalf("expected only red to survive the white filter, got %+v", result.Colors)
	}
	if result.Colors[0].Hex != "#dc2828" {
		t.Errorf("expected #dc2828, got %s", result.Colors[0].Hex)
	}
	if result.Colors[0].Percentage != 100 {
		t.Errorf("expected 100%% once white pixels are excluded, got %.2f", result.Colors[0].Percentage)
	}
}

func TestExtractFromImage_EmptyImageErrors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 0, 0))
	_, err := ExtractFromImage(img, Options{})
	if err == nil {
		t.Fatal("expected an error for a zero-size image, got nil")
	}
}

func TestExtract_DecodesPNGAndProducesValidJSON(t *testing.T) {
	red := color.RGBA{220, 40, 40, 255}
	img := makeSolidBlocksImage(50, 50, []struct {
		color.RGBA
		widthRatio float64
	}{
		{red, 1.0},
	})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("failed to encode test PNG: %v", err)
	}

	jsonBytes, err := ExtractJSON(bytes.NewReader(buf.Bytes()), Options{NumColors: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded Result
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(decoded.Colors) == 0 {
		t.Fatal("expected at least one color in decoded JSON")
	}
}

func TestOptions_Defaults(t *testing.T) {
	got := Options{}.withDefaults()
	if got.NumColors != 5 {
		t.Errorf("expected default NumColors=5, got %d", got.NumColors)
	}
	if got.MaxSampleDim != 100 {
		t.Errorf("expected default MaxSampleDim=100, got %d", got.MaxSampleDim)
	}
}
