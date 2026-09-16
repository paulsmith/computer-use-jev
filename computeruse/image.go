package computeruse

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/png"
	"net/http"
	"strings"
)

const (
	readImageMaxBytes = 5 * 1024 * 1024 / 4 * 3
	readImageMaxSide  = 8000
	// decodeAllocationBudget bounds allocations for one full image decode.
	decodeAllocationBudget = uint64(readImageMaxSide) * uint64(readImageMaxSide)
)

// ItemImage carries one inline image to a model provider.
type ItemImage struct {
	MIME    string
	DataB64 string
	Width   int64
	Height  int64
}

func ImagePlaceholder(image ItemImage) string {
	bytes := len(image.DataB64) / 4 * 3
	var size string
	switch {
	case bytes >= 1024*1024:
		size = fmt.Sprintf("%.1f MiB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		size = fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	default:
		size = fmt.Sprintf("%d bytes", bytes)
	}
	mime := image.MIME
	if mime == "" {
		mime = "image"
	}
	if image.Width > 0 && image.Height > 0 {
		return fmt.Sprintf("[image: %s, %dx%d, %s]", mime, image.Width, image.Height, size)
	}
	return fmt.Sprintf("[image: %s, %s]", mime, size)
}

type imageInfo struct {
	mime          string
	width, height int
	complete      bool
}

// sniffImage identifies supported image containers and parses best-effort dimensions.
func sniffImage(data []byte) (info imageInfo, ok bool) {
	info.mime = http.DetectContentType(data)
	if info.mime != "image/png" {
		return imageInfo{}, false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return info, true
	}
	info.width, info.height = config.Width, config.Height
	if info.width <= 0 || info.height <= 0 || info.width > readImageMaxSide || info.height > readImageMaxSide {
		return info, true
	}
	// The decoder allocates RGBA, so refuse declared dimensions whose decoded
	// bytes exceed the budget.
	if uint64(info.width)*uint64(info.height)*4 > decodeAllocationBudget {
		return info, true
	}
	_, _, err = image.Decode(bytes.NewReader(data))
	info.complete = err == nil
	return info, true
}

// decodeImageB64 strictly decodes a base64 image payload and bounds it by
// readImageMaxBytes, rejecting padding or whitespace variants that would not
// round-trip.
func decodeImageB64(encoded string) ([]byte, error) {
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n") || len(encoded) > base64.StdEncoding.EncodedLen(readImageMaxBytes) {
		return nil, fmt.Errorf("screenshot has invalid or oversized base64 data")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > readImageMaxBytes || base64.StdEncoding.EncodeToString(data) != encoded {
		return nil, fmt.Errorf("screenshot has invalid or oversized base64 data")
	}
	return data, nil
}

// computerUseImage validates a screenshot payload against the same limits as
// other image-producing tools.
func computerUseImage(encoded string) (ItemImage, error) {
	data, err := decodeImageB64(encoded)
	if err != nil {
		return ItemImage{}, err
	}
	info, ok := sniffImage(data)
	if !ok || info.mime != "image/png" || info.width <= 0 || info.height <= 0 || !info.complete {
		return ItemImage{}, fmt.Errorf("screenshot is malformed or uses an unsupported image format")
	}
	if info.width > readImageMaxSide || info.height > readImageMaxSide {
		return ItemImage{}, fmt.Errorf("screenshot dimensions exceed %d pixels per side", readImageMaxSide)
	}
	return ItemImage{MIME: info.mime, DataB64: encoded, Width: int64(info.width), Height: int64(info.height)}, nil
}
