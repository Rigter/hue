// Package hue extracts the dominant colors from an image using
// the median-cut quantization algorithm. It is dependency-free (stdlib only),
// deterministic, and fast thanks to aggressive pixel sampling.
package hue

//go:generate go run ./tools/gendemo

import (
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"  // register decoder
	_ "image/jpeg" // register decoder
	_ "image/png"  // register decoder
	"io"
	"math"
	"sort"
)

// ColorInfo represents a single dominant color result.
type ColorInfo struct {
	Hex        string  `json:"hex"`
	R          uint8   `json:"r"`
	G          uint8   `json:"g"`
	B          uint8   `json:"b"`
	Percentage float64 `json:"percentage"` // 0-100, share of sampled pixels

	// Luminance is the WCAG relative luminance of the color, in [0, 1].
	// 0 = black, 1 = white. Exposed in case the caller wants finer-grained
	// control than the IsLight boolean (e.g. custom contrast thresholds).
	Luminance float64 `json:"luminance"`

	// IsLight classifies the color as visually light when Luminance > 0.5.
	// This display-oriented classification is intentionally independent from
	// RecommendedTextColor, which uses the WCAG contrast-ratio crossover.
	IsLight bool `json:"isLight"`

	// RecommendedTextColor is the "#000000" or "#ffffff" color with the
	// greater WCAG contrast ratio against this color.
	RecommendedTextColor string `json:"recommendedTextColor"`
}

// Result is the top-level JSON-serializable output.
type Result struct {
	Colors []ColorInfo `json:"colors"`
}

// Options controls extraction behavior.
type Options struct {
	// NumColors is how many dominant colors to return. Default: 5.
	NumColors int
	// MaxSampleDim caps the width/height used for sampling (the image is
	// logically downsampled to at most this many pixels per side before
	// analysis). Bigger = more accurate, slower. Default: 100.
	MaxSampleDim int
	// IgnoreNearWhite/IgnoreNearBlack skip background-ish pixels, which is
	// useful for product photos on white backgrounds. Default: false.
	IgnoreNearWhite bool
	IgnoreNearBlack bool
}

func (o Options) withDefaults() Options {
	if o.NumColors <= 0 {
		o.NumColors = 5
	}
	if o.MaxSampleDim <= 0 {
		o.MaxSampleDim = 100
	}
	return o
}

// maxRefineIterations bounds Lloyd's algorithm. Seeded from median-cut the
// assignment converges in a handful of passes; the cap only guards against
// pathological oscillation.
const maxRefineIterations = 16

// pixel is a lightweight internal representation (avoids repeated
// color.Color -> RGBA conversions and interface overhead during sorting).
type pixel struct {
	r, g, b uint8
}

// bucket is a group of pixels produced during median-cut splitting.
type bucket struct {
	pixels []pixel
}

// Extract decodes img (any format registered via blank imports above,
// i.e. jpeg/png/gif) and returns its dominant colors as Result.
func Extract(r io.Reader, opts Options) (Result, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return Result{}, err
	}
	return ExtractFromImage(img, opts)
}

// ExtractFromImage runs the same pipeline on an already-decoded image.Image,
// useful if the caller already has one in memory (e.g. from another step).
func ExtractFromImage(img image.Image, opts Options) (Result, error) {
	opts = opts.withDefaults()

	pixels := samplePixels(img, opts)
	if len(pixels) == 0 {
		return Result{}, errors.New("dominantcolor: no pixels sampled from image")
	}

	// Median-cut picks well-spread seed colors; refine turns them into real
	// clusters so both the colors and their shares reflect the image.
	buckets := medianCut(pixels, opts.NumColors)
	seeds := make([]pixel, 0, len(buckets))
	for _, b := range buckets {
		if len(b.pixels) == 0 {
			continue
		}
		seeds = append(seeds, averageColor(b.pixels))
	}
	if len(seeds) == 0 {
		return Result{}, errors.New("hue: no colors extracted from image")
	}

	centroids, counts := refine(pixels, seeds, maxRefineIterations)

	total := len(pixels)
	colors := make([]ColorInfo, 0, len(centroids))
	for i, c := range centroids {
		if counts[i] == 0 {
			continue // cluster lost every pixel to a nearer centroid
		}
		luminance := relativeLuminance(c)
		colors = append(colors, ColorInfo{
			Hex:                  hexString(c),
			R:                    c.r,
			G:                    c.g,
			B:                    c.b,
			Percentage:           round2(100 * float64(counts[i]) / float64(total)),
			Luminance:            round2(luminance),
			IsLight:              luminance > 0.5,
			RecommendedTextColor: textColorFor(luminance),
		})
	}

	// Most dominant first. SliceStable plus the hex tiebreaker keeps the order
	// reproducible when two colors hold an identical share.
	sort.SliceStable(colors, func(i, j int) bool {
		if colors[i].Percentage != colors[j].Percentage {
			return colors[i].Percentage > colors[j].Percentage
		}
		return colors[i].Hex < colors[j].Hex
	})

	return Result{Colors: colors}, nil
}

// refine runs Lloyd's algorithm (the assignment/update loop of k-means) seeded
// with the median-cut centroids. Median-cut splits each bucket at its median
// *index*, so bucket populations are fixed by the shape of the split tree
// (always 50/25/25 for NumColors=3) and say nothing about the image. Worse, on
// unbalanced images a split lands inside a single dominant color, which merges
// unrelated colors into one bucket and yields phantom or duplicate palette
// entries.
//
// Reassigning every sampled pixel to its nearest centroid and recomputing the
// centroids fixes both: Percentage becomes the real share of pixels, and the
// colors converge onto the image's actual clusters. Seeding from median-cut
// (rather than randomly, as textbook k-means does) keeps the result fully
// deterministic, and ties are broken by lowest centroid index.
func refine(pixels []pixel, seeds []pixel, maxIter int) ([]pixel, []int) {
	k := len(seeds)
	centroids := make([]pixel, k)
	copy(centroids, seeds)

	counts := make([]int, k)
	sumR := make([]int, k)
	sumG := make([]int, k)
	sumB := make([]int, k)
	assign := make([]int, len(pixels))
	for i := range assign {
		assign[i] = -1
	}

	for iter := 0; iter < maxIter; iter++ {
		for i := 0; i < k; i++ {
			counts[i], sumR[i], sumG[i], sumB[i] = 0, 0, 0, 0
		}

		changed := false
		for pi, p := range pixels {
			best, bestDist := 0, dist2(p, centroids[0])
			for ci := 1; ci < k; ci++ {
				// strict < keeps the lowest index on ties => deterministic
				if d := dist2(p, centroids[ci]); d < bestDist {
					best, bestDist = ci, d
				}
			}
			if assign[pi] != best {
				assign[pi] = best
				changed = true
			}
			counts[best]++
			sumR[best] += int(p.r)
			sumG[best] += int(p.g)
			sumB[best] += int(p.b)
		}

		empty := false
		for ci := 0; ci < k; ci++ {
			if counts[ci] == 0 {
				empty = true
				continue
			}
			centroids[ci] = pixel{
				r: uint8(sumR[ci] / counts[ci]),
				g: uint8(sumG[ci] / counts[ci]),
				b: uint8(sumB[ci] / counts[ci]),
			}
		}

		// A centroid can lose every pixel to a nearer one - median-cut emits
		// duplicate seeds when a split lands inside a single dominant color.
		// Rather than dropping the slot (which silently returns fewer colors
		// than requested), move it onto the worst-represented pixel, the
		// standard furthest-point reseed. That recovers small but distinct
		// colors, e.g. the 5% blue and green blocks of a 90/5/5 image.
		if empty && iter < maxIter-1 {
			if reseedEmpty(pixels, assign, centroids, counts) {
				changed = true
			}
		}

		if !changed {
			break
		}
	}

	return centroids, counts
}

// reseedEmpty moves every centroid that holds no pixels onto the pixel that
// sits furthest from the centroid it was assigned to, so the next iteration
// can grow a real cluster there. Pixels already chosen in this pass are
// skipped so two empty centroids never land on the same color. It reports
// whether anything moved.
func reseedEmpty(pixels []pixel, assign []int, centroids []pixel, counts []int) bool {
	moved := false
	taken := make(map[pixel]bool)

	for ci := range centroids {
		if counts[ci] != 0 {
			continue
		}

		bestIdx, bestDist := -1, -1
		for pi, p := range pixels {
			if taken[p] {
				continue
			}
			if d := dist2(p, centroids[assign[pi]]); d > bestDist {
				bestIdx, bestDist = pi, d
			}
		}
		if bestIdx == -1 || bestDist == 0 {
			continue // every pixel already sits exactly on a centroid
		}

		centroids[ci] = pixels[bestIdx]
		taken[pixels[bestIdx]] = true
		moved = true
	}

	return moved
}

// dist2 is the squared Euclidean distance in RGB. Squared avoids a sqrt in the
// inner loop, and monotonicity means it orders candidates identically.
func dist2(a, b pixel) int {
	dr := int(a.r) - int(b.r)
	dg := int(a.g) - int(b.g)
	db := int(a.b) - int(b.b)
	return dr*dr + dg*dg + db*db
}

// ExtractJSON is a convenience wrapper that returns the marshaled JSON bytes
// directly, which is likely what you want for an HTTP handler or CLI.
func ExtractJSON(r io.Reader, opts Options) ([]byte, error) {
	result, err := Extract(r, opts)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// samplePixels walks the image on a fixed grid sized to opts.MaxSampleDim,
// so cost stays bounded regardless of the source image's resolution
// (a 4000x3000 photo and a 400x300 photo cost roughly the same to analyze).
func samplePixels(img image.Image, opts Options) []pixel {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return nil
	}

	stepX := maxInt(1, w/opts.MaxSampleDim)
	stepY := maxInt(1, h/opts.MaxSampleDim)

	pixels := make([]pixel, 0, (w/stepX+1)*(h/stepY+1))

	for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
		for x := bounds.Min.X; x < bounds.Max.X; x += stepX {
			r, g, b, a := img.At(x, y).RGBA()
			if a == 0 {
				continue // fully transparent, skip
			}
			// RGBA returns alpha-premultiplied 16-bit channels. Composite partial
			// transparency over white so extracted colors match a conventional
			// light background rather than becoming artificially dark.
			p := pixel{
				r: uint8((r + 0xffff - a) >> 8),
				g: uint8((g + 0xffff - a) >> 8),
				b: uint8((b + 0xffff - a) >> 8),
			}

			if opts.IgnoreNearWhite && isNearWhite(p) {
				continue
			}
			if opts.IgnoreNearBlack && isNearBlack(p) {
				continue
			}
			pixels = append(pixels, p)
		}
	}
	return pixels
}

// medianCut recursively splits pixels along their widest color-channel range,
// producing 2^k buckets each iteration until numColors buckets exist.
// This is the classic median-cut quantization algorithm: deterministic,
// O(n log n), and well suited to "top N dominant colors" use cases.
func medianCut(pixels []pixel, numColors int) []bucket {
	buckets := []bucket{{pixels: pixels}}

	for len(buckets) < numColors {
		// Find the bucket with the greatest color range to split next;
		// splitting the biggest bucket each round gives more balanced,
		// visually distinct results than always splitting bucket 0.
		splitIdx := largestRangeBucket(buckets)
		if splitIdx == -1 {
			break // no bucket can be split further
		}

		b := buckets[splitIdx]
		left, right := splitBucket(b)

		buckets = append(buckets[:splitIdx], buckets[splitIdx+1:]...)
		buckets = append(buckets, left, right)
	}

	return buckets
}

// largestRangeBucket returns the index of the splittable bucket (>=2 pixels
// AND non-zero color spread) whose widest single-channel range is greatest,
// or -1 if none qualifies. Requiring spread > 0 prevents the algorithm from
// pointlessly splitting an already-uniform bucket into duplicate colors when
// the image has fewer distinct colors than the requested NumColors.
func largestRangeBucket(buckets []bucket) int {
	best := -1
	bestRange := 0
	for i, b := range buckets {
		if len(b.pixels) < 2 {
			continue
		}
		_, r := widestChannel(b.pixels)
		if r > bestRange {
			bestRange = r
			best = i
		}
	}
	return best
}

// channel identifies which color component has the widest spread in a
// set of pixels (0=R, 1=G, 2=B), which is what median-cut sorts on.
func widestChannel(pixels []pixel) (channel int, spread int) {
	minR, maxR := uint8(255), uint8(0)
	minG, maxG := uint8(255), uint8(0)
	minB, maxB := uint8(255), uint8(0)

	for _, p := range pixels {
		minR, maxR = minU8(minR, p.r), maxU8(maxR, p.r)
		minG, maxG = minU8(minG, p.g), maxU8(maxG, p.g)
		minB, maxB = minU8(minB, p.b), maxU8(maxB, p.b)
	}

	rRange := int(maxR) - int(minR)
	gRange := int(maxG) - int(minG)
	bRange := int(maxB) - int(minB)

	switch {
	case rRange >= gRange && rRange >= bRange:
		return 0, rRange
	case gRange >= rRange && gRange >= bRange:
		return 1, gRange
	default:
		return 2, bRange
	}
}

// splitBucket sorts pixels by their widest channel and divides them at the
// median, producing two buckets of roughly equal pixel count.
func splitBucket(b bucket) (bucket, bucket) {
	channel, _ := widestChannel(b.pixels)

	sort.Slice(b.pixels, func(i, j int) bool {
		return pixelLess(b.pixels[i], b.pixels[j], channel)
	})

	mid := len(b.pixels) / 2
	return bucket{pixels: b.pixels[:mid]}, bucket{pixels: b.pixels[mid:]}
}

// pixelLess orders pixels by the selected channel, then by the two remaining
// channels. Median-cut uses the order to choose bucket boundaries, so a total
// order is necessary for deterministic results across Go versions.
func pixelLess(a, b pixel, channel int) bool {
	var primaryA, secondaryA, tertiaryA uint8
	var primaryB, secondaryB, tertiaryB uint8
	switch channel {
	case 0:
		primaryA, secondaryA, tertiaryA = a.r, a.g, a.b
		primaryB, secondaryB, tertiaryB = b.r, b.g, b.b
	case 1:
		primaryA, secondaryA, tertiaryA = a.g, a.r, a.b
		primaryB, secondaryB, tertiaryB = b.g, b.r, b.b
	default:
		primaryA, secondaryA, tertiaryA = a.b, a.r, a.g
		primaryB, secondaryB, tertiaryB = b.b, b.r, b.g
	}

	if primaryA != primaryB {
		return primaryA < primaryB
	}
	if secondaryA != secondaryB {
		return secondaryA < secondaryB
	}
	return tertiaryA < tertiaryB
}

// averageColor computes the mean R/G/B across a bucket's pixels, used as
// that bucket's representative dominant color.
func averageColor(pixels []pixel) pixel {
	var rSum, gSum, bSum int
	for _, p := range pixels {
		rSum += int(p.r)
		gSum += int(p.g)
		bSum += int(p.b)
	}
	n := len(pixels)
	return pixel{
		r: uint8(rSum / n),
		g: uint8(gSum / n),
		b: uint8(bSum / n),
	}
}

// relativeLuminance computes the WCAG 2.x relative luminance of a color,
// the standard formula used for accessibility contrast calculations.
// It gamma-corrects each channel before weighting them, which matches how
// human eyes perceive brightness (green contributes far more than blue).
// See: https://www.w3.org/TR/WCAG20/#relativeluminancedef
func relativeLuminance(p pixel) float64 {
	linearize := func(c uint8) float64 {
		cs := float64(c) / 255
		if cs <= 0.03928 {
			return cs / 12.92
		}
		return math.Pow((cs+0.055)/1.055, 2.4)
	}
	r := linearize(p.r)
	g := linearize(p.g)
	b := linearize(p.b)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// textColorFor returns the hex color ("#000000" or "#ffffff") that gives
// the greater WCAG contrast ratio against a background of the given luminance.
func textColorFor(luminance float64) string {
	// Contrast with black is (L + 0.05) / 0.05; contrast with white is
	// 1.05 / (L + 0.05). They meet at sqrt(0.0525) - 0.05.
	const blackTextCrossover = 0.179128784747792
	if luminance > blackTextCrossover {
		return "#000000"
	}
	return "#ffffff"
}

func hexString(p pixel) string {
	const hexDigits = "0123456789abcdef"
	buf := make([]byte, 7)
	buf[0] = '#'
	buf[1], buf[2] = hexDigits[p.r>>4], hexDigits[p.r&0xF]
	buf[3], buf[4] = hexDigits[p.g>>4], hexDigits[p.g&0xF]
	buf[5], buf[6] = hexDigits[p.b>>4], hexDigits[p.b&0xF]
	return string(buf)
}

func isNearWhite(p pixel) bool {
	const threshold = 240
	return p.r >= threshold && p.g >= threshold && p.b >= threshold
}

func isNearBlack(p pixel) bool {
	const threshold = 15
	return p.r <= threshold && p.g <= threshold && p.b <= threshold
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minU8(a, b uint8) uint8 {
	if a < b {
		return a
	}
	return b
}

func maxU8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}
